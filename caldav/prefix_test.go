package caldav

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestResourceTypeAtPathWithPrefix(t *testing.T) {
	b := backend{
		Backend: testBackend{},
		Prefix:  "/dav",
	}
	ctx := context.Background()

	for _, p := range []string{
		"/",
		"/other",
		"/user/",
		"/user/calendars/work/",
		"/user/calendars/work/event.ics",
		"/dav2",
		"/dav2/user/calendars/work",
		"/dav2/user/calendars/work/event.ics",
	} {
		got := b.resourceTypeAtPath(ctx, p)
		if got >= resourceTypeRoot || got == resourceTypeScheduleInbox {
			t.Errorf("resourceTypeAtPath(%q) = %v, want out-of-prefix sentinel", p, got)
		}
	}

	inPrefixCases := []struct {
		path string
		want resourceType
	}{
		{"/dav", resourceTypeRoot},
		{"/dav/", resourceTypeRoot},
		{"/dav/user/", resourceTypeUserPrincipal},
		{"/dav/user/calendars/", resourceTypeCalendarHomeSet},
		{"/dav/user/calendars/work/", resourceTypeCalendar},
		{"/dav/user/calendars/work/event.ics", resourceTypeCalendarObject},
	}
	for _, tc := range inPrefixCases {
		if got := b.resourceTypeAtPath(ctx, tc.path); got != tc.want {
			t.Errorf("resourceTypeAtPath(%q) = %v, want %v", tc.path, got, tc.want)
		}
	}
}

type prefixSpyCalBackend struct {
	testBackend
	calledPaths []string
}

func (b *prefixSpyCalBackend) CurrentUserPrincipal(ctx context.Context) (string, error) {
	return "/dav/user/", nil
}

func (b *prefixSpyCalBackend) CalendarHomeSetPath(ctx context.Context) (string, error) {
	return "/dav/user/calendars/", nil
}

func (b *prefixSpyCalBackend) GetCalendarObject(ctx context.Context, path string, req *CalendarCompRequest) (*CalendarObject, error) {
	b.calledPaths = append(b.calledPaths, path)
	return nil, &httpErrorNotFound{}
}

type httpErrorNotFound struct{}

func (e *httpErrorNotFound) Error() string { return "not found" }

func TestHandlerRejectsOutOfPrefixRequests(t *testing.T) {
	spy := &prefixSpyCalBackend{
		testBackend: testBackend{
			calendars: []Calendar{{Path: "/dav/user/calendars/work/", Name: "Work"}},
		},
	}
	h := &Handler{Backend: spy, Prefix: "/dav/"}

	outOfPrefix := []struct {
		method string
		target string
		body   string
	}{
		{"PROPFIND", "/", ""},
		{"PROPFIND", "/user/", ""},
		{"PROPFIND", "/dav2/user/calendars/work/", ""},
		{"OPTIONS", "/", ""},
		{"OPTIONS", "/dav2/user/calendars/work/event.ics", ""},
		{"GET", "/user/calendars/work/event.ics", ""},
		{"PUT", "/user/calendars/work/event.ics", "BEGIN:VCALENDAR\r\nVERSION:2.0\r\nEND:VCALENDAR\r\n"},
		{"DELETE", "/user/calendars/work/event.ics", ""},
		{"MKCOL", "/user/calendars/new/", ""},
		{"MKCALENDAR", "/user/calendars/new/", ""},
		{"MKCALENDAR", "/dav2/user/calendars/new/", ""},
		{"REPORT", "/user/calendars/work/", `<C:calendar-query xmlns:D="DAV:" xmlns:C="urn:ietf:params:xml:ns:caldav"><D:prop><D:getetag/></D:prop><C:filter><C:comp-filter name="VCALENDAR"/></C:filter></C:calendar-query>`},
	}

	for _, tc := range outOfPrefix {
		req := httptest.NewRequest(tc.method, tc.target, strings.NewReader(tc.body))
		if tc.body != "" {
			if tc.method == "PUT" {
				req.Header.Set("Content-Type", "text/calendar")
			} else {
				req.Header.Set("Content-Type", "application/xml")
			}
		}
		w := httptest.NewRecorder()
		h.ServeHTTP(w, req)
		if w.Code != http.StatusNotFound {
			t.Errorf("%s %s: got status %d, want 404", tc.method, tc.target, w.Code)
		}
	}

	// Multiget inside prefix with an out-of-prefix href must not pass out-of-prefix path to backend.
	spy.calledPaths = nil
	multigetBody := `<C:calendar-multiget xmlns:D="DAV:" xmlns:C="urn:ietf:params:xml:ns:caldav"><D:prop><D:getetag/></D:prop><D:href>/other/event.ics</D:href></C:calendar-multiget>`
	req := httptest.NewRequest("REPORT", "/dav/user/calendars/work/", strings.NewReader(multigetBody))
	req.Header.Set("Content-Type", "application/xml")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	if w.Code != http.StatusMultiStatus {
		t.Fatalf("multiget REPORT got %d, want 207: %s", w.Code, w.Body.String())
	}
	if len(spy.calledPaths) > 0 {
		t.Errorf("backend GetCalendarObject called with out-of-prefix paths: %v", spy.calledPaths)
	}
	if !strings.Contains(w.Body.String(), "404") {
		t.Errorf("expected 404 in multiget response for out-of-prefix href, got:\n%s", w.Body.String())
	}

	// /.well-known/caldav must still redirect even when Prefix is configured.
	wkReq := httptest.NewRequest("GET", "/.well-known/caldav", nil)
	wkW := httptest.NewRecorder()
	h.ServeHTTP(wkW, wkReq)
	if wkW.Code != http.StatusPermanentRedirect {
		t.Errorf("GET /.well-known/caldav: got %d, want 308", wkW.Code)
	}

	// In-prefix root PROPFIND must succeed with 207.
	inReq := httptest.NewRequest("PROPFIND", "/dav/", nil)
	inReq.Header.Set("Depth", "0")
	inW := httptest.NewRecorder()
	h.ServeHTTP(inW, inReq)
	if inW.Code != http.StatusMultiStatus {
		t.Errorf("PROPFIND /dav/: got %d, want 207: %s", inW.Code, inW.Body.String())
	}
}
