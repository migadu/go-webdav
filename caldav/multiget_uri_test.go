package caldav

import (
	"encoding/xml"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"

	"github.com/migadu/go-webdav/internal"
)

// multiStatusHrefCodes returns the status of each response of a multistatus,
// by href as sent, so that an href which isn't a path is seen as is.
func multiStatusHrefCodes(t *testing.T, w *httptest.ResponseRecorder) map[string]int {
	t.Helper()
	if w.Code != http.StatusMultiStatus {
		t.Fatalf("status = %d, want 207; body:\n%s", w.Code, w.Body)
	}
	var ms internal.MultiStatus
	if err := xml.NewDecoder(w.Body).Decode(&ms); err != nil {
		t.Fatalf("failed to decode multistatus: %v", err)
	}
	codes := make(map[string]int)
	for _, resp := range ms.Responses {
		for _, href := range resp.Hrefs {
			code := http.StatusOK
			if resp.Status != nil {
				code = resp.Status.Code
			}
			codes[href.String()] = code
		}
	}
	return codes
}

// An http(s) URL with an empty path names "/", and a URI of another scheme
// no resource of the server (RFC 3986 sections 5.2.2 and 6.2.3): neither is
// the Request-URI, here a calendar object, which only its own href may name
// (RFC 4791 section 7.9).
func TestMultiGetAbsoluteURIs(t *testing.T) {
	const (
		cal   = "/user/calendars/work/"
		event = cal + "event.ics"
	)
	newBackend := func() testBackend {
		return testBackend{
			calendars: []Calendar{{Path: cal}},
			objectMap: map[string][]CalendarObject{cal: {newMultiGetTestObject(event, "uid-1")}},
		}
	}
	body := `<?xml version="1.0" encoding="UTF-8"?>
<C:calendar-multiget xmlns:D="DAV:" xmlns:C="urn:ietf:params:xml:ns:caldav">
  <D:prop><D:getetag/></D:prop>
  <D:href>https://dav.example.com</D:href>
  <D:href>//dav.example.com</D:href>
  <D:href>urn:uuid:1234</D:href>
  <D:href>event.ics</D:href>
</C:calendar-multiget>`
	want := map[string]int{
		"/":             http.StatusNotFound,
		"urn:uuid:1234": http.StatusNotFound,
		event:           http.StatusOK,
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
			req := httptest.NewRequest("REPORT", event, strings.NewReader(body))
			req.Header.Set("Content-Type", "application/xml")
			w := httptest.NewRecorder()
			handler.ServeHTTP(w, req)

			if got := multiStatusHrefCodes(t, w); !reflect.DeepEqual(got, want) {
				t.Errorf("response statuses = %v, want %v", got, want)
			}
			if mb, ok := tc.backend.(*multiGetBackend); ok {
				if calls := [][]string{{event}}; !reflect.DeepEqual(mb.calls, calls) {
					t.Errorf("GetCalendarObjects calls = %v, want %v", mb.calls, calls)
				}
			}
		})
	}
}
