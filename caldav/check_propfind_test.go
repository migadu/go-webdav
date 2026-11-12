package caldav

import (
	"encoding/xml"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"

	"github.com/emersion/go-webdav/internal"
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

// statusResponses serves req with handler, and returns the hrefs and status
// of each response of the 207 Multi-Status it gets, followed by "propstat"
// for one with properties.
func statusResponses(t *testing.T, handler http.Handler, req *http.Request) []string {
	t.Helper()
	req.Header.Set("Content-Type", "application/xml")
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)
	if w.Code != http.StatusMultiStatus {
		t.Fatalf("%v returned %v, want 207: %s", req.Method, w.Code, w.Body.String())
	}
	var ms internal.MultiStatus
	if err := xml.NewDecoder(w.Body).Decode(&ms); err != nil {
		t.Fatal(err)
	}
	l := make([]string, 0, len(ms.Responses))
	for _, resp := range ms.Responses {
		s := strings.Join(resp.Paths(), " ")
		if resp.Status != nil {
			s += fmt.Sprintf(" %v", resp.Status.Code)
		}
		if len(resp.PropStats) > 0 {
			s += " propstat"
		}
		l = append(l, s)
	}
	return l
}

// calendarQueryWithoutProp returns a calendar-query with no propname, allprop
// or prop element, for the events whose UID contains uid.
func calendarQueryWithoutProp(uid string) string {
	return `<?xml version="1.0" encoding="UTF-8"?>
<C:calendar-query xmlns:D="DAV:" xmlns:C="urn:ietf:params:xml:ns:caldav">
  <C:filter>
    <C:comp-filter name="VCALENDAR">
      <C:comp-filter name="VEVENT">
        <C:prop-filter name="UID"><C:text-match>` + uid + `</C:text-match></C:prop-filter>
      </C:comp-filter>
    </C:comp-filter>
  </C:filter>
</C:calendar-query>`
}

// Unlike PROPFIND, a calendar-query doesn't have to ask for properties (RFC
// 4791 section 9.5). Each object matching the filter still gets a response
// (section 7.8), with only a 200 OK status.
func TestQueryWithoutProp(t *testing.T) {
	const calPath = "/user/calendars/a/"
	handler := &Handler{Backend: queryBackend{testBackend{
		calendars: []Calendar{{Path: calPath}},
		objectMap: map[string][]CalendarObject{calPath: {
			newMultiGetTestObject(calPath+"1.ics", "uid-1"),
			newMultiGetTestObject(calPath+"2.ics", "uid-2"),
		}},
	}}}

	for _, tc := range []struct {
		uid  string
		want []string
	}{
		{"uid-2", []string{calPath + "2.ics 200"}},
		{"uid-", []string{calPath + "1.ics 200", calPath + "2.ics 200"}},
		{"uid-3", []string{}},
	} {
		req := httptest.NewRequest("REPORT", calPath, strings.NewReader(calendarQueryWithoutProp(tc.uid)))
		req.Header.Set("Depth", "1")
		if got := statusResponses(t, handler, req); !reflect.DeepEqual(got, tc.want) {
			t.Errorf("query for %q: responses = %q, want %q", tc.uid, got, tc.want)
		}
	}
}

// Nor does a calendar-multiget (RFC 4791 section 9.10): each object found
// gets a response with only a 200 OK status, and each missing one its 404.
func TestMultiGetWithoutProp(t *testing.T) {
	const calPath = "/user/calendars/a/"
	objects := testBackend{
		calendars: []Calendar{{Path: calPath}},
		objectMap: map[string][]CalendarObject{calPath: {
			newMultiGetTestObject(calPath+"1.ics", "uid-1"),
			newMultiGetTestObject(calPath+"2.ics", "uid-2"),
		}},
	}
	multiget := func(paths ...string) string {
		body := `<?xml version="1.0" encoding="UTF-8"?>
<C:calendar-multiget xmlns:D="DAV:" xmlns:C="urn:ietf:params:xml:ns:caldav">`
		for _, p := range paths {
			body += "<D:href>" + p + "</D:href>"
		}
		return body + "</C:calendar-multiget>"
	}

	// The objects are fetched one by one with GetCalendarObject
	handler := &Handler{Backend: objects}
	req := httptest.NewRequest("REPORT", calPath, strings.NewReader(multiget(calPath+"2.ics", calPath+"1.ics")))
	want := []string{calPath + "2.ics 200", calPath + "1.ics 200"}
	if got := statusResponses(t, handler, req); !reflect.DeepEqual(got, want) {
		t.Errorf("GetCalendarObject: responses = %q, want %q", got, want)
	}

	// or all at once with GetCalendarObjects
	handler = &Handler{Backend: &multiGetBackend{testBackend: objects}}
	req = httptest.NewRequest("REPORT", calPath, strings.NewReader(multiget(calPath+"missing.ics", calPath+"1.ics")))
	want = []string{calPath + "missing.ics 404", calPath + "1.ics 200"}
	if got := statusResponses(t, handler, req); !reflect.DeepEqual(got, want) {
		t.Errorf("GetCalendarObjects: responses = %q, want %q", got, want)
	}
}
