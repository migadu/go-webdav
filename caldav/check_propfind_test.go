package caldav

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// noCallBackend is a Backend none of whose methods can be called: they all
// panic.
type noCallBackend struct {
	Backend
}

// serveNoCall serves req with a Handler whose backend can't be called, and
// fails the test if the handler calls it.
func serveNoCall(t *testing.T, req *http.Request) *httptest.ResponseRecorder {
	t.Helper()
	w := httptest.NewRecorder()
	func() {
		defer func() {
			if v := recover(); v != nil {
				t.Errorf("%v %v called the backend: %v", req.Method, req.URL.Path, v)
			}
		}()
		h := Handler{Backend: noCallBackend{}}
		h.ServeHTTP(w, req)
	}()
	return w
}

// A PROPFIND request asking for no properties (no propname, allprop or prop
// element, RFC 4918 section 14.20) is refused before the backend is called,
// whatever the resource.
func TestPropFindWithoutProp(t *testing.T) {
	for _, p := range []string{
		"/",
		"/user/",
		"/user/calendars/",
		"/user/calendars/a/",
		"/user/calendars/a/1.ics",
	} {
		body := `<?xml version="1.0" encoding="UTF-8"?><D:propfind xmlns:D="DAV:"/>`
		req := httptest.NewRequest("PROPFIND", p, strings.NewReader(body))
		req.Header.Set("Content-Type", "application/xml")
		req.Header.Set("Depth", "1")
		if w := serveNoCall(t, req); w.Code != http.StatusBadRequest {
			t.Errorf("PROPFIND %v returned %v, want 400: %s", p, w.Code, w.Body.String())
		}
	}
}

const calendarQueryWithoutProp = `<?xml version="1.0" encoding="UTF-8"?>
<C:calendar-query xmlns:D="DAV:" xmlns:C="urn:ietf:params:xml:ns:caldav">
  <C:filter><C:comp-filter name="VCALENDAR"/></C:filter>
</C:calendar-query>`

// A calendar-query asking for no properties is refused before the backend is
// queried, like a calendar-multiget: the request fails the same way whether
// or not objects match.
func TestQueryWithoutProp(t *testing.T) {
	const calPath = "/user/calendars/a/"

	t.Run("no backend call", func(t *testing.T) {
		req := httptest.NewRequest("REPORT", calPath, strings.NewReader(calendarQueryWithoutProp))
		req.Header.Set("Content-Type", "application/xml")
		if w := serveNoCall(t, req); w.Code != http.StatusBadRequest {
			t.Errorf("REPORT returned %v, want 400: %s", w.Code, w.Body.String())
		}
	})

	// testBackend.QueryCalendarObjects matches nothing
	t.Run("no match", func(t *testing.T) {
		handler := Handler{Backend: testBackend{calendars: []Calendar{{Path: calPath}}}}
		req := httptest.NewRequest("REPORT", calPath, strings.NewReader(calendarQueryWithoutProp))
		req.Header.Set("Content-Type", "application/xml")
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, req)
		if w.Code != http.StatusBadRequest {
			t.Errorf("REPORT returned %v, want 400: %s", w.Code, w.Body.String())
		}
	})
}
