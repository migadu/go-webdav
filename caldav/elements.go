package caldav

import (
	"encoding/xml"
	"fmt"
	"time"

	"github.com/migadu/go-webdav/internal"
)

const (
	namespace      = "urn:ietf:params:xml:ns:caldav"
	appleNamespace = "http://apple.com/ns/ical/"
)

var (
	calendarHomeSetName        = xml.Name{namespace, "calendar-home-set"}
	calendarUserAddressSetName = xml.Name{namespace, "calendar-user-address-set"}
	scheduleInboxURLName       = xml.Name{namespace, "schedule-inbox-URL"}

	calendarDescriptionName           = xml.Name{namespace, "calendar-description"}
	calendarTimezoneName              = xml.Name{namespace, "calendar-timezone"}
	supportedCalendarDataName         = xml.Name{namespace, "supported-calendar-data"}
	supportedCalendarComponentSetName = xml.Name{namespace, "supported-calendar-component-set"}
	supportedCollationSetName         = xml.Name{namespace, "supported-collation-set"}
	maxResourceSizeName               = xml.Name{namespace, "max-resource-size"}
	minDateTimeName                   = xml.Name{namespace, "min-date-time"}
	maxDateTimeName                   = xml.Name{namespace, "max-date-time"}
	maxInstancesName                  = xml.Name{namespace, "max-instances"}
	maxAttendeesPerInstanceName       = xml.Name{namespace, "max-attendees-per-instance"}

	calendarColorName = xml.Name{appleNamespace, "calendar-color"}

	calendarQueryName    = xml.Name{namespace, "calendar-query"}
	calendarMultigetName = xml.Name{namespace, "calendar-multiget"}
	mkcalendarName       = xml.Name{namespace, "mkcalendar"}

	calendarName      = xml.Name{namespace, "calendar"}
	scheduleInboxName = xml.Name{namespace, "schedule-inbox"}
	calendarDataName  = xml.Name{namespace, "calendar-data"}
)

// https://tools.ietf.org/html/rfc4791#section-6.2.1
//
// The property may hold any number of hrefs (DAV:href*), none included. An
// empty href refers to the request URL (RFC 3986 section 4.4).
type calendarHomeSet struct {
	XMLName xml.Name        `xml:"urn:ietf:params:xml:ns:caldav calendar-home-set"`
	Hrefs   []internal.Href `xml:"DAV: href"`
}

func (a *calendarHomeSet) GetXMLName() xml.Name {
	return calendarHomeSetName
}

// https://datatracker.ietf.org/doc/html/rfc6638#section-2.4.1
type calendarUserAddressSet struct {
	XMLName   xml.Name `xml:"urn:ietf:params:xml:ns:caldav calendar-user-address-set"`
	Addresses []string `xml:"DAV: href"`
}

// https://datatracker.ietf.org/doc/html/rfc6638#section-2.2.1
type scheduleInboxURL struct {
	XMLName xml.Name      `xml:"urn:ietf:params:xml:ns:caldav schedule-inbox-URL"`
	Href    internal.Href `xml:"DAV: href"`
}

// https://tools.ietf.org/html/rfc4791#section-5.2.1
type calendarDescription struct {
	XMLName     xml.Name `xml:"urn:ietf:params:xml:ns:caldav calendar-description"`
	Description string   `xml:",chardata"`
}

// https://tools.ietf.org/html/rfc4791#section-5.2.2
type calendarTimezone struct {
	XMLName  xml.Name `xml:"urn:ietf:params:xml:ns:caldav calendar-timezone"`
	Timezone string   `xml:",chardata"`
}

type calendarColor struct {
	XMLName xml.Name `xml:"http://apple.com/ns/ical/ calendar-color"`
	Color   string   `xml:",chardata"`
}

// https://tools.ietf.org/html/rfc4791#section-5.2.4
type supportedCalendarData struct {
	XMLName xml.Name           `xml:"urn:ietf:params:xml:ns:caldav supported-calendar-data"`
	Types   []calendarDataType `xml:"calendar-data"`
}

// https://tools.ietf.org/html/rfc4791#section-5.2.3
type supportedCalendarComponentSet struct {
	XMLName xml.Name `xml:"urn:ietf:params:xml:ns:caldav supported-calendar-component-set"`
	Comp    []comp   `xml:"comp"`
}

// https://tools.ietf.org/html/rfc4791#section-7.5.1
type supportedCollationSet struct {
	XMLName    xml.Name `xml:"urn:ietf:params:xml:ns:caldav supported-collation-set"`
	Collations []string `xml:"supported-collation"`
}

// https://tools.ietf.org/html/rfc4791#section-9.6
type calendarDataType struct {
	XMLName     xml.Name `xml:"urn:ietf:params:xml:ns:caldav calendar-data"`
	ContentType string   `xml:"content-type,attr"`
	Version     string   `xml:"version,attr"`
}

// https://tools.ietf.org/html/rfc4791#section-5.2.5
type maxResourceSize struct {
	XMLName xml.Name `xml:"urn:ietf:params:xml:ns:caldav max-resource-size"`
	Size    int64    `xml:",chardata"`
}

// https://tools.ietf.org/html/rfc4791#section-9.5
type calendarQuery struct {
	XMLName  xml.Name       `xml:"urn:ietf:params:xml:ns:caldav calendar-query"`
	Prop     *internal.Prop `xml:"DAV: prop,omitempty"`
	AllProp  *struct{}      `xml:"DAV: allprop,omitempty"`
	PropName *struct{}      `xml:"DAV: propname,omitempty"`
	// Filter is a pointer to tell a missing filter, which section 9.5
	// requires, from an empty one
	Filter *filter `xml:"filter"`
	// TODO: timezone
}

// https://tools.ietf.org/html/rfc4791#section-9.10
type calendarMultiget struct {
	XMLName  xml.Name        `xml:"urn:ietf:params:xml:ns:caldav calendar-multiget"`
	Hrefs    []internal.Href `xml:"DAV: href"`
	Prop     *internal.Prop  `xml:"DAV: prop,omitempty"`
	AllProp  *struct{}       `xml:"DAV: allprop,omitempty"`
	PropName *struct{}       `xml:"DAV: propname,omitempty"`
}

// https://tools.ietf.org/html/rfc4791#section-9.7
//
// The filter holds a single comp-filter. CompFilters is a slice to tell more
// than one, which encoding/xml would decode onto the first one, from a single
// one.
type filter struct {
	XMLName     xml.Name     `xml:"urn:ietf:params:xml:ns:caldav filter"`
	CompFilters []compFilter `xml:"comp-filter"`
}

// https://tools.ietf.org/html/rfc4791#section-9.7.1
//
// The grammar allows at most one is-not-defined and one time-range element.
// They're slices to tell a repeated element, which encoding/xml would decode
// onto the first one, from a single one. So are the is-not-defined,
// time-range and text-match elements of propFilter and paramFilter.
type compFilter struct {
	XMLName      xml.Name     `xml:"urn:ietf:params:xml:ns:caldav comp-filter"`
	Name         string       `xml:"name,attr"`
	IsNotDefined []struct{}   `xml:"is-not-defined,omitempty"`
	TimeRange    []timeRange  `xml:"time-range,omitempty"`
	PropFilters  []propFilter `xml:"prop-filter,omitempty"`
	CompFilters  []compFilter `xml:"comp-filter,omitempty"`
}

// https://tools.ietf.org/html/rfc4791#section-9.7.2
type propFilter struct {
	XMLName      xml.Name      `xml:"urn:ietf:params:xml:ns:caldav prop-filter"`
	Name         string        `xml:"name,attr"`
	IsNotDefined []struct{}    `xml:"is-not-defined,omitempty"`
	TimeRange    []timeRange   `xml:"time-range,omitempty"`
	TextMatch    []textMatch   `xml:"text-match,omitempty"`
	ParamFilter  []paramFilter `xml:"param-filter,omitempty"`
}

// https://tools.ietf.org/html/rfc4791#section-9.7.3
type paramFilter struct {
	XMLName      xml.Name    `xml:"urn:ietf:params:xml:ns:caldav param-filter"`
	Name         string      `xml:"name,attr"`
	IsNotDefined []struct{}  `xml:"is-not-defined,omitempty"`
	TextMatch    []textMatch `xml:"text-match,omitempty"`
}

// https://tools.ietf.org/html/rfc4791#section-9.7.5
type textMatch struct {
	XMLName         xml.Name        `xml:"urn:ietf:params:xml:ns:caldav text-match"`
	Text            string          `xml:",chardata"`
	Collation       string          `xml:"collation,attr,omitempty"`
	NegateCondition negateCondition `xml:"negate-condition,attr,omitempty"`
}

type negateCondition bool

func (nc *negateCondition) UnmarshalText(b []byte) error {
	switch s := string(b); s {
	case "yes":
		*nc = true
	case "no":
		*nc = false
	default:
		return fmt.Errorf("caldav: invalid negate-condition value: %q", s)
	}
	return nil
}

func (nc negateCondition) MarshalText() ([]byte, error) {
	if nc {
		return []byte("yes"), nil
	}
	return nil, nil
}

// https://tools.ietf.org/html/rfc4791#section-9.9
type timeRange struct {
	XMLName xml.Name         `xml:"urn:ietf:params:xml:ns:caldav time-range"`
	Start   *dateWithUTCTime `xml:"start,attr,omitempty"`
	End     *dateWithUTCTime `xml:"end,attr,omitempty"`
}

// newTimeRange returns the time-range element for [start, end), leaving out
// the attribute of a zero bound: RFC 4791 section 9.9 reads a missing start
// as -infinity and a missing end as +infinity.
//
// The bounds are truncated to the second, as the "date with UTC time" format
// is, so that check sees the range that is sent, as for newRequiredTimeRange.
func newTimeRange(start, end time.Time) timeRange {
	var tr timeRange
	if !start.IsZero() {
		t := dateWithUTCTime(start.UTC().Truncate(time.Second))
		tr.Start = &t
	}
	if !end.IsZero() {
		t := dateWithUTCTime(end.UTC().Truncate(time.Second))
		tr.End = &t
	}
	return tr
}

// check checks tr against RFC 4791 section 9.9: at least one of start and
// end, and the end after the start when both are there.
func (tr *timeRange) check() error {
	if tr.Start == nil && tr.End == nil {
		return fmt.Errorf("caldav: time-range requires a start or an end attribute")
	}
	if tr.Start != nil && tr.End != nil && !time.Time(*tr.End).After(time.Time(*tr.Start)) {
		return fmt.Errorf("caldav: the end of time-range must be after its start")
	}
	return nil
}

// bounds returns the start and end of the time range, zero when unbounded.
func (tr *timeRange) bounds() (start, end time.Time) {
	if tr.Start != nil {
		start = time.Time(*tr.Start)
	}
	if tr.End != nil {
		end = time.Time(*tr.End)
	}
	return start, end
}

const dateWithUTCTimeLayout = "20060102T150405Z"

// dateWithUTCTime is the "date with UTC time" format defined in RFC 5545 page
// 34.
type dateWithUTCTime time.Time

func (t *dateWithUTCTime) UnmarshalText(b []byte) error {
	// time.Parse accepts a fraction of a second after the seconds, which
	// the RFC 5545 section 3.3.5 form doesn't have: it's always 16 octets
	if len(b) != len(dateWithUTCTimeLayout) {
		return fmt.Errorf("caldav: invalid date with UTC time: %q", b)
	}
	tt, err := time.Parse(dateWithUTCTimeLayout, string(b))
	if err != nil {
		return err
	}
	*t = dateWithUTCTime(tt)
	return nil
}

func (t *dateWithUTCTime) MarshalText() ([]byte, error) {
	s := time.Time(*t).UTC().Format(dateWithUTCTimeLayout)
	return []byte(s), nil
}

// Request variant of https://tools.ietf.org/html/rfc4791#section-9.6
//
// The request holds at most one comp, expand, limit-recurrence-set and
// limit-freebusy-set element. They're slices to tell a repeated element,
// which encoding/xml would decode onto the first one, from a single one.
type calendarDataReq struct {
	XMLName            xml.Name             `xml:"urn:ietf:params:xml:ns:caldav calendar-data"`
	Comp               []comp               `xml:"comp,omitempty"`
	Expand             []expand             `xml:"expand,omitempty"`
	LimitRecurrenceSet []limitRecurrenceSet `xml:"limit-recurrence-set,omitempty"`
	LimitFreeBusySet   []limitFreeBusySet   `xml:"limit-freebusy-set,omitempty"`
}

// https://tools.ietf.org/html/rfc4791#section-9.6.1
type comp struct {
	XMLName xml.Name `xml:"urn:ietf:params:xml:ns:caldav comp"`
	Name    string   `xml:"name,attr"`

	Allprop *struct{} `xml:"allprop,omitempty"`
	Prop    []prop    `xml:"prop,omitempty"`

	Allcomp *struct{} `xml:"allcomp,omitempty"`
	Comp    []comp    `xml:"comp,omitempty"`
}

// requiredTimeRange holds the start and end attributes of the expand,
// limit-recurrence-set and limit-freebusy-set elements. Both are required;
// they are pointers to tell a missing attribute from a zero time.
type requiredTimeRange struct {
	Start *dateWithUTCTime `xml:"start,attr,omitempty"`
	End   *dateWithUTCTime `xml:"end,attr,omitempty"`
}

// newRequiredTimeRange returns the time range from start to end, without the
// attribute of a zero time.
//
// The times are truncated to the second, as the "date with UTC time" format
// is, so that check sees the range that is sent: a range shorter than a
// second would otherwise pass it and go out with the end equal to the start.
func newRequiredTimeRange(start, end time.Time) requiredTimeRange {
	var tr requiredTimeRange
	if !start.IsZero() {
		s := dateWithUTCTime(start.UTC().Truncate(time.Second))
		tr.Start = &s
	}
	if !end.IsZero() {
		e := dateWithUTCTime(end.UTC().Truncate(time.Second))
		tr.End = &e
	}
	return tr
}

// check checks tr, the time range of the element called name. RFC 4791
// sections 9.6.5 to 9.6.7 require a start and an end, the end after the
// start.
func (tr *requiredTimeRange) check(name string) error {
	if tr.Start == nil || tr.End == nil {
		return fmt.Errorf("caldav: %s requires a start and an end attribute", name)
	}
	if !time.Time(*tr.End).After(time.Time(*tr.Start)) {
		return fmt.Errorf("caldav: the end of %s must be after its start", name)
	}
	return nil
}

// https://tools.ietf.org/html/rfc4791#section-9.6.5
type expand struct {
	XMLName xml.Name `xml:"urn:ietf:params:xml:ns:caldav expand"`
	requiredTimeRange
}

// https://tools.ietf.org/html/rfc4791#section-9.6.6
type limitRecurrenceSet struct {
	XMLName xml.Name `xml:"urn:ietf:params:xml:ns:caldav limit-recurrence-set"`
	requiredTimeRange
}

// https://tools.ietf.org/html/rfc4791#section-9.6.7
type limitFreeBusySet struct {
	XMLName xml.Name `xml:"urn:ietf:params:xml:ns:caldav limit-freebusy-set"`
	requiredTimeRange
}

// https://tools.ietf.org/html/rfc4791#section-9.6.4
type prop struct {
	XMLName xml.Name `xml:"urn:ietf:params:xml:ns:caldav prop"`
	Name    string   `xml:"name,attr"`
	NoValue noValue  `xml:"novalue,attr,omitempty"`
}

// noValue is the novalue attribute of CALDAV:prop, "yes" or "no".
type noValue bool

func (nv *noValue) UnmarshalText(b []byte) error {
	switch s := string(b); s {
	case "yes":
		*nv = true
	case "no":
		*nv = false
	default:
		return fmt.Errorf("caldav: invalid novalue value: %q", s)
	}
	return nil
}

func (nv noValue) MarshalText() ([]byte, error) {
	if nv {
		return []byte("yes"), nil
	}
	return []byte("no"), nil
}

// Response variant of https://tools.ietf.org/html/rfc4791#section-9.6
type calendarDataResp struct {
	XMLName xml.Name `xml:"urn:ietf:params:xml:ns:caldav calendar-data"`
	Data    []byte   `xml:",chardata"`
}

type reportReq struct {
	Query          *calendarQuery
	Multiget       *calendarMultiget
	SyncCollection *internal.SyncCollectionQuery
	// TODO: CALDAV:free-busy-query
}

func (r *reportReq) UnmarshalXML(d *xml.Decoder, start xml.StartElement) error {
	var v interface{}
	switch start.Name {
	case calendarQueryName:
		r.Query = &calendarQuery{}
		v = r.Query
	case calendarMultigetName:
		r.Multiget = &calendarMultiget{}
		v = r.Multiget
	case internal.SyncCollectionName:
		r.SyncCollection = &internal.SyncCollectionQuery{}
		v = r.SyncCollection
	default:
		return fmt.Errorf("caldav: unsupported REPORT root %q %q", start.Name.Space, start.Name.Local)
	}

	return d.DecodeElement(v, &start)
}

// mkcalendarSetReq is the body of a MKCALENDAR request sent by a Client (RFC
// 4791 section 9.2). The server decodes it with
// internal.DecodeCreateCollectionRequest.
type mkcalendarSetReq struct {
	XMLName xml.Name     `xml:"urn:ietf:params:xml:ns:caldav mkcalendar"`
	Set     internal.Set `xml:"DAV: set"`
}
