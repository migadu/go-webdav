package caldav

import (
	"context"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/emersion/go-ical"
)

// dataRequestBackend records the calendar data requests it is given.
type dataRequestBackend struct {
	testBackend
	query *CalendarQuery
	get   []CalendarCompRequest
}

func (b *dataRequestBackend) QueryCalendarObjects(ctx context.Context, path string, query *CalendarQuery) ([]CalendarObject, error) {
	b.query = query
	return nil, nil
}

func (b *dataRequestBackend) GetCalendarObject(ctx context.Context, path string, req *CalendarCompRequest) (*CalendarObject, error) {
	b.get = append(b.get, *req)
	return b.testBackend.GetCalendarObject(ctx, path, req)
}

const calendarDataRequest = `<C:calendar-data>
      <C:comp name="VCALENDAR">
        <C:prop name="VERSION"/>
        <C:comp name="VEVENT">
          <C:prop name="SUMMARY"/>
          <C:prop name="UID"/>
        </C:comp>
      </C:comp>
      <C:expand start="20060103T000000Z" end="20060105T000000Z"/>
    </C:calendar-data>`

var wantCalendarDataRequest = CalendarCompRequest{
	Name:  "VCALENDAR",
	Props: []string{"VERSION"},
	Comps: []CalendarCompRequest{{
		Name:  "VEVENT",
		Props: []string{"SUMMARY", "UID"},
	}},
	Expand: &CalendarExpandRequest{
		Start: time.Date(2006, 1, 3, 0, 0, 0, 0, time.UTC),
		End:   time.Date(2006, 1, 5, 0, 0, 0, 0, time.UTC),
	},
}

// The calendar-data element of a REPORT tells the backend which components
// and properties to return, and whether to expand recurrences (RFC 4791
// section 9.6).
func TestReportCalendarDataRequest(t *testing.T) {
	const calPath = "/user/calendars/a/"
	backend := &dataRequestBackend{testBackend: testBackend{
		calendars: []Calendar{{Path: calPath}},
		objectMap: map[string][]CalendarObject{calPath: {newDataRequestTestObject(calPath + "1.ics")}},
	}}
	handler := Handler{Backend: backend}

	for _, body := range []string{
		`<?xml version="1.0" encoding="utf-8" ?>
<C:calendar-query xmlns:D="DAV:" xmlns:C="urn:ietf:params:xml:ns:caldav">
  <D:prop>
    <D:getetag/>
    ` + calendarDataRequest + `
  </D:prop>
  <C:filter><C:comp-filter name="VCALENDAR"/></C:filter>
</C:calendar-query>`,
		`<?xml version="1.0" encoding="utf-8" ?>
<C:calendar-multiget xmlns:D="DAV:" xmlns:C="urn:ietf:params:xml:ns:caldav">
  <D:prop>
    <D:getetag/>
    ` + calendarDataRequest + `
  </D:prop>
  <D:href>` + calPath + `1.ics</D:href>
</C:calendar-multiget>`,
	} {
		req := httptest.NewRequest("REPORT", calPath, strings.NewReader(body))
		req.Header.Set("Content-Type", "application/xml")
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, req)
		if w.Code != http.StatusMultiStatus {
			t.Fatalf("REPORT returned %v: %s", w.Code, w.Body.String())
		}
	}

	if backend.query == nil {
		t.Fatalf("QueryCalendarObjects not called")
	}
	if got := backend.query.CompRequest; !reflect.DeepEqual(got, wantCalendarDataRequest) {
		t.Errorf("calendar-query data request = %+v, want %+v", got, wantCalendarDataRequest)
	}
	if len(backend.get) != 1 {
		t.Fatalf("GetCalendarObject called %d times, want 1", len(backend.get))
	}
	if got := backend.get[0]; !reflect.DeepEqual(got, wantCalendarDataRequest) {
		t.Errorf("calendar-multiget data request = %+v, want %+v", got, wantCalendarDataRequest)
	}
}

func newDataRequestTestObject(path string) CalendarObject {
	event := ical.NewEvent()
	event.Props.SetText(ical.PropUID, "uid-1")
	event.Props.SetDateTime(ical.PropDateTimeStamp, time.Now())
	cal := ical.NewCalendar()
	cal.Props.SetText(ical.PropVersion, "2.0")
	cal.Props.SetText(ical.PropProductID, "-//Test//Test//EN")
	cal.Children = []*ical.Component{event.Component}
	return CalendarObject{Path: path, Data: cal}
}
