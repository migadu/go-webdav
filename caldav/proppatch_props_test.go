package caldav

import (
	"encoding/xml"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"

	"github.com/emersion/go-ical"
	"github.com/migadu/go-webdav/internal"
)

func TestPropPatchPropsAndOptions(t *testing.T) {
	const calPath = "/user/calendars/work/"

	t.Run("omits empty calendar-description in PROPFIND", func(t *testing.T) {
		h := &Handler{Backend: testBackend{calendars: []Calendar{{Path: calPath, Name: "Work"}}}}
		req := httptest.NewRequest("PROPFIND", calPath, strings.NewReader(`<?xml version="1.0" encoding="utf-8"?>
<D:propfind xmlns:D="DAV:" xmlns:C="urn:ietf:params:xml:ns:caldav">
  <D:prop>
    <D:displayname/>
    <C:calendar-description/>
  </D:prop>
</D:propfind>`))
		req.Header.Set("Content-Type", "application/xml")
		req.Header.Set("Depth", "0")
		w := httptest.NewRecorder()
		h.ServeHTTP(w, req)

		var ms internal.MultiStatus
		if err := xml.NewDecoder(w.Body).Decode(&ms); err != nil {
			t.Fatalf("Decode MultiStatus: %v", err)
		}
		codes := make(map[xml.Name]int)
		for _, ps := range ms.Responses[0].PropStats {
			for _, raw := range ps.Prop.Raw {
				name, _ := raw.XMLName()
				codes[name] = ps.Status.Code
			}
		}
		if got := codes[calendarDescriptionName]; got != http.StatusNotFound {
			t.Errorf("empty calendar-description status = %d, want 404", got)
		}
	})

	t.Run("OPTIONS advertises PROPPATCH when CalendarUpdater is implemented", func(t *testing.T) {
		backend := &updateBackend{testBackend: testBackend{calendars: []Calendar{{Path: calPath}}}}
		h := &Handler{Backend: backend}

		req := httptest.NewRequest(http.MethodOptions, calPath, nil)
		w := httptest.NewRecorder()
		h.ServeHTTP(w, req)

		allow := w.Header().Get("Allow")
		if !strings.Contains(allow, "PROPPATCH") {
			t.Errorf("Allow header = %q, want it to contain PROPPATCH", allow)
		}
	})

	t.Run("PROPPATCH sets and removes calendar-color and calendar-timezone", func(t *testing.T) {
		backend := &updateBackend{testBackend: testBackend{calendars: []Calendar{{Path: calPath}}}}
		h := &Handler{Backend: backend}

		codes := propPatchCalendar(t, h, calPath, `<?xml version="1.0" encoding="UTF-8"?>
<D:propertyupdate xmlns:D="DAV:" xmlns:C="urn:ietf:params:xml:ns:caldav" xmlns:I="http://apple.com/ns/ical/">
  <D:set><D:prop>
    <I:calendar-color> #FF5733 </I:calendar-color>
    <C:calendar-timezone><![CDATA[`+calendarTimezoneData+`]]></C:calendar-timezone>
  </D:prop></D:set>
</D:propertyupdate>`)

		wantCodes := map[xml.Name]int{
			calendarColorName:    http.StatusOK,
			calendarTimezoneName: http.StatusOK,
		}
		if !reflect.DeepEqual(codes, wantCodes) {
			t.Fatalf("propstat codes = %v, want %v", codes, wantCodes)
		}
		if len(backend.updates) != 1 {
			t.Fatalf("got %d updates, want 1", len(backend.updates))
		}
		got := backend.updates[0]
		if got.Color == nil || *got.Color != "#FF5733" {
			t.Errorf("got Color = %v, want #FF5733", got.Color)
		}
		if got.Timezone == nil || got.RemoveTimezone {
			t.Fatalf("got Timezone = %v, RemoveTimezone = %v; want non-nil Timezone and RemoveTimezone=false", got.Timezone, got.RemoveTimezone)
		}
		if tzid := got.Timezone.Children[0].Props.Get(ical.PropTimezoneID); tzid == nil || tzid.Value != "US-Eastern" {
			t.Errorf("got TZID = %v, want US-Eastern", tzid)
		}

		// Remove both properties in a second PROPPATCH.
		codes = propPatchCalendar(t, h, calPath, `<?xml version="1.0" encoding="UTF-8"?>
<D:propertyupdate xmlns:D="DAV:" xmlns:C="urn:ietf:params:xml:ns:caldav" xmlns:I="http://apple.com/ns/ical/">
  <D:remove><D:prop>
    <I:calendar-color/>
    <C:calendar-timezone/>
  </D:prop></D:remove>
</D:propertyupdate>`)
		if !reflect.DeepEqual(codes, wantCodes) {
			t.Fatalf("remove propstat codes = %v, want %v", codes, wantCodes)
		}
		if len(backend.updates) != 2 {
			t.Fatalf("got %d updates, want 2", len(backend.updates))
		}
		gotRemove := backend.updates[1]
		if gotRemove.Color == nil || *gotRemove.Color != "" {
			t.Errorf("got Color on remove = %v, want empty string", gotRemove.Color)
		}
		if gotRemove.Timezone != nil || !gotRemove.RemoveTimezone {
			t.Errorf("got Timezone=%v, RemoveTimezone=%v on remove; want nil and true", gotRemove.Timezone, gotRemove.RemoveTimezone)
		}
	})

	t.Run("PROPPATCH rejects invalid calendar-timezone with valid-calendar-data precondition", func(t *testing.T) {
		backend := &updateBackend{testBackend: testBackend{calendars: []Calendar{{Path: calPath}}}}
		h := &Handler{Backend: backend}

		req := httptest.NewRequest("PROPPATCH", calPath, strings.NewReader(`<?xml version="1.0" encoding="UTF-8"?>
<D:propertyupdate xmlns:D="DAV:" xmlns:C="urn:ietf:params:xml:ns:caldav">
  <D:set><D:prop>
    <C:calendar-timezone>NOT-A-VCALENDAR</C:calendar-timezone>
  </D:prop></D:set>
</D:propertyupdate>`))
		req.Header.Set("Content-Type", "application/xml")
		w := httptest.NewRecorder()
		h.ServeHTTP(w, req)

		if w.Code != http.StatusMultiStatus {
			t.Fatalf("status = %d, want 207", w.Code)
		}
		body := w.Body.String()
		if !strings.Contains(body, "409 Conflict") || !strings.Contains(body, "valid-calendar-data") {
			t.Errorf("expected 409 Conflict with valid-calendar-data precondition in propstat:\n%s", body)
		}
		if len(backend.updates) != 0 {
			t.Errorf("expected UpdateCalendar not to be called on invalid timezone, got %+v", backend.updates)
		}
	})
}
