package internal

import (
	"encoding/xml"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

const Namespace = "DAV:"

var (
	ResourceTypeName     = xml.Name{Namespace, "resourcetype"}
	DisplayNameName      = xml.Name{Namespace, "displayname"}
	GetContentLengthName = xml.Name{Namespace, "getcontentlength"}
	GetContentTypeName   = xml.Name{Namespace, "getcontenttype"}
	GetLastModifiedName  = xml.Name{Namespace, "getlastmodified"}
	GetETagName          = xml.Name{Namespace, "getetag"}

	CollectionName = xml.Name{Namespace, "collection"}
	PrincipalName  = xml.Name{Namespace, "principal"}

	CurrentUserPrincipalName    = xml.Name{Namespace, "current-user-principal"}
	CurrentUserPrivilegeSetName = xml.Name{Namespace, "current-user-privilege-set"}
)

type Status struct {
	Code int
	Text string
}

func (s *Status) MarshalText() ([]byte, error) {
	text := s.Text
	if text == "" {
		text = http.StatusText(s.Code)
	}
	return []byte(fmt.Sprintf("HTTP/1.1 %v %v", s.Code, text)), nil
}

func (s *Status) UnmarshalText(b []byte) error {
	if len(b) == 0 {
		return nil
	}

	parts := strings.SplitN(string(b), " ", 3)
	if len(parts) != 3 {
		return fmt.Errorf("webdav: invalid HTTP status %q: expected 3 fields", s)
	}
	code, err := strconv.Atoi(parts[1])
	if err != nil {
		return fmt.Errorf("webdav: invalid HTTP status %q: failed to parse code: %v", s, err)
	}

	s.Code = code
	s.Text = parts[2]
	return nil
}

func (s *Status) Err() error {
	if s == nil {
		return nil
	}

	// TODO: handle 2xx, 3xx
	if s.Code != http.StatusOK {
		return &HTTPError{Code: s.Code}
	}
	return nil
}

type Href url.URL

func (h *Href) String() string {
	u := (*url.URL)(h)
	return u.String()
}

// pathOrURL returns the path of a relative href, or the whole URL of an
// absolute one.
func (h *Href) pathOrURL() string {
	if h.Host == "" {
		return h.Path
	}
	return h.String()
}

func (h *Href) MarshalText() ([]byte, error) {
	return []byte(h.String()), nil
}

func (h *Href) UnmarshalText(b []byte) error {
	u, err := url.Parse(string(b))
	if err != nil {
		return err
	}
	*h = Href(*u)
	return nil
}

// https://tools.ietf.org/html/rfc4918#section-14.16
type MultiStatus struct {
	XMLName             xml.Name   `xml:"DAV: multistatus"`
	Responses           []Response `xml:"response"`
	ResponseDescription string     `xml:"responsedescription,omitempty"`
	SyncToken           string     `xml:"sync-token,omitempty"`
}

func NewMultiStatus(resps ...Response) *MultiStatus {
	return &MultiStatus{Responses: resps}
}

// https://tools.ietf.org/html/rfc4918#section-14.24
//
// Fields are in the order required by the RFC.
type Response struct {
	XMLName             xml.Name   `xml:"DAV: response"`
	Hrefs               []Href     `xml:"href"`
	Status              *Status    `xml:"status,omitempty"`
	PropStats           []PropStat `xml:"propstat,omitempty"`
	Error               *Error     `xml:"error,omitempty"`
	ResponseDescription string     `xml:"responsedescription,omitempty"`
	Location            *Location  `xml:"location,omitempty"`

	// For a response received by a Client, the client and the URL its
	// relative hrefs are relative to, for HrefPath
	client *Client
	base   *url.URL
}

func NewOKResponse(path string) *Response {
	href := Href{Path: path}
	return &Response{
		Hrefs:  []Href{href},
		Status: &Status{Code: http.StatusOK},
	}
}

func NewErrorResponse(path string, err error) *Response {
	code := http.StatusInternalServerError
	var httpErr *HTTPError
	if errors.As(err, &httpErr) {
		code = httpErr.Code
	}

	var errElt *Error
	errors.As(err, &errElt)

	// The status already conveys the code of an HTTPError, and the error
	// element a DAV:error: only describe the rest.
	var desc string
	if httpErr, ok := err.(*HTTPError); ok {
		err = httpErr.Err
	}
	if err != nil && err != error(errElt) {
		desc = err.Error()
	}

	href := Href{Path: path}
	return &Response{
		Hrefs:               []Href{href},
		Status:              &Status{Code: code},
		ResponseDescription: desc,
		Error:               errElt,
	}
}

func (resp *Response) Err() error {
	if resp.Status == nil || resp.Status.Code/100 == 2 {
		return nil
	}

	var err error
	if resp.Error != nil {
		err = resp.Error
	}
	if resp.ResponseDescription != "" {
		if err != nil {
			err = fmt.Errorf("%v (%w)", resp.ResponseDescription, err)
		} else {
			err = fmt.Errorf("%v", resp.ResponseDescription)
		}
	}

	err = &HTTPError{Code: resp.Status.Code, Err: err}
	if len(resp.Hrefs) == 0 {
		return err
	}

	hrefErrs := make([]error, len(resp.Hrefs))
	for i, href := range resp.Hrefs {
		hrefErrs[i] = &HrefError{Href: url.URL(href), Err: err}
	}
	return errors.Join(hrefErrs...)
}

// Path returns the path of the resource the response is about, along with
// the response's error, if any: the path of a relative href, or the whole URL
// of an absolute one.
//
// The hrefs of a response received by a Client are first normalized to the
// form returned by clients, as by Client.HrefPath.
func (resp *Response) Path() (string, error) {
	err := resp.Err()
	var path string
	if len(resp.Hrefs) == 1 {
		path = resp.Hrefs[0].pathOrURL()
	} else if err == nil {
		err = fmt.Errorf("webdav: malformed response: expected exactly one href element, got %v", len(resp.Hrefs))
	}
	return path, err
}

// HrefPath returns the path of the resource at href, found in one of the
// response's properties, in the same form as Path.
func (resp *Response) HrefPath(href *Href) string {
	h := *href
	if resp.client != nil {
		h = resp.client.normalizeHref(resp.base, h)
	}
	return h.pathOrURL()
}

func (resp *Response) DecodeProp(values ...interface{}) error {
	for _, v := range values {
		// TODO wrap errors with more context (XML name)
		name, err := valueXMLName(v)
		if err != nil {
			return err
		}
		if err := resp.Err(); err != nil {
			return newPropError(name, err)
		}
		for _, propstat := range resp.PropStats {
			raw := propstat.Prop.Get(name)
			if raw == nil {
				continue
			}
			if err := propstat.Status.Err(); err != nil {
				return newPropError(name, err)
			}
			if err := raw.Decode(v); err != nil {
				return newPropError(name, err)
			}
			return nil
		}
		return newPropError(name, &HTTPError{
			Code: http.StatusNotFound,
			Err:  fmt.Errorf("missing property"),
		})
	}

	return nil
}

func newPropError(name xml.Name, err error) error {
	return fmt.Errorf("property <%v %v>: %w", name.Space, name.Local, err)
}

func (resp *Response) EncodeProp(code int, v interface{}) error {
	raw, err := EncodeRawXMLElement(v)
	if err != nil {
		return err
	}

	for i := range resp.PropStats {
		propstat := &resp.PropStats[i]
		if propstat.Status.Code == code {
			propstat.Prop.Raw = append(propstat.Prop.Raw, *raw)
			return nil
		}
	}

	resp.PropStats = append(resp.PropStats, PropStat{
		Status: Status{Code: code},
		Prop:   Prop{Raw: []RawXMLValue{*raw}},
	})
	return nil
}

// https://tools.ietf.org/html/rfc4918#section-14.9
type Location struct {
	XMLName xml.Name `xml:"DAV: location"`
	Href    Href     `xml:"href"`
}

// https://tools.ietf.org/html/rfc4918#section-14.22
//
// Fields are in the order required by the RFC.
type PropStat struct {
	XMLName             xml.Name `xml:"DAV: propstat"`
	Prop                Prop     `xml:"prop"`
	Status              Status   `xml:"status"`
	Error               *Error   `xml:"error,omitempty"`
	ResponseDescription string   `xml:"responsedescription,omitempty"`
}

// https://tools.ietf.org/html/rfc4918#section-14.18
type Prop struct {
	XMLName xml.Name      `xml:"DAV: prop"`
	Raw     []RawXMLValue `xml:",any"`
}

func EncodeProp(values ...interface{}) (*Prop, error) {
	l := make([]RawXMLValue, len(values))
	for i, v := range values {
		raw, err := EncodeRawXMLElement(v)
		if err != nil {
			return nil, err
		}
		l[i] = *raw
	}
	return &Prop{Raw: l}, nil
}

func (p *Prop) Get(name xml.Name) *RawXMLValue {
	for i := range p.Raw {
		raw := &p.Raw[i]
		if n, ok := raw.XMLName(); ok && name == n {
			return raw
		}
	}
	return nil
}

func (p *Prop) Decode(v interface{}) error {
	name, err := valueXMLName(v)
	if err != nil {
		return err
	}

	raw := p.Get(name)
	if raw == nil {
		return HTTPErrorf(http.StatusNotFound, "missing property %s", name)
	}

	return raw.Decode(v)
}

// https://tools.ietf.org/html/rfc4918#section-14.20
type PropFind struct {
	XMLName  xml.Name  `xml:"DAV: propfind"`
	Prop     *Prop     `xml:"prop,omitempty"`
	AllProp  *struct{} `xml:"allprop,omitempty"`
	Include  *Include  `xml:"include,omitempty"`
	PropName *struct{} `xml:"propname,omitempty"`
}

func xmlNamesToRaw(names []xml.Name) []RawXMLValue {
	l := make([]RawXMLValue, len(names))
	for i, name := range names {
		l[i] = *NewRawXMLElement(name, nil, nil)
	}
	return l
}

func NewPropNamePropFind(names ...xml.Name) *PropFind {
	return &PropFind{Prop: &Prop{Raw: xmlNamesToRaw(names)}}
}

// https://tools.ietf.org/html/rfc4918#section-14.8
type Include struct {
	XMLName xml.Name      `xml:"DAV: include"`
	Raw     []RawXMLValue `xml:",any"`
}

// https://tools.ietf.org/html/rfc4918#section-15.9
type ResourceType struct {
	XMLName xml.Name      `xml:"DAV: resourcetype"`
	Raw     []RawXMLValue `xml:",any"`
}

func NewResourceType(names ...xml.Name) *ResourceType {
	return &ResourceType{Raw: xmlNamesToRaw(names)}
}

func (t *ResourceType) Is(name xml.Name) bool {
	for _, raw := range t.Raw {
		if n, ok := raw.XMLName(); ok && name == n {
			return true
		}
	}
	return false
}

// https://tools.ietf.org/html/rfc4918#section-15.4
type GetContentLength struct {
	XMLName xml.Name `xml:"DAV: getcontentlength"`
	Length  int64    `xml:",chardata"`
}

// https://tools.ietf.org/html/rfc4918#section-15.5
type GetContentType struct {
	XMLName xml.Name `xml:"DAV: getcontenttype"`
	Type    string   `xml:",chardata"`
}

type Time time.Time

func (t *Time) UnmarshalText(b []byte) error {
	tt, err := http.ParseTime(string(b))
	if err != nil {
		return err
	}
	*t = Time(tt)
	return nil
}

func (t *Time) MarshalText() ([]byte, error) {
	s := time.Time(*t).UTC().Format(http.TimeFormat)
	return []byte(s), nil
}

// https://tools.ietf.org/html/rfc4918#section-15.7
type GetLastModified struct {
	XMLName      xml.Name `xml:"DAV: getlastmodified"`
	LastModified Time     `xml:",chardata"`
}

// https://tools.ietf.org/html/rfc4918#section-15.6
type GetETag struct {
	XMLName xml.Name `xml:"DAV: getetag"`
	ETag    ETag     `xml:",chardata"`
}

// ETag is an entity-tag (RFC 7232 section 2.3).
//
// A strong entity-tag is held as its opaque-tag without the surrounding double
// quotes. A weak one is held whole, as W/ followed by the quoted opaque-tag:
// an opaque-tag cannot contain a double quote, so the two forms never collide.
//
// An opaque-tag is a quoted string without escapes: a backslash is an ordinary
// character in it.
type ETag string

func (etag *ETag) UnmarshalText(b []byte) error {
	s := strings.TrimSpace(string(b))
	if opaque, ok := strings.CutPrefix(s, "W/"); ok && strings.HasPrefix(opaque, `"`) {
		if len(opaque) < 2 || !strings.HasSuffix(opaque, `"`) || strings.Contains(opaque[1:len(opaque)-1], `"`) {
			return fmt.Errorf("webdav: malformed ETag %q", b)
		}
		*etag = ETag(s)
		return nil
	}
	if len(s) >= 2 && s[0] == '"' && s[len(s)-1] == '"' {
		s = s[1 : len(s)-1]
	}
	// A value without the surrounding double quotes is out of spec, but some
	// servers send it so and it is still usable as an opaque-tag.
	if strings.Contains(s, `"`) {
		return fmt.Errorf("webdav: malformed ETag %q", b)
	}
	*etag = ETag(s)
	return nil
}

// IsWeak reports whether the entity-tag is weak.
func (etag ETag) IsWeak() bool {
	return strings.HasPrefix(string(etag), `W/"`)
}

func (etag ETag) MarshalText() ([]byte, error) {
	return []byte(etag.String()), nil
}

func (etag ETag) String() string {
	if etag.IsWeak() {
		return string(etag)
	}
	return `"` + string(etag) + `"`
}

// https://tools.ietf.org/html/rfc4918#section-14.5
type Error struct {
	XMLName xml.Name      `xml:"DAV: error"`
	Raw     []RawXMLValue `xml:",any"`
}

func (err *Error) Error() string {
	b, _ := xml.Marshal(err)
	return string(b)
}

// https://tools.ietf.org/html/rfc4918#section-15.2
type DisplayName struct {
	XMLName xml.Name `xml:"DAV: displayname"`
	Name    string   `xml:",chardata"`
}

// https://tools.ietf.org/html/rfc5397#section-3
type CurrentUserPrincipal struct {
	XMLName         xml.Name  `xml:"DAV: current-user-principal"`
	Href            Href      `xml:"href,omitempty"`
	Unauthenticated *struct{} `xml:"unauthenticated,omitempty"`
}

// https://tools.ietf.org/html/rfc4918#section-14.19
type PropertyUpdate struct {
	XMLName xml.Name `xml:"DAV: propertyupdate"`
	Remove  []Remove `xml:"remove"`
	Set     []Set    `xml:"set"`

	// Instructions lists the remove and set instructions in document order,
	// which RFC 4918 section 9.2 requires servers to process them in.
	// Decoding fills it along with Remove and Set.
	Instructions []PropertyUpdateInstruction `xml:"-"`
}

// PropertyUpdateInstruction is a remove or set instruction of a
// PropertyUpdate.
type PropertyUpdateInstruction struct {
	Remove bool
	Prop   Prop
}

// UnmarshalXML implements xml.Unmarshaler.
func (pu *PropertyUpdate) UnmarshalXML(d *xml.Decoder, start xml.StartElement) error {
	if start.Name != (xml.Name{Namespace, "propertyupdate"}) {
		return fmt.Errorf("webdav: expected propertyupdate element, got %q %q", start.Name.Space, start.Name.Local)
	}
	*pu = PropertyUpdate{XMLName: start.Name}

	for {
		tok, err := d.Token()
		if err != nil {
			return err
		}
		switch tok := tok.(type) {
		case xml.StartElement:
			// Match the local name only, as the struct tags do: decoding into
			// Remove or Set rejects another namespace
			switch tok.Name.Local {
			case "remove":
				var remove Remove
				if err := d.DecodeElement(&remove, &tok); err != nil {
					return err
				}
				pu.Remove = append(pu.Remove, remove)
				pu.Instructions = append(pu.Instructions, PropertyUpdateInstruction{Remove: true, Prop: remove.Prop})
			case "set":
				var set Set
				if err := d.DecodeElement(&set, &tok); err != nil {
					return err
				}
				pu.Set = append(pu.Set, set)
				pu.Instructions = append(pu.Instructions, PropertyUpdateInstruction{Prop: set.Prop})
			default:
				if err := d.Skip(); err != nil {
					return err
				}
			}
		case xml.EndElement:
			return nil
		}
	}
}

// MarshalXML implements xml.Marshaler. If Instructions is set, the
// instructions are encoded in its order, and Remove and Set are ignored.
func (pu *PropertyUpdate) MarshalXML(e *xml.Encoder, start xml.StartElement) error {
	if len(pu.Instructions) == 0 {
		type propertyUpdate PropertyUpdate // without the methods
		return e.Encode((*propertyUpdate)(pu))
	}

	start = xml.StartElement{Name: xml.Name{Namespace, "propertyupdate"}}
	if err := e.EncodeToken(start); err != nil {
		return err
	}
	for _, ins := range pu.Instructions {
		var v interface{} = &Set{Prop: ins.Prop}
		if ins.Remove {
			v = &Remove{Prop: ins.Prop}
		}
		if err := e.Encode(v); err != nil {
			return err
		}
	}
	return e.EncodeToken(start.End())
}

// https://tools.ietf.org/html/rfc4918#section-14.23
type Remove struct {
	XMLName xml.Name `xml:"DAV: remove"`
	Prop    Prop     `xml:"prop"`
}

// https://tools.ietf.org/html/rfc4918#section-14.26
type Set struct {
	XMLName xml.Name `xml:"DAV: set"`
	Prop    Prop     `xml:"prop"`
}

// https://tools.ietf.org/html/rfc6578#section-6.1
type SyncCollectionQuery struct {
	XMLName   xml.Name `xml:"DAV: sync-collection"`
	SyncToken string   `xml:"sync-token"`
	Limit     *Limit   `xml:"limit,omitempty"`
	SyncLevel string   `xml:"sync-level"`
	Prop      *Prop    `xml:"prop"`
}

// https://tools.ietf.org/html/rfc5323#section-5.17
type Limit struct {
	XMLName  xml.Name `xml:"DAV: limit"`
	NResults uint     `xml:"nresults"`
}

// https://tools.ietf.org/html/rfc3744#section-5.4
type CurrentUserPrivilegeSet struct {
	XMLName   xml.Name `xml:"DAV: current-user-privilege-set"`
	Privilege []Privilege
}

// https://tools.ietf.org/html/rfc3744#section-5.4
type Privilege struct {
	XMLName xml.Name  `xml:"DAV: privilege"`
	Read    *struct{} `xml:"DAV: read,omitempty"`
	Write   *struct{} `xml:"DAV: write,omitempty"`

	// The privileges of CalDAV scheduling Inbox collections, see RFC 6638
	// section 6.1. ScheduleDeliver aggregates the three others.
	ScheduleDeliver       *struct{} `xml:"urn:ietf:params:xml:ns:caldav schedule-deliver,omitempty"`
	ScheduleDeliverInvite *struct{} `xml:"urn:ietf:params:xml:ns:caldav schedule-deliver-invite,omitempty"`
	ScheduleDeliverReply  *struct{} `xml:"urn:ietf:params:xml:ns:caldav schedule-deliver-reply,omitempty"`
	ScheduleQueryFreeBusy *struct{} `xml:"urn:ietf:params:xml:ns:caldav schedule-query-freebusy,omitempty"`
}

// NewCurrentUserPrivilegeSet returns a privilege set granting DAV:read, plus
// DAV:write unless readOnly is set.
func NewCurrentUserPrivilegeSet(readOnly bool) *CurrentUserPrivilegeSet {
	priv := Privilege{Read: &struct{}{}}
	if !readOnly {
		priv.Write = &struct{}{}
	}
	return &CurrentUserPrivilegeSet{Privilege: []Privilege{priv}}
}

// CalendarServerNamespace is the namespace of the CalendarServer extensions.
const CalendarServerNamespace = "http://calendarserver.org/ns/"

var GetCTagName = xml.Name{CalendarServerNamespace, "getctag"}

// GetCTag is the CalendarServer getctag property: an opaque token that changes
// whenever the collection's contents change, letting clients detect changes
// without enumerating every member's ETag. Despite the "calendarserver"
// namespace, the same property and namespace are used for CardDAV address
// books, not only CalDAV calendars.
type GetCTag struct {
	XMLName xml.Name `xml:"http://calendarserver.org/ns/ getctag"`
	CTag    string   `xml:",chardata"`
}
