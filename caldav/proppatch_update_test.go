package caldav

import (
	"context"
	"encoding/xml"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"

	"github.com/migadu/go-webdav/internal"
)

// updateBackend records the calendar updates it is asked for.
type updateBackend struct {
	testBackend
	updates []CalendarUpdate
}

func (b *updateBackend) UpdateCalendar(ctx context.Context, path string, update *CalendarUpdate) error {
	b.updates = append(b.updates, *update)
	return nil
}

func propPatchCalendar(t *testing.T, h *Handler, path, body string) map[xml.Name]int {
	t.Helper()

	req := httptest.NewRequest("PROPPATCH", path, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/xml")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)

	if w.Code != http.StatusMultiStatus {
		t.Fatalf("PROPPATCH returned %v, want 207: %s", w.Code, w.Body.String())
	}
	var ms internal.MultiStatus
	if err := xml.NewDecoder(w.Body).Decode(&ms); err != nil {
		t.Fatal(err)
	}
	codes := make(map[xml.Name]int)
	for _, propstat := range ms.Responses[0].PropStats {
		for _, raw := range propstat.Prop.Raw {
			name, _ := raw.XMLName()
			codes[name] = propstat.Status.Code
		}
	}
	return codes
}

// RFC 4791 section 5.3.1: clients may set a calendar's displayname with a
// PROPPATCH, e.g. right after creating it.
func TestPropPatchUpdateCalendar(t *testing.T) {
	const calPath = "/user/calendars/work/"
	backend := &updateBackend{testBackend: testBackend{calendars: []Calendar{{Path: calPath}}}}
	h := &Handler{Backend: backend}

	codes := propPatchCalendar(t, h, calPath, `<?xml version="1.0" encoding="UTF-8"?>
<D:propertyupdate xmlns:D="DAV:" xmlns:C="urn:ietf:params:xml:ns:caldav">
  <D:set><D:prop>
    <D:displayname>Work</D:displayname>
    <C:calendar-description>Meetings and such</C:calendar-description>
  </D:prop></D:set>
</D:propertyupdate>`)

	want := map[xml.Name]int{
		internal.DisplayNameName: http.StatusOK,
		calendarDescriptionName:  http.StatusOK,
	}
	if !reflect.DeepEqual(codes, want) {
		t.Errorf("propstat codes = %v, want %v", codes, want)
	}
	name, desc := "Work", "Meetings and such"
	wantUpdates := []CalendarUpdate{{Name: &name, Description: &desc}}
	if !reflect.DeepEqual(backend.updates, wantUpdates) {
		t.Errorf("updates = %+v, want %+v", backend.updates, wantUpdates)
	}

	// A protected property makes the whole request fail
	codes = propPatchCalendar(t, h, calPath, `<?xml version="1.0" encoding="UTF-8"?>
<D:propertyupdate xmlns:D="DAV:" xmlns:C="urn:ietf:params:xml:ns:caldav">
  <D:remove><D:prop><D:displayname/></D:prop></D:remove>
  <D:set><D:prop><C:supported-calendar-component-set><C:comp name="VTODO"/></C:supported-calendar-component-set></D:prop></D:set>
</D:propertyupdate>`)
	want = map[xml.Name]int{
		internal.DisplayNameName:          http.StatusFailedDependency,
		supportedCalendarComponentSetName: http.StatusForbidden,
	}
	if !reflect.DeepEqual(codes, want) {
		t.Errorf("propstat codes = %v, want %v", codes, want)
	}
	if len(backend.updates) != 1 {
		t.Errorf("updates = %+v, want only the first one", backend.updates)
	}
}
