package caldav

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// An is-not-defined element with siblings breaks the grammar of RFC 4791
// sections 9.7.1 to 9.7.3, so the CALDAV:valid-filter precondition of
// calendar-query (section 7.8) fails. Section 1.3 asks for 403 or 409 with
// the precondition in a DAV:error element; the request always fails, so 403.
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
