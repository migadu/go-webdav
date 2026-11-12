package caldav

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// A filter which breaks the grammar of RFC 4791 sections 9.7.1 to 9.7.3, or
// a time-range which breaks section 9.9, fails the CALDAV:valid-filter
// precondition of calendar-query (section 7.8). Section 1.3 asks for 403 or
// 409 with the precondition in a DAV:error element; the request always
// fails, so 403.
func TestCalendarQueryValidFilterPrecondition(t *testing.T) {
	h := &Handler{Backend: queryBackend{testBackend{calendars: []Calendar{{Path: "/user/calendars/work/"}}}}}

	for _, tc := range []struct {
		name string
		// The filter in the VEVENT comp-filter
		filter string
	}{
		{
			name:   "comp-filter is-not-defined with prop-filter",
			filter: `<C:is-not-defined/><C:prop-filter name="SUMMARY"/>`,
		},
		{
			name:   "comp-filter is-not-defined with time-range",
			filter: `<C:is-not-defined/><C:time-range start="20060104T000000Z"/>`,
		},
		{
			name:   "nested comp-filter is-not-defined with comp-filter",
			filter: `<C:comp-filter name="VALARM"><C:is-not-defined/><C:comp-filter name="X-CHILD"/></C:comp-filter>`,
		},
		{
			name: "prop-filter is-not-defined with text-match",
			filter: `<C:prop-filter name="SUMMARY">
  <C:is-not-defined/>
  <C:text-match>Meet</C:text-match>
</C:prop-filter>`,
		},
		{
			name: "prop-filter is-not-defined with param-filter",
			filter: `<C:prop-filter name="ATTENDEE">
  <C:is-not-defined/>
  <C:param-filter name="PARTSTAT"/>
</C:prop-filter>`,
		},
		{
			name: "param-filter is-not-defined with text-match",
			filter: `<C:prop-filter name="ATTENDEE">
  <C:param-filter name="PARTSTAT">
    <C:is-not-defined/>
    <C:text-match>ACCEPTED</C:text-match>
  </C:param-filter>
</C:prop-filter>`,
		},
		{
			name:   "time-range without start and end",
			filter: `<C:time-range/>`,
		},
		{
			name:   "time-range ending before its start",
			filter: `<C:time-range start="20060105T000000Z" end="20060104T000000Z"/>`,
		},
		{
			name:   "time-range ending at its start",
			filter: `<C:time-range start="20060104T000000Z" end="20060104T000000Z"/>`,
		},
		{
			name:   "prop-filter time-range without start and end",
			filter: `<C:prop-filter name="DTSTAMP"><C:time-range/></C:prop-filter>`,
		},
		{
			name:   "prop-filter time-range ending at its start",
			filter: `<C:prop-filter name="DTSTAMP"><C:time-range start="20060104T000000Z" end="20060104T000000Z"/></C:prop-filter>`,
		},
		{
			name: "prop-filter with time-range and text-match",
			filter: `<C:prop-filter name="DTSTAMP">
  <C:time-range start="20060104T000000Z" end="20060105T000000Z"/>
  <C:text-match>2006</C:text-match>
</C:prop-filter>`,
		},
		{
			name:   "two time-ranges",
			filter: `<C:time-range start="20060104T000000Z"/><C:time-range end="20060105T000000Z"/>`,
		},
		{
			name:   "two is-not-defined",
			filter: `<C:comp-filter name="VALARM"><C:is-not-defined/><C:is-not-defined/></C:comp-filter>`,
		},
		{
			name: "prop-filter with two text-matches",
			filter: `<C:prop-filter name="SUMMARY">
  <C:text-match>Meet</C:text-match>
  <C:text-match negate-condition="yes">Lunch</C:text-match>
</C:prop-filter>`,
		},
		{
			name: "prop-filter with two time-ranges",
			filter: `<C:prop-filter name="DTSTAMP">
  <C:time-range start="20060104T000000Z"/>
  <C:time-range end="20060105T000000Z"/>
</C:prop-filter>`,
		},
		{
			name: "param-filter with two text-matches",
			filter: `<C:prop-filter name="ATTENDEE">
  <C:param-filter name="PARTSTAT">
    <C:text-match>ACCEPTED</C:text-match>
    <C:text-match>DECLINED</C:text-match>
  </C:param-filter>
</C:prop-filter>`,
		},
		{
			name:   "comp-filter without a name",
			filter: `<C:comp-filter/>`,
		},
		{
			name:   "prop-filter without a name",
			filter: `<C:prop-filter><C:text-match>Meet</C:text-match></C:prop-filter>`,
		},
		{
			name:   "param-filter without a name",
			filter: `<C:prop-filter name="ATTENDEE"><C:param-filter/></C:prop-filter>`,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			body := `<?xml version="1.0" encoding="utf-8"?>
<C:calendar-query xmlns:D="DAV:" xmlns:C="urn:ietf:params:xml:ns:caldav">
  <D:prop><D:getetag/></D:prop>
  <C:filter>
    <C:comp-filter name="VCALENDAR">
      <C:comp-filter name="VEVENT">` + tc.filter + `</C:comp-filter>
    </C:comp-filter>
  </C:filter>
</C:calendar-query>`
			req := httptest.NewRequest("REPORT", "/user/calendars/work/", strings.NewReader(body))
			req.Header.Set("Content-Type", "application/xml")
			req.Header.Set("Depth", "1")
			w := httptest.NewRecorder()
			h.ServeHTTP(w, req)

			if w.Code != http.StatusForbidden {
				t.Errorf("status = %d, want 403 Forbidden", w.Code)
			}
			if !hasCalDAVPrecondition(t, w, PreconditionValidFilter) {
				t.Errorf("body isn't a DAV:error with CALDAV:valid-filter:\n%s", w.Body.String())
			}
		})
	}
}

// RFC 4791 section 9.9 defines a time-range for the VEVENT, VTODO, VJOURNAL,
// VFREEBUSY and VALARM components and for seven properties only: "The
// semantic of CALDAV:time-range is not defined for any other calendar
// components and properties." Section 7.8 gives a time-range in a SUMMARY
// prop-filter as an example of a filter which fails CALDAV:valid-filter.
func TestCalendarQueryTimeRangeUndefined(t *testing.T) {
	const calPath = "/user/calendars/work/"
	co := calendarObject(t, "BEGIN:VEVENT\r\nDTSTAMP:20060206T001102Z\r\nUID:event\r\nSUMMARY:Meeting\r\n"+
		"DTSTART:20060104T100000Z\r\nRECURRENCE-ID:20060104T100000Z\r\nEND:VEVENT\r\n")
	co.Path = calPath + "event.ics"
	h := &Handler{Backend: queryBackend{testBackend{
		calendars: []Calendar{{Path: calPath}},
		objectMap: map[string][]CalendarObject{calPath: {co}},
	}}}

	const timeRange = `<C:time-range start="20060104T000000Z" end="20060105T000000Z"/>`
	for _, tc := range []struct {
		name string
		// The content of the VCALENDAR comp-filter
		filter string
	}{
		{"VCALENDAR", timeRange},
		{"VTIMEZONE", `<C:comp-filter name="VTIMEZONE">` + timeRange + `</C:comp-filter>`},
		{"STANDARD", `<C:comp-filter name="VTIMEZONE"><C:comp-filter name="STANDARD">` + timeRange + `</C:comp-filter></C:comp-filter>`},
		{"SUMMARY", `<C:comp-filter name="VEVENT"><C:prop-filter name="SUMMARY">` + timeRange + `</C:prop-filter></C:comp-filter>`},
		{"RECURRENCE-ID", `<C:comp-filter name="VEVENT"><C:prop-filter name="RECURRENCE-ID">` + timeRange + `</C:prop-filter></C:comp-filter>`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			body := `<?xml version="1.0" encoding="utf-8"?>
<C:calendar-query xmlns:D="DAV:" xmlns:C="urn:ietf:params:xml:ns:caldav">
  <D:prop><D:getetag/></D:prop>
  <C:filter><C:comp-filter name="VCALENDAR">` + tc.filter + `</C:comp-filter></C:filter>
</C:calendar-query>`
			req := httptest.NewRequest("REPORT", calPath, strings.NewReader(body))
			req.Header.Set("Content-Type", "application/xml")
			req.Header.Set("Depth", "1")
			w := httptest.NewRecorder()
			h.ServeHTTP(w, req)

			if w.Code != http.StatusForbidden {
				t.Errorf("status = %d, want 403 Forbidden", w.Code)
			}
			if !hasCalDAVPrecondition(t, w, PreconditionValidFilter) {
				t.Errorf("body isn't a DAV:error with CALDAV:valid-filter:\n%s", w.Body.String())
			}
		})
	}
}

// RFC 4791 section 7.8: "a CALDAV:filter cannot nest a <C:comp
// name="VEVENT"> element in a <C:comp name="VTODO"> element". Each component
// of RFC 5545 has its place: VEVENT, VTODO, VJOURNAL, VFREEBUSY and VTIMEZONE
// in VCALENDAR (section 3.6), VALARM in VEVENT or VTODO, STANDARD and
// DAYLIGHT in VTIMEZONE.
func TestCalendarQueryCompFilterNesting(t *testing.T) {
	h := &Handler{Backend: queryBackend{testBackend{calendars: []Calendar{{Path: "/user/calendars/work/"}}}}}

	for _, tc := range []struct {
		name string
		// The content of the VCALENDAR comp-filter
		filter string
	}{
		{"VEVENT in VTODO", `<C:comp-filter name="VTODO"><C:comp-filter name="VEVENT"/></C:comp-filter>`},
		{"VTIMEZONE in VEVENT", `<C:comp-filter name="VEVENT"><C:comp-filter name="VTIMEZONE"/></C:comp-filter>`},
		{"VCALENDAR in VCALENDAR", `<C:comp-filter name="VCALENDAR"/>`},
		{"VALARM in VCALENDAR", `<C:comp-filter name="VALARM"/>`},
		{"VALARM in VJOURNAL", `<C:comp-filter name="VJOURNAL"><C:comp-filter name="VALARM"/></C:comp-filter>`},
		{"VALARM in VALARM", `<C:comp-filter name="VEVENT"><C:comp-filter name="VALARM"><C:comp-filter name="VALARM"/></C:comp-filter></C:comp-filter>`},
		{"STANDARD in VCALENDAR", `<C:comp-filter name="STANDARD"/>`},
		{"DAYLIGHT in VEVENT", `<C:comp-filter name="VEVENT"><C:comp-filter name="DAYLIGHT"/></C:comp-filter>`},
		{"VEVENT in an X- component", `<C:comp-filter name="X-WRAPPER"><C:comp-filter name="VEVENT"/></C:comp-filter>`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			body := `<?xml version="1.0" encoding="utf-8"?>
<C:calendar-query xmlns:D="DAV:" xmlns:C="urn:ietf:params:xml:ns:caldav">
  <D:prop><D:getetag/></D:prop>
  <C:filter><C:comp-filter name="VCALENDAR">` + tc.filter + `</C:comp-filter></C:filter>
</C:calendar-query>`
			req := httptest.NewRequest("REPORT", "/user/calendars/work/", strings.NewReader(body))
			req.Header.Set("Content-Type", "application/xml")
			req.Header.Set("Depth", "1")
			w := httptest.NewRecorder()
			h.ServeHTTP(w, req)

			if w.Code != http.StatusForbidden {
				t.Errorf("status = %d, want 403 Forbidden", w.Code)
			}
			if !hasCalDAVPrecondition(t, w, PreconditionValidFilter) {
				t.Errorf("body isn't a DAV:error with CALDAV:valid-filter:\n%s", w.Body.String())
			}
		})
	}
}

// No precondition of calendar-query fits a server which can't afford to
// expand a recurring component up to the time range (RFC 4791 section 7.8):
// it's a 500, which names the component, rather than a wrong answer.
func TestCalendarQueryTooManyOccurrences(t *testing.T) {
	const calPath = "/user/calendars/work/"
	co := calendarObject(t, "BEGIN:VEVENT\r\nDTSTAMP:20060206T001102Z\r\nUID:secondly\r\n"+
		"DTSTART:20060101T000000Z\r\nDURATION:PT1S\r\nRRULE:FREQ=SECONDLY\r\nEND:VEVENT\r\n")
	co.Path = calPath + "secondly.ics"
	h := &Handler{Backend: queryBackend{testBackend{
		calendars: []Calendar{{Path: calPath}},
		objectMap: map[string][]CalendarObject{calPath: {co}},
	}}}
	body := `<?xml version="1.0" encoding="utf-8"?>
<C:calendar-query xmlns:D="DAV:" xmlns:C="urn:ietf:params:xml:ns:caldav">
  <D:prop><D:getetag/></D:prop>
  <C:filter><C:comp-filter name="VCALENDAR"><C:comp-filter name="VEVENT">
    <C:time-range start="22500101T000000Z" end="22500102T000000Z"/>
  </C:comp-filter></C:comp-filter></C:filter>
</C:calendar-query>`
	req := httptest.NewRequest("REPORT", calPath, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/xml")
	req.Header.Set("Depth", "1")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	if w.Code != http.StatusInternalServerError || !strings.Contains(w.Body.String(), `"secondly"`) {
		t.Errorf("status = %d, want 500 Internal Server Error naming the event; body:\n%s", w.Code, w.Body.String())
	}
}

// A time-range whose start has a fraction of a second isn't in the "date
// with UTC time" format, so the request is malformed, as for any other value
// that isn't one.
func TestCalendarQueryTimeRangeFraction(t *testing.T) {
	h := &Handler{Backend: queryBackend{testBackend{calendars: []Calendar{{Path: "/user/calendars/work/"}}}}}
	body := `<?xml version="1.0" encoding="utf-8"?>
<C:calendar-query xmlns:D="DAV:" xmlns:C="urn:ietf:params:xml:ns:caldav">
  <D:prop><D:getetag/></D:prop>
  <C:filter><C:comp-filter name="VCALENDAR"><C:comp-filter name="VEVENT">
    <C:time-range start="20060104T000000.5Z"/>
  </C:comp-filter></C:comp-filter></C:filter>
</C:calendar-query>`
	req := httptest.NewRequest("REPORT", "/user/calendars/work/", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/xml")
	req.Header.Set("Depth", "1")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	if w.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400 Bad Request", w.Code)
	}
}

// RFC 4791 section 9.5 requires a filter in calendar-query, and section 9.7
// makes it hold a single comp-filter. Section 9.7.1 sets the scope of that
// comp-filter to the calendar object, a VCALENDAR, and requires its name
// attribute. A query which breaks one of these fails CALDAV:valid-filter.
func TestCalendarQueryValidFilterRoot(t *testing.T) {
	h := &Handler{Backend: queryBackend{testBackend{calendars: []Calendar{{Path: "/user/calendars/work/"}}}}}

	for _, tc := range []struct {
		name string
		// What follows the DAV:prop element in the calendar-query
		query string
	}{
		{"no filter", ``},
		{"empty filter", `<C:filter/>`},
		{"comp-filter without a name", `<C:filter><C:comp-filter/></C:filter>`},
		{"VEVENT comp-filter", `<C:filter><C:comp-filter name="VEVENT"/></C:filter>`},
		{"two comp-filters", `<C:filter>
  <C:comp-filter name="VCALENDAR"/>
  <C:comp-filter name="VCALENDAR"><C:comp-filter name="VEVENT"/></C:comp-filter>
</C:filter>`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			body := `<?xml version="1.0" encoding="utf-8"?>
<C:calendar-query xmlns:D="DAV:" xmlns:C="urn:ietf:params:xml:ns:caldav">
  <D:prop><D:getetag/></D:prop>
  ` + tc.query + `
</C:calendar-query>`
			req := httptest.NewRequest("REPORT", "/user/calendars/work/", strings.NewReader(body))
			req.Header.Set("Content-Type", "application/xml")
			req.Header.Set("Depth", "1")
			w := httptest.NewRecorder()
			h.ServeHTTP(w, req)

			if w.Code != http.StatusForbidden {
				t.Errorf("status = %d, want 403 Forbidden", w.Code)
			}
			if !hasCalDAVPrecondition(t, w, PreconditionValidFilter) {
				t.Errorf("body isn't a DAV:error with CALDAV:valid-filter:\n%s", w.Body.String())
			}
		})
	}
}
