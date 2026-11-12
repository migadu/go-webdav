package caldav

import (
	"context"
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

// A PROPFIND, calendar-query or calendar-multiget request has a propname, an
// allprop or a prop element, but not several (RFC 4918 section 14.20, RFC
// 4791 sections 9.5 and 9.10): other combinations are refused before the
// backend is called.
func TestExclusivePropFindElements(t *testing.T) {
	const calPath = "/user/calendars/a/"
	for _, elts := range []string{
		`<D:allprop/><D:prop><D:getetag/></D:prop>`,
		`<D:propname/><D:allprop/>`,
		`<D:propname/><D:prop><D:getetag/></D:prop>`,
	} {
		for _, body := range []string{
			`<D:propfind xmlns:D="DAV:">` + elts + `</D:propfind>`,
			`<C:calendar-query xmlns:D="DAV:" xmlns:C="urn:ietf:params:xml:ns:caldav">` + elts +
				`<C:filter><C:comp-filter name="VCALENDAR"/></C:filter></C:calendar-query>`,
			`<C:calendar-multiget xmlns:D="DAV:" xmlns:C="urn:ietf:params:xml:ns:caldav">` + elts +
				`<D:href>` + calPath + `1.ics</D:href></C:calendar-multiget>`,
		} {
			method := "REPORT"
			if strings.HasPrefix(body, "<D:propfind") {
				method = "PROPFIND"
			}
			req := httptest.NewRequest(method, calPath, strings.NewReader(body))
			req.Header.Set("Content-Type", "application/xml")
			req.Header.Set("Depth", "1")
			if w := serveNoCall(t, req); w.Code != http.StatusBadRequest {
				t.Errorf("%v %v returned %v, want 400: %s", method, body, w.Code, w.Body.String())
			}
		}
	}
}

// describeResponses serves req with handler, and describes each response of
// the 207 Multi-Status it gets: its hrefs, followed by its status if any, and
// by the status and number of properties of each of its propstats.
func describeResponses(t *testing.T, handler http.Handler, req *http.Request) []string {
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
		for _, propstat := range resp.PropStats {
			s += fmt.Sprintf(" propstat %v with %v properties", propstat.Status.Code, len(propstat.Prop.Raw))
		}
		l = append(l, s)
	}
	return l
}

// emptyPropBackend is a syncTestBackend whose calendar-query REPORTs match
// every object.
type emptyPropBackend struct {
	*syncTestBackend
}

func (b emptyPropBackend) QueryCalendarObjects(ctx context.Context, path string, query *CalendarQuery) ([]CalendarObject, error) {
	return b.objectMap[path], nil
}

// An empty prop element (RFC 4918 section 14.18: <!ELEMENT prop ANY >) asks
// for no property. Each resource still gets a response, which must hold
// either a status or at least one propstat (section 14.24): a 200 OK status,
// as for a REPORT without a prop element. In a sync-collection REPORT, the
// response for a changed member must not have a status but a propstat (RFC
// 6578 section 3.2): it gets a 200 OK propstat with an empty prop element.
func TestEmptyProp(t *testing.T) {
	const (
		one  = syncCalendarPath + "/one.ics"
		two  = syncCalendarPath + "/two.ics"
		gone = syncCalendarPath + "/gone.ics"
	)
	handler := &Handler{Backend: emptyPropBackend{newSyncTestBackend()}}
	for _, tc := range []struct {
		method, path, depth, body string
		want                      []string
	}{
		{"PROPFIND", "/", "0", `<D:propfind xmlns:D="DAV:"><D:prop/></D:propfind>`, []string{"/ 200"}},
		{"PROPFIND", "/user/", "0", `<D:propfind xmlns:D="DAV:"><D:prop/></D:propfind>`, []string{"/user/ 200"}},
		{"PROPFIND", "/user/calendars/", "0", `<D:propfind xmlns:D="DAV:"><D:prop/></D:propfind>`, []string{"/user/calendars/ 200"}},
		{"PROPFIND", syncCalendarPath, "1", `<D:propfind xmlns:D="DAV:"><D:prop/></D:propfind>`, []string{syncCalendarPath + " 200", one + " 200", two + " 200"}},
		{"PROPFIND", one, "0", `<D:propfind xmlns:D="DAV:"><D:prop/></D:propfind>`, []string{one + " 200"}},
		{
			"REPORT", syncCalendarPath, "1",
			`<C:calendar-query xmlns:D="DAV:" xmlns:C="urn:ietf:params:xml:ns:caldav"><D:prop/><C:filter><C:comp-filter name="VCALENDAR"/></C:filter></C:calendar-query>`,
			[]string{one + " 200", two + " 200"},
		},
		{
			"REPORT", syncCalendarPath, "1",
			`<C:calendar-multiget xmlns:D="DAV:" xmlns:C="urn:ietf:params:xml:ns:caldav"><D:prop/><D:href>` + two + `</D:href><D:href>` + one + `</D:href></C:calendar-multiget>`,
			[]string{two + " 200", one + " 200"},
		},
		{
			"REPORT", syncCalendarPath, "0",
			`<D:sync-collection xmlns:D="DAV:"><D:sync-token>` + syncToken1 + `</D:sync-token><D:sync-level>1</D:sync-level><D:prop/></D:sync-collection>`,
			[]string{two + " propstat 200 with 0 properties", gone + " 404"},
		},
	} {
		req := httptest.NewRequest(tc.method, tc.path, strings.NewReader(tc.body))
		req.Header.Set("Depth", tc.depth)
		if got := describeResponses(t, handler, req); !reflect.DeepEqual(got, tc.want) {
			t.Errorf("%v %v %v: responses = %q, want %q", tc.method, tc.path, tc.body, got, tc.want)
		}
	}
}
