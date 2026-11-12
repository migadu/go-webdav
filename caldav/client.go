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

// Client provides access to a remote CalDAV server.
//
// The webdav.Client it embeds performs the requests which aren't specific to
// CalDAV, such as removing a calendar, along with the calendar objects it
// holds, or a single calendar object with RemoveAll.
type Client struct {
	*webdav.Client

	ic *internal.Client
}

// NewClient creates a new CalDAV client.
//
// The paths the client returns and takes are as described for
// webdav.NewClient: a calendar home set, calendar or calendar object on
// another host than the endpoint's, as with iCloud, is returned as an absolute
// URL. The credentials of webdav.HTTPClientWithBasicAuth are only sent there
// by a Client with an endpoint on that host, e.g. the home set.
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

// FindCalendarHomeSet returns the path of the calendar home set of the
// principal at principal, the collection holding its calendars. If the
// principal has several, it returns the first one the server lists: see
// FindCalendarHomeSets.
//
// If the principal has no calendar home set, because the server doesn't
// define the CALDAV:calendar-home-set property for it or defines it without
// any href (RFC 4791 section 6.2.1), the returned error wraps a
// *webdav.HTTPError with the status 404 Not Found.
func (c *Client) FindCalendarHomeSet(ctx context.Context, principal string) (string, error) {
	homeSets, err := c.FindCalendarHomeSets(ctx, principal)
	if err != nil {
		return "", err
	}
	return homeSets[0], nil
}

// FindCalendarHomeSets returns the paths of the calendar home sets of the
// principal at principal, in the order the server lists them: the
// CALDAV:calendar-home-set property may hold several hrefs (RFC 4791 section
// 6.2.1).
//
// If the principal has no calendar home set, it returns the same error as
// FindCalendarHomeSet, so the list is never empty.
func (c *Client) FindCalendarHomeSets(ctx context.Context, principal string) ([]string, error) {
	propfind := internal.NewPropNamePropFind(calendarHomeSetName)
	resp, err := c.ic.PropFindFlat(ctx, principal, propfind)
	if err != nil {
		return nil, err
	}

	var prop calendarHomeSet
	if err := resp.DecodeProp(&prop); err != nil {
		return nil, err
	}
	if len(prop.Hrefs) == 0 {
		return nil, internal.NoHrefError(calendarHomeSetName)
	}

	homeSets := make([]string, len(prop.Hrefs))
	for i := range prop.Hrefs {
		homeSets[i] = resp.HrefPath(&prop.Hrefs[i])
	}
	return homeSets, nil
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

	cal := &Calendar{
		Path:                  path,
		Name:                  dispName.Name,
		Description:           desc.Description,
		MaxResourceSize:       maxResSize.Size,
		SupportedComponentSet: compNames,
	}

	// The other properties only fill in optional fields. Clients should be
	// prepared for any status for a property (RFC 4918 section 9.1.2), e.g.
	// 403 Forbidden for current-user-privilege-set, which needs its own
	// privilege (RFC 3744 section 3.7): if the server fails to return one,
	// or returns a value which can't be decoded, its field is left empty
	// rather than failing the whole request.
	var color calendarColor
	if resp.DecodeProp(&color) == nil {
		cal.Color = strings.TrimSpace(color.Color)
	}

	var calTZ calendarTimezone
	if resp.DecodeProp(&calTZ) == nil {
		if s := strings.TrimSpace(calTZ.Timezone); s != "" {
			if tz, err := ical.NewDecoder(strings.NewReader(s)).Decode(); err == nil {
				cal.Timezone = tz
			}
		}
	}

	var syncToken internal.SyncToken
	if resp.DecodeProp(&syncToken) == nil {
		cal.SyncToken = syncToken.Token
	}

	var ctag internal.GetCTag
	if resp.DecodeProp(&ctag) == nil {
		cal.CTag = ctag.CTag
	}

	var privSet internal.CurrentUserPrivilegeSet
	if resp.DecodeProp(&privSet) == nil {
		cal.ReadOnly = !privSet.HasWrite()
	}

	return cal, nil
}

// CreateCalendar creates a calendar at calendar.Path with a MKCALENDAR
// request (RFC 4791 section 5.3.1). The fields of calendar which aren't empty
// set its properties: Name, Description, Color, Timezone, which must hold a
// single VTIMEZONE component (RFC 4791 section 5.2.2), and
// SupportedComponentSet, which can't be changed afterwards (RFC 4791 section
// 5.2.3). The other fields are maintained by the server, and ignored:
// MaxResourceSize, for instance, is protected (RFC 4791 section 5.2.5).
//
// The server fails the request if there already is a resource at the path,
// generally with 405 Method Not Allowed, if the collection which would hold
// the calendar doesn't exist, with 409 Conflict, or if calendars can't be
// created there, with 403 Forbidden and the
// CALDAV:calendar-collection-location-ok precondition: the error is then a
// *webdav.HTTPError with that status. If the server refuses some of the
// properties, the error lists each of them with its status, as for
// UpdateCalendar.
//
// Servers needn't support MKCALENDAR: some only have one calendar per user,
// created along with the account (RFC 4791 section 5.3.1). Calendars are
// removed with RemoveAll.
func (c *Client) CreateCalendar(ctx context.Context, calendar *Calendar) error {
	if calendar.Path == "" {
		return fmt.Errorf("caldav: missing calendar path")
	}
	prop, err := encodeCalendarProps(calendar)
	if err != nil {
		return err
	}

	var req *http.Request
	if len(prop.Raw) == 0 {
		// The CALDAV:mkcalendar element holds a DAV:set element (RFC 4791
		// section 9.3): without properties to set, there's no request body
		req, err = c.ic.NewRequest("MKCALENDAR", calendar.Path, nil)
	} else {
		req, err = c.ic.NewXMLRequest("MKCALENDAR", calendar.Path, &mkcalendarSetReq{
			Set: internal.Set{Prop: *prop},
		})
	}
	if err != nil {
		return err
	}
	return c.ic.DoCreateCollection(req.WithContext(ctx))
}

// encodeCalendarProps returns the properties of cal which CreateCalendar sets,
// those which aren't empty.
func encodeCalendarProps(cal *Calendar) (*internal.Prop, error) {
	var values []interface{}
	if cal.Name != "" {
		values = append(values, &internal.DisplayName{Name: cal.Name})
	}
	if cal.Description != "" {
		values = append(values, &calendarDescription{Description: cal.Description})
	}
	if len(cal.SupportedComponentSet) > 0 {
		var set supportedCalendarComponentSet
		for _, name := range cal.SupportedComponentSet {
			set.Comp = append(set.Comp, comp{Name: name})
		}
		values = append(values, &set)
	}
	if cal.Timezone != nil {
		tz, err := encodeCalendarTimezone(cal.Timezone)
		if err != nil {
			return nil, err
		}
		values = append(values, tz)
	}
	if cal.Color != "" {
		values = append(values, &calendarColor{Color: cal.Color})
	}
	return internal.EncodeProp(values...)
}

func encodeCalendarTimezone(tz *ical.Calendar) (*calendarTimezone, error) {
	var buf bytes.Buffer
	if err := ical.NewEncoder(&buf).Encode(tz); err != nil {
		return nil, fmt.Errorf("caldav: failed to encode calendar timezone: %w", err)
	}
	return &calendarTimezone{Timezone: buf.String()}, nil
}

// UpdateCalendar changes the properties of the calendar at path with a
// PROPPATCH request (RFC 4918 section 9.2), which the server applies in full
// or not at all. The fields of update which are nil are left as they are; a
// field pointing to the empty string removes the property, as RemoveTimezone
// removes the timezone. An update without any change sends no request.
//
// If the server refuses the request, the returned error lists each property
// with its status: first those which failed, then those which got 424 Failed
// Dependency because of them, so that errors.As finds the *webdav.HTTPError
// of a property which failed. The server can also fail the request as a
// whole, for instance with 404 Not Found if there's no calendar at path: the
// error is then a *webdav.HTTPError with that status.
func (c *Client) UpdateCalendar(ctx context.Context, path string, update *CalendarUpdate) error {
	propUpdate, err := encodeCalendarUpdate(update)
	if err != nil || propUpdate == nil {
		return err
	}
	return c.ic.PropPatch(ctx, path, propUpdate)
}

// encodeCalendarUpdate returns the PROPPATCH request body for update, or nil
// if it changes nothing.
func encodeCalendarUpdate(update *CalendarUpdate) (*internal.PropertyUpdate, error) {
	if update == nil {
		return nil, nil
	}
	if update.Timezone != nil && update.RemoveTimezone {
		return nil, fmt.Errorf("caldav: calendar update both sets and removes the timezone")
	}

	var set []interface{}
	var remove []xml.Name
	if s := update.Name; s != nil {
		if *s == "" {
			remove = append(remove, internal.DisplayNameName)
		} else {
			set = append(set, &internal.DisplayName{Name: *s})
		}
	}
	if s := update.Description; s != nil {
		if *s == "" {
			remove = append(remove, calendarDescriptionName)
		} else {
			set = append(set, &calendarDescription{Description: *s})
		}
	}
	if s := update.Color; s != nil {
		if *s == "" {
			remove = append(remove, calendarColorName)
		} else {
			set = append(set, &calendarColor{Color: *s})
		}
	}
	if update.Timezone != nil {
		tz, err := encodeCalendarTimezone(update.Timezone)
		if err != nil {
			return nil, err
		}
		set = append(set, tz)
	} else if update.RemoveTimezone {
		remove = append(remove, calendarTimezoneName)
	}
	return internal.NewPropertyUpdate(set, remove)
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
	if c.Expand != nil && c.LimitRecurrenceSet != nil {
		// See RFC 4791 section 9.6
		return nil, fmt.Errorf("caldav: only one of Expand or LimitRecurrenceSet can be set in a calendar data request")
	}
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

	// The time ranges are checked as the server checks them, a zero time
	// being a missing attribute
	calDataReq := calendarDataReq{Comp: compReq}
	if e := c.Expand; e != nil {
		tr := newRequiredTimeRange(e.Start, e.End)
		if err := tr.check("expand"); err != nil {
			return nil, err
		}
		calDataReq.Expand = []expand{{requiredTimeRange: tr}}
	}
	if l := c.LimitRecurrenceSet; l != nil {
		tr := newRequiredTimeRange(l.Start, l.End)
		if err := tr.check("limit-recurrence-set"); err != nil {
			return nil, err
		}
		calDataReq.LimitRecurrenceSet = []limitRecurrenceSet{{requiredTimeRange: tr}}
	}
	if l := c.LimitFreeBusySet; l != nil {
		tr := newRequiredTimeRange(l.Start, l.End)
		if err := tr.check("limit-freebusy-set"); err != nil {
			return nil, err
		}
		calDataReq.LimitFreeBusySet = []limitFreeBusySet{{requiredTimeRange: tr}}
	}

	getLastModReq := internal.NewRawXMLElement(internal.GetLastModifiedName, nil, nil)
	getETagReq := internal.NewRawXMLElement(internal.GetETagName, nil, nil)
	getContentLengthReq := internal.NewRawXMLElement(internal.GetContentLengthName, nil, nil)
	return internal.EncodeProp(&calDataReq, getLastModReq, getETagReq, getContentLengthReq)
}

// encodeFilter returns the filter of a calendar-query whose comp-filter is
// cf. That comp-filter targets the calendar object (RFC 4791 section 9.7.1):
// without a name, it's VCALENDAR, and any other name is refused, as the
// server refuses it.
func encodeFilter(cf *CompFilter) (*filter, error) {
	if cf.Name == "" {
		withName := *cf
		withName.Name = ical.CompCalendar
		cf = &withName
	} else if !strings.EqualFold(cf.Name, ical.CompCalendar) {
		return nil, fmt.Errorf("caldav: the outer CompFilter must be %v, not %q", ical.CompCalendar, cf.Name)
	}
	encoded, err := encodeCompFilter(cf)
	if err != nil {
		return nil, err
	}
	return &filter{CompFilters: []compFilter{*encoded}}, nil
}

// encodeCompFilter returns the comp-filter element for filter. Like its
// prop-filter and param-filter elements, it's checked as the server checks
// it: RFC 4791 sections 9.7.1 to 9.7.3 require a name, and section 9.9 a
// time range whose end is after its start.
func encodeCompFilter(filter *CompFilter) (*compFilter, error) {
	if filter.Name == "" {
		return nil, fmt.Errorf("caldav: a CompFilter requires a Name")
	}
	encoded := compFilter{Name: filter.Name}
	if filter.IsNotDefined {
		// is-not-defined can't have siblings, see RFC 4791 section 9.7.1
		encoded.IsNotDefined = []struct{}{{}}
		return &encoded, nil
	}
	if !filter.Start.IsZero() || !filter.End.IsZero() {
		tr := newTimeRange(filter.Start, filter.End)
		if err := tr.check(); err != nil {
			return nil, err
		}
		encoded.TimeRange = []timeRange{tr}
	}
	for _, child := range filter.Comps {
		el, err := encodeCompFilter(&child)
		if err != nil {
			return nil, err
		}
		encoded.CompFilters = append(encoded.CompFilters, *el)
	}
	for _, pf := range filter.Props {
		el, err := encodePropFilter(&pf)
		if err != nil {
			return nil, err
		}
		encoded.PropFilters = append(encoded.PropFilters, *el)
	}
	return &encoded, nil
}

func encodePropFilter(filter *PropFilter) (*propFilter, error) {
	if filter.Name == "" {
		return nil, fmt.Errorf("caldav: a PropFilter requires a Name")
	}
	encoded := propFilter{Name: filter.Name}
	if filter.IsNotDefined {
		// is-not-defined can't have siblings, see RFC 4791 section 9.7.2
		encoded.IsNotDefined = []struct{}{{}}
		return &encoded, nil
	}
	if !filter.Start.IsZero() || !filter.End.IsZero() {
		// (time-range | text-match)?, see RFC 4791 section 9.7.2
		if filter.TextMatch != nil {
			return nil, fmt.Errorf("caldav: PropFilter %v can't have both a time range and a TextMatch", filter.Name)
		}
		tr := newTimeRange(filter.Start, filter.End)
		if err := tr.check(); err != nil {
			return nil, err
		}
		encoded.TimeRange = []timeRange{tr}
	}
	encoded.TextMatch = encodeTextMatch(filter.TextMatch)
	for _, pf := range filter.ParamFilter {
		el, err := encodeParamFilter(pf)
		if err != nil {
			return nil, err
		}
		encoded.ParamFilter = append(encoded.ParamFilter, el)
	}
	return &encoded, nil
}

func encodeParamFilter(pf ParamFilter) (paramFilter, error) {
	if pf.Name == "" {
		return paramFilter{}, fmt.Errorf("caldav: a ParamFilter requires a Name")
	}
	encoded := paramFilter{Name: pf.Name}
	if pf.IsNotDefined {
		// is-not-defined can't have siblings, see RFC 4791 section 9.7.3
		encoded.IsNotDefined = []struct{}{{}}
		return encoded, nil
	}
	encoded.TextMatch = encodeTextMatch(pf.TextMatch)
	return encoded, nil
}

// encodeTextMatch returns the text-match elements for tm: none if tm is nil.
func encodeTextMatch(tm *TextMatch) []textMatch {
	if tm == nil {
		return nil
	}
	return []textMatch{{
		Text:            tm.Text,
		Collation:       tm.Collation,
		NegateCondition: negateCondition(tm.NegateCondition),
	}}
}

// doCalendarObjectList performs req, a calendar-query or calendar-multiget
// REPORT, and decodes the calendar object of each response as soon as the
// response is received.
func (c *Client) doCalendarObjectList(req *http.Request) ([]CalendarObject, error) {
	addrs := []CalendarObject{}
	var errs []error
	_, err := c.ic.DoMultiStatusFunc(req, func(resp *internal.Response) error {
		path, err := resp.Path()
		if err != nil {
			errs = append(errs, err)
			return nil
		}

		var calData calendarDataResp
		if err := resp.DecodeProp(&calData); err != nil {
			return err
		}

		var getLastMod internal.GetLastModified
		if err := resp.DecodeProp(&getLastMod); err != nil && !internal.IsNotFound(err) {
			return err
		}

		var getETag internal.GetETag
		if err := resp.DecodeProp(&getETag); err != nil && !internal.IsNotFound(err) {
			return err
		}

		var getContentLength internal.GetContentLength
		if err := resp.DecodeProp(&getContentLength); err != nil && !internal.IsNotFound(err) {
			return err
		}

		r := bytes.NewReader(calData.Data)
		data, err := ical.NewDecoder(r).Decode()
		if err != nil {
			return err
		}

		addrs = append(addrs, CalendarObject{
			Path:          path,
			ModTime:       time.Time(getLastMod.LastModified),
			ContentLength: getContentLength.Length,
			ETag:          string(getETag.ETag),
			Data:          data,
		})
		return nil
	})
	if err != nil {
		return nil, err
	}

	return addrs, errors.Join(errs...)
}

func (c *Client) QueryCalendar(ctx context.Context, calendar string, query *CalendarQuery) ([]CalendarObject, error) {
	propReq, err := encodeCalendarReq(&query.CompRequest)
	if err != nil {
		return nil, err
	}

	filter, err := encodeFilter(&query.CompFilter)
	if err != nil {
		return nil, err
	}

	calendarQuery := calendarQuery{Prop: propReq, Filter: filter}
	req, err := c.ic.NewXMLRequest("REPORT", calendar, &calendarQuery)
	if err != nil {
		return nil, err
	}
	req.Header.Add("Depth", "1")

	return c.doCalendarObjectList(req.WithContext(ctx))
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

	return c.doCalendarObjectList(req.WithContext(ctx))
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
//
// Members the server couldn't report, such as changed calendar objects it
// failed to retrieve, are returned as an error made of HrefErrors, along
// with the SyncResponse holding the other changes. The new SyncToken covers
// their changes too, so a caller keeping it should fetch them otherwise, for
// instance with MultiGetCalendar.
func (c *Client) SyncCollection(ctx context.Context, path string, query *SyncQuery) (*SyncResponse, error) {
	var limit *internal.Limit
	if query.Limit > 0 {
		limit = &internal.Limit{NResults: uint(query.Limit)}
	}

	compReq, err := encodeCalendarReq(&query.CompRequest)
	if err != nil {
		return nil, err
	}

	u, err := c.ic.ResolveHref(path)
	if err != nil {
		return nil, err
	}
	collectionPath := c.ic.HrefPath(nil, u)

	ret := &SyncResponse{}
	var errs []error
	ms, err := c.ic.SyncCollection(ctx, path, query.SyncToken, internal.DepthOne, limit, compReq, func(resp *internal.Response) error {
		p, err := resp.Path()
		if err != nil {
			var httpErr *internal.HTTPError
			switch {
			case !errors.As(err, &httpErr):
				errs = append(errs, err)
			case httpErr.Code == http.StatusNotFound && len(resp.Hrefs) > 0:
				ret.Deleted = append(ret.Deleted, resp.Paths()...)
			case httpErr.Code == http.StatusInsufficientStorage &&
				(len(resp.Hrefs) == 0 || (len(resp.Hrefs) == 1 && internal.SamePath(p, collectionPath))):
				// The server reports a truncated result on the request-URI
				// (RFC 6578 section 3.6).
				ret.Truncated = true
			default:
				errs = append(errs, err)
			}
			return nil
		}

		if internal.SamePath(p, collectionPath) {
			return nil
		}

		// A changed member can't have an error status (RFC 6578 section
		// 3.2): the server reports one it failed to retrieve with failed
		// propstats instead. Report it as an error too, rather than as
		// updated without its data.
		if err := resp.PropStatErr(); err != nil {
			errs = append(errs, err)
			return nil
		}

		var getLastMod internal.GetLastModified
		if err := resp.DecodeProp(&getLastMod); err != nil && !internal.IsNotFound(err) {
			return err
		}

		var getETag internal.GetETag
		if err := resp.DecodeProp(&getETag); err != nil && !internal.IsNotFound(err) {
			return err
		}

		var getContentLength internal.GetContentLength
		if err := resp.DecodeProp(&getContentLength); err != nil && !internal.IsNotFound(err) {
			return err
		}

		o := CalendarObject{
			Path:          p,
			ModTime:       time.Time(getLastMod.LastModified),
			ContentLength: getContentLength.Length,
			ETag:          string(getETag.ETag),
		}

		// Many servers return the calendar-data inline in the sync-collection
		// response (the query already requests it via CompRequest); decode it so
		// the caller need not follow up with a multiget. When the server omits the
		// data, Data is left nil and the caller fetches it as before.
		var calData calendarDataResp
		if err := resp.DecodeProp(&calData); err == nil {
			cal, err := ical.NewDecoder(bytes.NewReader(calData.Data)).Decode()
			if err != nil {
				return err
			}
			o.Data = cal
		} else if !internal.IsNotFound(err) {
			return err
		}

		ret.Updated = append(ret.Updated, o)
		return nil
	})
	if err != nil {
		return nil, err
	}

	ret.SyncToken = ms.SyncToken
	return ret, errors.Join(errs...)

}
