package caldav

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestCalendarQueryInvalidFilterReturns400(t *testing.T) {
	h := &Handler{Backend: queryBackend{testBackend{calendars: []Calendar{{Path: "/user/calendars/work/"}}}}}

	for _, tc := range []struct {
		name string
		body string
	}{
		{
			name: "comp-filter is-not-defined with prop-filter",
			body: `<?xml version="1.0" encoding="utf-8"?>
<C:calendar-query xmlns:D="DAV:" xmlns:C="urn:ietf:params:xml:ns:caldav">
  <D:prop><D:getetag/></D:prop>
  <C:filter>
    <C:comp-filter name="VCALENDAR">
      <C:comp-filter name="VEVENT">
        <C:is-not-defined/>
        <C:prop-filter name="SUMMARY"/>
      </C:comp-filter>
    </C:comp-filter>
  </C:filter>
</C:calendar-query>`,
		},
		{
			name: "prop-filter is-not-defined with text-match",
			body: `<?xml version="1.0" encoding="utf-8"?>
<C:calendar-query xmlns:D="DAV:" xmlns:C="urn:ietf:params:xml:ns:caldav">
  <D:prop><D:getetag/></D:prop>
  <C:filter>
    <C:comp-filter name="VCALENDAR">
      <C:comp-filter name="VEVENT">
        <C:prop-filter name="SUMMARY">
          <C:is-not-defined/>
          <C:text-match>Meet</C:text-match>
        </C:prop-filter>
      </C:comp-filter>
    </C:comp-filter>
  </C:filter>
</C:calendar-query>`,
		},
		{
			name: "param-filter is-not-defined with text-match",
			body: `<?xml version="1.0" encoding="utf-8"?>
<C:calendar-query xmlns:D="DAV:" xmlns:C="urn:ietf:params:xml:ns:caldav">
  <D:prop><D:getetag/></D:prop>
  <C:filter>
    <C:comp-filter name="VCALENDAR">
      <C:comp-filter name="VEVENT">
        <C:prop-filter name="ATTENDEE">
          <C:param-filter name="PARTSTAT">
            <C:is-not-defined/>
            <C:text-match>ACCEPTED</C:text-match>
          </C:param-filter>
        </C:prop-filter>
      </C:comp-filter>
    </C:comp-filter>
  </C:filter>
</C:calendar-query>`,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest("REPORT", "/user/calendars/work/", strings.NewReader(tc.body))
			req.Header.Set("Content-Type", "application/xml")
			req.Header.Set("Depth", "1")
			w := httptest.NewRecorder()
			h.ServeHTTP(w, req)

			if w.Code != http.StatusBadRequest {
				t.Errorf("status = %d, want 400 Bad Request", w.Code)
			}
		})
	}
}
