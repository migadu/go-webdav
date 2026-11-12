package caldav

import (
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
)

// RFC 4791 section 7.9: "If the Request-URI is a collection resource, then
// the DAV:href elements MUST refer to calendar object resources within that
// collection". The others are answered like missing objects, without asking
// the backend for them, which may serve them to another calendar's users.
func TestMultiGetOutOfScope(t *testing.T) {
	const (
		work = "/user/calendars/work/"
		home = "/user/calendars/home/"
	)
	newBackend := func() testBackend {
		return testBackend{
			calendars: []Calendar{{Path: work}, {Path: home}},
			objectMap: map[string][]CalendarObject{
				work: {newMultiGetTestObject(work+"event.ics", "uid-work")},
				home: {newMultiGetTestObject(home+"event.ics", "uid-home")},
			},
		}
	}
	body := `<?xml version="1.0" encoding="UTF-8"?>
<C:calendar-multiget xmlns:D="DAV:" xmlns:C="urn:ietf:params:xml:ns:caldav">
  <D:prop><D:getetag/></D:prop>
  <D:href>` + work + `event.ics</D:href>
  <D:href>` + home + `event.ics</D:href>
  <D:href>` + work + `../home/event.ics</D:href>
</C:calendar-multiget>`
	want := map[string]int{
		work + "event.ics":         http.StatusOK,
		home + "event.ics":         http.StatusNotFound,
		work + "../home/event.ics": http.StatusNotFound,
	}

	for _, tc := range []struct {
		name    string
		backend Backend
	}{
		{"GetCalendarObject", newBackend()},
		{"GetCalendarObjects", &multiGetBackend{testBackend: newBackend()}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			handler := Handler{Backend: tc.backend}
			req := httptest.NewRequest("REPORT", work, strings.NewReader(body))
			req.Header.Set("Content-Type", "application/xml")
			w := httptest.NewRecorder()
			handler.ServeHTTP(w, req)

			checkStatusCodes(t, multiStatusCodes(t, w), want)
			if mb, ok := tc.backend.(*multiGetBackend); ok {
				if calls := [][]string{{work + "event.ics"}}; !reflect.DeepEqual(mb.calls, calls) {
					t.Errorf("GetCalendarObjects calls = %v, want %v", mb.calls, calls)
				}
			}
		})
	}
}
