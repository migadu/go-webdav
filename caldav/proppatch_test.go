package caldav

import (
	"encoding/xml"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/emersion/go-webdav/internal"
)

// macOS Calendar PROPPATCHes display metadata right after discovering a
// calendar, and drops the calendar if the request fails as a whole.
const propPatchCalendarRequest = `<?xml version="1.0" encoding="UTF-8"?>
<A:propertyupdate xmlns:A="DAV:" xmlns:D="http://apple.com/ns/ical/">
  <A:set>
    <A:prop>
      <A:displayname>Work</A:displayname>
      <D:calendar-color symbolic-color="orange">#FF9500</D:calendar-color>
    </A:prop>
  </A:set>
  <A:remove>
    <A:prop>
      <D:calendar-order/>
    </A:prop>
  </A:remove>
</A:propertyupdate>`

func TestPropPatchCalendar(t *testing.T) {
	cal := Calendar{Path: "/user/calendars/work/"}
	handler := Handler{Backend: testBackend{calendars: []Calendar{cal}}}

	req := httptest.NewRequest("PROPPATCH", cal.Path, strings.NewReader(propPatchCalendarRequest))
	req.Header.Set("Content-Type", "application/xml")
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	res := w.Result()
	defer res.Body.Close()
	if res.StatusCode != http.StatusMultiStatus {
		t.Fatalf("PROPPATCH returned %v, want 207", res.Status)
	}

	var ms internal.MultiStatus
	if err := xml.NewDecoder(res.Body).Decode(&ms); err != nil {
		t.Fatalf("failed to decode multistatus: %v", err)
	}
	if len(ms.Responses) != 1 {
		t.Fatalf("got %d responses, want 1", len(ms.Responses))
	}
	resp := ms.Responses[0]
	// A response carries either a status or propstats, never both (RFC 4918
	// section 14.24).
	if resp.Status != nil {
		t.Errorf("response has a top-level status %v alongside its propstats", resp.Status.Code)
	}

	want := map[xml.Name]bool{
		internal.DisplayNameName: true,
		{Space: "http://apple.com/ns/ical/", Local: "calendar-color"}: true,
		{Space: "http://apple.com/ns/ical/", Local: "calendar-order"}: true,
	}
	got := make(map[xml.Name]bool)
	for _, propstat := range resp.PropStats {
		if propstat.Status.Code/100 == 2 {
			t.Errorf("propstat reports success (%v) for properties the server cannot store", propstat.Status.Code)
		}
		for _, raw := range propstat.Prop.Raw {
			name, _ := raw.XMLName()
			got[name] = true
		}
	}
	for name := range want {
		if !got[name] {
			t.Errorf("property %v missing from the response", name)
		}
	}
	for name := range got {
		if !want[name] {
			t.Errorf("unexpected property %v in the response", name)
		}
	}
}

func TestPropPatchCalendarNotFound(t *testing.T) {
	handler := Handler{Backend: testBackend{}}

	req := httptest.NewRequest("PROPPATCH", "/user/calendars/missing/", strings.NewReader(propPatchCalendarRequest))
	req.Header.Set("Content-Type", "application/xml")
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	if w.Code == http.StatusMultiStatus {
		t.Fatalf("PROPPATCH on a missing calendar returned 207, want an error")
	}
}
