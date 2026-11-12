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
				work: {
					newMultiGetTestObject(work+"event.ics", "uid-work"),
					newMultiGetTestObject(work+"other.ics", "uid-other"),
				},
				home: {newMultiGetTestObject(home+"event.ics", "uid-home")},
			},
		}
	}
	body := `<?xml version="1.0" encoding="UTF-8"?>
<C:calendar-multiget xmlns:D="DAV:" xmlns:C="urn:ietf:params:xml:ns:caldav">
  <D:prop><D:getetag/></D:prop>
  <D:href>` + work + `event.ics</D:href>
  <D:href>` + home + `event.ics</D:href>
  <D:href>` + work + `../home/missing.ics</D:href>
  <D:href>other.ics</D:href>
</C:calendar-multiget>`
	// Responses are for the hrefs resolved against the Request-URI, with
	// dot-segments removed (RFC 4918 section 8.3)
	want := map[string]int{
		work + "event.ics":   http.StatusOK,
		home + "event.ics":   http.StatusNotFound,
		home + "missing.ics": http.StatusNotFound,
		work + "other.ics":   http.StatusOK,
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
				if calls := [][]string{{work + "event.ics", work + "other.ics"}}; !reflect.DeepEqual(mb.calls, calls) {
					t.Errorf("GetCalendarObjects calls = %v, want %v", mb.calls, calls)
				}
			}
		})
	}
}

// The hrefs are resolved against the Request-URI before reaching the backend,
// which may not know what to do with a relative reference or dot-segments
// (RFC 4918 section 8.3, RFC 3986 section 5.2).
func TestMultiGetHrefResolution(t *testing.T) {
	const cal = "/user/calendars/work/"
	backend := &multiGetBackend{testBackend: testBackend{
		calendars: []Calendar{{Path: cal}},
		objectMap: map[string][]CalendarObject{cal: {newMultiGetTestObject(cal+"event.ics", "uid-1")}},
	}}
	handler := Handler{Backend: backend}
	body := `<?xml version="1.0" encoding="UTF-8"?>
<C:calendar-multiget xmlns:D="DAV:" xmlns:C="urn:ietf:params:xml:ns:caldav">
  <D:prop><D:getetag/></D:prop>
  <D:href>event.ics</D:href>
  <D:href>./sub/../event.ics</D:href>
  <D:href>` + cal + `./event.ics</D:href>
  <D:href>https://dav.example.com` + cal + `event.ics</D:href>
</C:calendar-multiget>`
	req := httptest.NewRequest("REPORT", cal, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/xml")
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	checkStatusCodes(t, multiStatusCodes(t, w), map[string]int{cal + "event.ics": http.StatusOK})
	if want := [][]string{{cal + "event.ics", cal + "event.ics", cal + "event.ics", cal + "event.ics"}}; !reflect.DeepEqual(backend.calls, want) {
		t.Errorf("GetCalendarObjects calls = %v, want %v", backend.calls, want)
	}
}

// RFC 4791 section 9.10: <!ELEMENT calendar-multiget ((DAV:allprop |
// DAV:propname | DAV:prop)?, DAV:href+)>
func TestMultiGetWithoutHref(t *testing.T) {
	const cal = "/user/calendars/work/"
	handler := Handler{Backend: testBackend{calendars: []Calendar{{Path: cal}}}}
	body := `<?xml version="1.0" encoding="UTF-8"?>
<C:calendar-multiget xmlns:D="DAV:" xmlns:C="urn:ietf:params:xml:ns:caldav">
  <D:prop><D:getetag/></D:prop>
</C:calendar-multiget>`
	req := httptest.NewRequest("REPORT", cal, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/xml")
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)
	if w.Code != http.StatusBadRequest {
		t.Errorf("status = %v, want 400; body:\n%s", w.Code, w.Body)
	}
}
