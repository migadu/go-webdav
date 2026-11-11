package caldav

import (
	"bytes"
	"context"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/emersion/go-ical"
	"github.com/emersion/go-webdav"
	"github.com/emersion/go-webdav/internal"
)

var calendarProps = []xml.Name{
	internal.ResourceTypeName,
	internal.DisplayNameName,
	calendarDescriptionName,
	calendarColorName,
	calendarTimezoneName,
	maxResourceSizeName,
	supportedCalendarComponentSetName,
	internal.SyncTokenName,
	internal.GetCTagName,
	internal.CurrentUserPrivilegeSetName,
}

// ErrNoServiceRecord is returned by DiscoverContextURL when the domain has no
// SRV record for the service. RFC 6764 section 6 leaves it to the client to ask
// the user for the server, or to guess one, e.g. the domain itself with
// "https://" + domain + "/.well-known/caldav".
var ErrNoServiceRecord = internal.ErrNoServiceRecord

// DiscoverContextURL performs a DNS-based CalDAV service discovery as
// described in RFC 6764. It returns the URL to the CalDAV server.
func DiscoverContextURL(ctx context.Context, domain string) (string, error) {
	return internal.DiscoverContextURL(ctx, "caldav", domain)
}

// Client provides access to a remote CardDAV server.
type Client struct {
	*webdav.Client

	ic *internal.Client
}

// NewClient creates a new CalDAV client.
//
// The paths the client returns and takes are as described for
// webdav.NewClient: a calendar home set, calendar or calendar object on
// another host than the endpoint's, as with iCloud, is returned as an absolute
// URL.
func NewClient(c webdav.HTTPClient, endpoint string) (*Client, error) {
	wc, err := webdav.NewClient(c, endpoint)
	if err != nil {
		return nil, err
	}
	ic, err := internal.NewClient(c, endpoint)
	if err != nil {
		return nil, err
	}
	return &Client{wc, ic}, nil
}

// Get calendar information without loading all the events once the path of the calendar is known.
func (c *Client) FindCalendar(ctx context.Context, path string) (*Calendar, error) {
	propfind := internal.NewPropNamePropFind(calendarProps...)
	resp, err := c.ic.PropFindFlat(ctx, path, propfind)
	if err != nil {
		return nil, err
	}
	p, err := resp.Path()
	if err != nil {
		return nil, err
	}

	var resType internal.ResourceType
	if err := resp.DecodeProp(&resType); err != nil {
		return nil, err
	}
	if !resType.Is(calendarName) {
		return nil, fmt.Errorf("caldav: resource at %q is not a calendar", p)
	}

	return decodeCalendar(p, resp)
}

func (c *Client) FindCalendarHomeSet(ctx context.Context, principal string) (string, error) {
	propfind := internal.NewPropNamePropFind(calendarHomeSetName)
	resp, err := c.ic.PropFindFlat(ctx, principal, propfind)
	if err != nil {
		return "", err
	}

	var prop calendarHomeSet
	if err := resp.DecodeProp(&prop); err != nil {
		return "", err
	}

	return resp.HrefPath(&prop.Href), nil
}

func (c *Client) FindCalendars(ctx context.Context, calendarHomeSet string) ([]Calendar, error) {
	propfind := internal.NewPropNamePropFind(calendarProps...)
	ms, err := c.ic.PropFind(ctx, calendarHomeSet, internal.DepthOne, propfind)
	if err != nil {
		return nil, err
	}

	l := make([]Calendar, 0, len(ms.Responses))
	errs := make([]error, 0, len(ms.Responses))
	for _, resp := range ms.Responses {
		path, err := resp.Path()
		if err != nil {
			errs = append(errs, err)
			continue
		}

		var resType internal.ResourceType
		if err := resp.DecodeProp(&resType); err != nil {
			return nil, err
		}
		if !resType.Is(calendarName) {
			continue
		}

		calendar, err := decodeCalendar(path, &resp)
		if err != nil {
			return nil, err
		}

		l = append(l, *calendar)
	}

	return l, errors.Join(errs...)
}

func decodeCalendar(path string, resp *internal.Response) (*Calendar, error) {
	var desc calendarDescription
	if err := resp.DecodeProp(&desc); err != nil && !internal.IsNotFound(err) {
		return nil, err
	}

	var dispName internal.DisplayName
	if err := resp.DecodeProp(&dispName); err != nil && !internal.IsNotFound(err) {
		return nil, err
	}

	var maxResSize maxResourceSize
	if err := resp.DecodeProp(&maxResSize); err != nil && !internal.IsNotFound(err) {
		return nil, err
	}
	if maxResSize.Size < 0 {
		return nil, fmt.Errorf("caldav: max-resource-size must be a positive integer")
	}

	var supportedCompSet supportedCalendarComponentSet
	if err := resp.DecodeProp(&supportedCompSet); err != nil && !internal.IsNotFound(err) {
		return nil, err
	}

	compNames := make([]string, 0, len(supportedCompSet.Comp))
	for _, comp := range supportedCompSet.Comp {
		compNames = append(compNames, comp.Name)
	}

	var color calendarColor
	if err := resp.DecodeProp(&color); err != nil && !internal.IsNotFound(err) {
		return nil, err
	}

	var tz *ical.Calendar
	var calTZ calendarTimezone
	if err := resp.DecodeProp(&calTZ); err == nil {
		if s := strings.TrimSpace(calTZ.Timezone); s != "" {
			decoded, err := ical.NewDecoder(strings.NewReader(s)).Decode()
			if err != nil {
				return nil, fmt.Errorf("caldav: failed to decode calendar-timezone: %w", err)
			}
			tz = decoded
		}
	} else if !internal.IsNotFound(err) {
		return nil, err
	}

	var syncToken internal.SyncToken
	if err := resp.DecodeProp(&syncToken); err != nil && !internal.IsNotFound(err) {
		return nil, err
	}

	var ctag internal.GetCTag
	if err := resp.DecodeProp(&ctag); err != nil && !internal.IsNotFound(err) {
		return nil, err
	}

	var readOnly bool
	var privSet internal.CurrentUserPrivilegeSet
	if err := resp.DecodeProp(&privSet); err == nil {
		readOnly = !privSet.HasWrite()
	} else if !internal.IsNotFound(err) {
		return nil, err
	}

	return &Calendar{
		Path:                  path,
		Name:                  dispName.Name,
		Description:           desc.Description,
		Color:                 strings.TrimSpace(color.Color),
		Timezone:              tz,
		MaxResourceSize:       maxResSize.Size,
		SyncToken:             syncToken.Token,
		SupportedComponentSet: compNames,
		ReadOnly:              readOnly,
		CTag:                  ctag.CTag,
	}, nil
}

// isEmpty reports whether the request asks for nothing in particular.
func (c *CalendarCompRequest) isEmpty() bool {
	return c == nil || (c.Name == "" && !c.AllProps && !c.AllComps && len(c.Props) == 0 && len(c.Comps) == 0)
}

func encodeCalendarCompReq(c *CalendarCompRequest) (*comp, error) {
	if c.isEmpty() {
		return nil, nil
	}
	if c.Name == "" {
		return nil, fmt.Errorf("caldav: missing component name in calendar data request")
	}

	encoded := comp{Name: c.Name}

	if c.AllProps {
		encoded.Allprop = &struct{}{}
	}
	for _, name := range c.Props {
		encoded.Prop = append(encoded.Prop, prop{Name: name})
	}

	if c.AllComps {
		encoded.Allcomp = &struct{}{}
	}
	for _, child := range c.Comps {
		encodedChild, err := encodeCalendarCompReq(&child)
		if err != nil {
			return nil, err
		} else if encodedChild == nil {
			// An empty request has nothing to ask for
			continue
		}
		encoded.Comp = append(encoded.Comp, *encodedChild)
	}

	return &encoded, nil
}

func encodeCalendarReq(c *CalendarCompRequest) (*internal.Prop, error) {
	if !c.isEmpty() && c.Name == "" {
		// The outermost component is the calendar itself (RFC 4791 section
		// 9.6.1), and the name attribute is required
		withName := *c
		withName.Name = ical.CompCalendar
		c = &withName
	}
	compReq, err := encodeCalendarCompReq(c)
	if err != nil {
		return nil, err
	}

	expandReq := encodeExpandRequest(c.Expand)

	calDataReq := calendarDataReq{Comp: compReq, Expand: expandReq}

	getLastModReq := internal.NewRawXMLElement(internal.GetLastModifiedName, nil, nil)
	getETagReq := internal.NewRawXMLElement(internal.GetETagName, nil, nil)
	return internal.EncodeProp(&calDataReq, getLastModReq, getETagReq)
}

func encodeCompFilter(filter *CompFilter) *compFilter {
	encoded := compFilter{Name: filter.Name}
	if filter.IsNotDefined {
		// is-not-defined can't have siblings, see RFC 4791 section 9.7.1
		encoded.IsNotDefined = &struct{}{}
		return &encoded
	}
	if !filter.Start.IsZero() || !filter.End.IsZero() {
		encoded.TimeRange = newTimeRange(filter.Start, filter.End)
	}
	for _, child := range filter.Comps {
		encoded.CompFilters = append(encoded.CompFilters, *encodeCompFilter(&child))
	}
	for _, pf := range filter.Props {
		encoded.PropFilters = append(encoded.PropFilters, *encodePropFilter(&pf))
	}
	return &encoded
}

func encodePropFilter(filter *PropFilter) *propFilter {
	encoded := propFilter{Name: filter.Name}
	if filter.IsNotDefined {
		// is-not-defined can't have siblings, see RFC 4791 section 9.7.2
		encoded.IsNotDefined = &struct{}{}
		return &encoded
	}
	if !filter.Start.IsZero() || !filter.End.IsZero() {
		encoded.TimeRange = newTimeRange(filter.Start, filter.End)
	}
	encoded.TextMatch = encodeTextMatch(filter.TextMatch)
	for _, pf := range filter.ParamFilter {
		encoded.ParamFilter = append(encoded.ParamFilter, encodeParamFilter(pf))
	}
	return &encoded
}

func encodeParamFilter(pf ParamFilter) paramFilter {
	encoded := paramFilter{Name: pf.Name}
	if pf.IsNotDefined {
		// is-not-defined can't have siblings, see RFC 4791 section 9.7.3
		encoded.IsNotDefined = &struct{}{}
		return encoded
	}
	encoded.TextMatch = encodeTextMatch(pf.TextMatch)
	return encoded
}

func encodeTextMatch(tm *TextMatch) *textMatch {
	if tm == nil {
		return nil
	}

	encoded := &textMatch{
		Text:            tm.Text,
		NegateCondition: negateCondition(tm.NegateCondition),
	}
	return encoded
}

func encodeExpandRequest(e *CalendarExpandRequest) *expand {
	if e == nil {
		return nil
	}
	encoded := expand{
		Start: dateWithUTCTime(e.Start),
		End:   dateWithUTCTime(e.End),
	}
	return &encoded
}

func decodeCalendarObjectList(ms *internal.MultiStatus) ([]CalendarObject, error) {
	addrs := make([]CalendarObject, 0, len(ms.Responses))
	errs := make([]error, 0, len(ms.Responses))
	for _, resp := range ms.Responses {
		path, err := resp.Path()
		if err != nil {
			errs = append(errs, err)
			continue
		}

		var calData calendarDataResp
		if err := resp.DecodeProp(&calData); err != nil {
			return nil, err
		}

		var getLastMod internal.GetLastModified
		if err := resp.DecodeProp(&getLastMod); err != nil && !internal.IsNotFound(err) {
			return nil, err
		}

		var getETag internal.GetETag
		if err := resp.DecodeProp(&getETag); err != nil && !internal.IsNotFound(err) {
			return nil, err
		}

		var getContentLength internal.GetContentLength
		if err := resp.DecodeProp(&getContentLength); err != nil && !internal.IsNotFound(err) {
			return nil, err
		}

		r := bytes.NewReader(calData.Data)
		data, err := ical.NewDecoder(r).Decode()
		if err != nil {
			return nil, err
		}

		addrs = append(addrs, CalendarObject{
			Path:          path,
			ModTime:       time.Time(getLastMod.LastModified),
			ContentLength: getContentLength.Length,
			ETag:          string(getETag.ETag),
			Data:          data,
		})
	}

	return addrs, errors.Join(errs...)
}

func (c *Client) QueryCalendar(ctx context.Context, calendar string, query *CalendarQuery) ([]CalendarObject, error) {
	propReq, err := encodeCalendarReq(&query.CompRequest)
	if err != nil {
		return nil, err
	}

	calendarQuery := calendarQuery{Prop: propReq}
	calendarQuery.Filter.CompFilter = *encodeCompFilter(&query.CompFilter)
	req, err := c.ic.NewXMLRequest("REPORT", calendar, &calendarQuery)
	if err != nil {
		return nil, err
	}
	req.Header.Add("Depth", "1")

	ms, err := c.ic.DoMultiStatus(req.WithContext(ctx))
	if err != nil {
		return nil, err
	}

	return decodeCalendarObjectList(ms)
}

func (c *Client) MultiGetCalendar(ctx context.Context, path string, multiGet *CalendarMultiGet) ([]CalendarObject, error) {
	propReq, err := encodeCalendarReq(&multiGet.CompRequest)
	if err != nil {
		return nil, err
	}

	paths := multiGet.Paths
	if len(paths) == 0 {
		paths = []string{path}
	}
	hrefs, err := c.ic.RequestHrefs(path, paths)
	if err != nil {
		return nil, err
	}

	calendarMultiget := calendarMultiget{Prop: propReq, Hrefs: hrefs}

	req, err := c.ic.NewXMLRequest("REPORT", path, &calendarMultiget)
	if err != nil {
		return nil, err
	}
	req.Header.Add("Depth", "1")

	ms, err := c.ic.DoMultiStatus(req.WithContext(ctx))
	if err != nil {
		return nil, err
	}

	return decodeCalendarObjectList(ms)
}

func (c *Client) populateCalendarObject(co *CalendarObject, resp *http.Response) error {
	h := resp.Header
	if loc := h.Get("Location"); loc != "" {
		u, err := url.Parse(loc)
		if err != nil {
			return err
		}
		var base *url.URL
		if resp.Request != nil {
			base = resp.Request.URL
		}
		co.Path = c.ic.HrefPath(base, u)
	}
	if s := h.Get("ETag"); s != "" {
		var etag internal.ETag
		if err := etag.UnmarshalText([]byte(s)); err != nil {
			return err
		}
		co.ETag = string(etag)
	}
	if contentLength := h.Get("Content-Length"); contentLength != "" {
		n, err := strconv.ParseInt(contentLength, 10, 64)
		if err != nil {
			return err
		}
		co.ContentLength = n
	}
	if lastModified := h.Get("Last-Modified"); lastModified != "" {
		t, err := http.ParseTime(lastModified)
		if err != nil {
			return err
		}
		co.ModTime = t
	}

	return nil
}

// isICalendarMediaType reports whether mediaType, as returned by
// mime.ParseMediaType, is acceptable for iCalendar data sent in a response.
// Besides text/calendar, this accepts text/plain or no Content-Type at all,
// which some servers such as iCloud send. A body which isn't iCalendar data is
// still rejected when it is decoded.
func isICalendarMediaType(mediaType string) bool {
	switch mediaType {
	case ical.MIMEType, "text/plain", "":
		return true
	default:
		return false
	}
}

func (c *Client) GetCalendarObject(ctx context.Context, path string) (*CalendarObject, error) {
	req, err := c.ic.NewRequest(http.MethodGet, path, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", ical.MIMEType)

	resp, err := c.ic.Do(req.WithContext(ctx))
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	var mediaType string
	if h := resp.Header.Get("Content-Type"); h != "" {
		mediaType, _, err = mime.ParseMediaType(h)
		if err != nil {
			return nil, err
		}
	}
	if !isICalendarMediaType(mediaType) {
		return nil, fmt.Errorf("caldav: expected Content-Type %q, got %q", ical.MIMEType, mediaType)
	}

	cal, err := ical.NewDecoder(resp.Body).Decode()
	if err == io.EOF {
		// The body was empty or ended within a component
		err = io.ErrUnexpectedEOF
	}
	if err != nil {
		return nil, fmt.Errorf("caldav: failed to decode iCalendar data in response with Content-Type %q: %w", mediaType, err)
	}

	co := &CalendarObject{
		Path: c.ic.HrefPath(nil, resp.Request.URL),
		Data: cal,
	}
	if err := c.populateCalendarObject(co, resp); err != nil {
		return nil, err
	}
	return co, nil
}

// PutCalendarObject stores cal as the calendar object resource at path. A nil
// opts performs an unconditional PUT; a non-nil opts may carry an If-Match
// (overwrite only if the resource still has this ETag) or If-None-Match
// (create only if absent) precondition. A failed precondition surfaces as an
// *webdav.HTTPError with code 412 Precondition Failed.
//
// The values of opts are sent as is, so they must be in header form: a quoted
// strong entity-tag such as "abc", a weak one such as W/"abc", a
// comma-separated list of these, or "*". If-Match uses the strong comparison,
// so a weak entity-tag never matches it.
func (c *Client) PutCalendarObject(ctx context.Context, path string, cal *ical.Calendar, opts *PutCalendarObjectOptions) (*CalendarObject, error) {
	// TODO: some servers want a Content-Length header, so we can't stream the
	// request body here. See the Radicale issue:
	// https://github.com/Kozea/Radicale/issues/1016

	var buf bytes.Buffer
	if err := ical.NewEncoder(&buf).Encode(cal); err != nil {
		return nil, err
	}

	req, err := c.ic.NewRequest(http.MethodPut, path, &buf)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", ical.MIMEType)
	if opts != nil {
		if opts.IfMatch.IsSet() {
			req.Header.Set("If-Match", string(opts.IfMatch))
		}
		if opts.IfNoneMatch.IsSet() {
			req.Header.Set("If-None-Match", string(opts.IfNoneMatch))
		}
	}

	resp, err := c.ic.Do(req.WithContext(ctx))
	if err != nil {
		return nil, err
	}
	resp.Body.Close()

	co := &CalendarObject{Path: path}
	if err := c.populateCalendarObject(co, resp); err != nil {
		return nil, err
	}
	return co, nil
}

// SyncCollection performs a collection synchronization operation on the
// specified resource, as defined in RFC 6578.
func (c *Client) SyncCollection(ctx context.Context, path string, query *SyncQuery) (*SyncResponse, error) {
	var limit *internal.Limit
	if query.Limit > 0 {
		limit = &internal.Limit{NResults: uint(query.Limit)}
	}

	compReq, err := encodeCalendarReq(&query.CompRequest)
	if err != nil {
		return nil, err
	}

	ms, err := c.ic.SyncCollection(ctx, path, query.SyncToken, internal.DepthOne, limit, compReq)
	if err != nil {
		return nil, err
	}

	ret := &SyncResponse{SyncToken: ms.SyncToken}
	var errs []error
	for _, resp := range ms.Responses {
		p, err := resp.Path()
		if err != nil {
			var httpErr *internal.HTTPError
			switch {
			case !errors.As(err, &httpErr):
				errs = append(errs, err)
			case httpErr.Code == http.StatusNotFound:
				ret.Deleted = append(ret.Deleted, p)
			case httpErr.Code == http.StatusInsufficientStorage:
				// The server reports a truncated result on the request-URI
				// (RFC 6578 section 3.6).
				ret.Truncated = true
			default:
				errs = append(errs, err)
			}
			continue
		}

		if p == path || path == fmt.Sprintf("%s/", p) {
			continue
		}

		var getLastMod internal.GetLastModified
		if err := resp.DecodeProp(&getLastMod); err != nil && !internal.IsNotFound(err) {
			return nil, err
		}

		var getETag internal.GetETag
		if err := resp.DecodeProp(&getETag); err != nil && !internal.IsNotFound(err) {
			return nil, err
		}

		o := CalendarObject{
			Path:    p,
			ModTime: time.Time(getLastMod.LastModified),
			ETag:    string(getETag.ETag),
		}

		// Many servers return the calendar-data inline in the sync-collection
		// response (the query already requests it via CompRequest); decode it so
		// the caller need not follow up with a multiget. When the server omits the
		// data, Data is left nil and the caller fetches it as before.
		var calData calendarDataResp
		if err := resp.DecodeProp(&calData); err == nil {
			cal, err := ical.NewDecoder(bytes.NewReader(calData.Data)).Decode()
			if err != nil {
				return nil, err
			}
			o.Data = cal
		} else if !internal.IsNotFound(err) {
			return nil, err
		}

		ret.Updated = append(ret.Updated, o)
	}

	return ret, errors.Join(errs...)

}
