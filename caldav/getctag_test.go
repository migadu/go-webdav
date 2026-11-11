package caldav

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// propFindCalendarCTag issues the Depth: 0 PROPFIND clients use to decide
// whether a calendar needs enumerating, and returns the response body.
func propFindCalendarCTag(t *testing.T, ctag string) string {
	t.Helper()

	cal := Calendar{Path: "/user/calendars/work/", CTag: ctag}
	handler := Handler{Backend: testBackend{calendars: []Calendar{cal}}}

	body := `<?xml version="1.0" encoding="UTF-8"?>
<A:propfind xmlns:A="DAV:" xmlns:C="http://calendarserver.org/ns/">
  <A:prop><A:resourcetype/><C:getctag/></A:prop>
</A:propfind>`
	req := httptest.NewRequest("PROPFIND", cal.Path, strings.NewReader(body))
	req.Header.Set("Depth", "0")
	req.Header.Set("Content-Type", "text/xml; charset=utf-8")
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	res := w.Result()
	defer res.Body.Close()
	if res.StatusCode != http.StatusMultiStatus {
		t.Fatalf("PROPFIND returned %d, expected 207", res.StatusCode)
	}
	out, err := io.ReadAll(res.Body)
	if err != nil {
		t.Fatalf("error reading body: %s", err)
	}
	return string(out)
}

func TestCalendarCTagServedWhenSet(t *testing.T) {
	body := propFindCalendarCTag(t, "abc123")

	if !strings.Contains(body, `<getctag xmlns="http://calendarserver.org/ns/">abc123</getctag>`) {
		t.Errorf("CS:getctag not served with the backend's value:\n%s", body)
	}
	if strings.Contains(body, "404") {
		t.Errorf("requested properties should all resolve, got a 404 propstat:\n%s", body)
	}
}

func TestCalendarCTagNotFoundWhenUnset(t *testing.T) {
	body := propFindCalendarCTag(t, "")

	if !strings.Contains(body, "404 Not Found") {
		t.Errorf("unset CTag should be reported in a 404 propstat:\n%s", body)
	}
}
