package caldav

import (
	"context"
	"encoding/xml"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/emersion/go-ical"
	"github.com/emersion/go-webdav/internal"
)

// multiGetBackend fetches the objects of a calendar-multiget at once.
type multiGetBackend struct {
	testBackend
	calls [][]string
}

func (b *multiGetBackend) GetCalendarObject(ctx context.Context, path string, req *CalendarCompRequest) (*CalendarObject, error) {
	panic("GetCalendarObject called for a calendar-multiget")
}

func (b *multiGetBackend) GetCalendarObjects(ctx context.Context, paths []string, req *CalendarCompRequest) ([]CalendarObject, error) {
	b.calls = append(b.calls, paths)
	var l []CalendarObject
	for _, objs := range b.objectMap {
		for _, obj := range objs {
			for _, p := range paths {
				if obj.Path == p {
					l = append(l, obj)
				}
			}
		}
	}
	return l, nil
}

func newMultiGetTestObject(path, uid string) CalendarObject {
	event := ical.NewEvent()
	event.Props.SetText(ical.PropUID, uid)
	event.Props.SetDateTime(ical.PropDateTimeStamp, time.Now())
	cal := ical.NewCalendar()
	cal.Props.SetText(ical.PropVersion, "2.0")
	cal.Props.SetText(ical.PropProductID, "-//Test//Test//EN")
	cal.Children = []*ical.Component{event.Component}
	return CalendarObject{Path: path, ETag: uid, Data: cal}
}

func TestMultiGetBackend(t *testing.T) {
	const calPath = "/user/calendars/a/"
	backend := &multiGetBackend{testBackend: testBackend{
		calendars: []Calendar{{Path: calPath}},
		objectMap: map[string][]CalendarObject{calPath: {
			newMultiGetTestObject(calPath+"1.ics", "uid-1"),
			newMultiGetTestObject(calPath+"2.ics", "uid-2"),
		}},
	}}
	handler := Handler{Backend: backend}

	body := `<?xml version="1.0" encoding="UTF-8"?>
<C:calendar-multiget xmlns:D="DAV:" xmlns:C="urn:ietf:params:xml:ns:caldav">
  <D:prop><D:getetag/><C:calendar-data/></D:prop>
  <D:href>` + calPath + `2.ics</D:href>
  <D:href>` + calPath + `missing.ics</D:href>
  <D:href>` + calPath + `1.ics</D:href>
</C:calendar-multiget>`
	req := httptest.NewRequest("REPORT", calPath, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/xml")
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	if w.Code != http.StatusMultiStatus {
		t.Fatalf("REPORT returned %v, want 207: %s", w.Code, w.Body.String())
	}
	wantCalls := [][]string{{calPath + "2.ics", calPath + "missing.ics", calPath + "1.ics"}}
	if !reflect.DeepEqual(backend.calls, wantCalls) {
		t.Errorf("GetCalendarObjects calls = %v, want %v", backend.calls, wantCalls)
	}

	var ms internal.MultiStatus
	if err := xml.NewDecoder(w.Body).Decode(&ms); err != nil {
		t.Fatal(err)
	}
	if len(ms.Responses) != 3 {
		t.Fatalf("got %d responses, want 3", len(ms.Responses))
	}
	// Responses come in the order of the request
	for i, want := range []struct {
		path string
		code int
	}{
		{calPath + "2.ics", http.StatusOK},
		{calPath + "missing.ics", http.StatusNotFound},
		{calPath + "1.ics", http.StatusOK},
	} {
		resp := ms.Responses[i]
		if p := resp.Hrefs[0].Path; p != want.path {
			t.Errorf("response %d is for %s, want %s", i, p, want.path)
		}
		code := http.StatusOK
		if resp.Status != nil {
			code = resp.Status.Code
		}
		if code != want.code {
			t.Errorf("response %d status = %d, want %d", i, code, want.code)
		}
	}
}
