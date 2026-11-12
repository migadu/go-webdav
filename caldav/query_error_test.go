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
