package caldav

import (
	"context"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"time"
)

const limitSetsCalPath = "/user/calendars/a/"

// calendarDataReports returns a calendar-query and a calendar-multiget REPORT
// on limitSetsCalPath which ask for calendarData.
func calendarDataReports(calendarData string) []struct{ name, body string } {
	return []struct{ name, body string }{{
		name: "calendar-query",
		body: `<?xml version="1.0" encoding="utf-8" ?>
<C:calendar-query xmlns:D="DAV:" xmlns:C="urn:ietf:params:xml:ns:caldav">
  <D:prop>
    <D:getetag/>
    ` + calendarData + `
  </D:prop>
  <C:filter><C:comp-filter name="VCALENDAR"/></C:filter>
</C:calendar-query>`,
	}, {
		name: "calendar-multiget",
		body: `<?xml version="1.0" encoding="utf-8" ?>
<C:calendar-multiget xmlns:D="DAV:" xmlns:C="urn:ietf:params:xml:ns:caldav">
  <D:prop>
    <D:getetag/>
    ` + calendarData + `
  </D:prop>
  <D:href>` + limitSetsCalPath + `1.ics</D:href>
</C:calendar-multiget>`,
	}}
}

// serveCalendarDataReport serves a REPORT with body to a backend recording
// the calendar data requests it is given.
func serveCalendarDataReport(body string) (*httptest.ResponseRecorder, *dataRequestBackend) {
	backend := &dataRequestBackend{testBackend: testBackend{
		calendars: []Calendar{{Path: limitSetsCalPath}},
		objectMap: map[string][]CalendarObject{
			limitSetsCalPath: {newDataRequestTestObject(limitSetsCalPath + "1.ics")},
		},
	}}
	handler := Handler{Backend: backend}

	req := httptest.NewRequest("REPORT", limitSetsCalPath, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/xml")
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)
	return w, backend
}

// RFC 4791 section 9.6 allows calendar-data to hold either expand or
// limit-recurrence-set, not both. Sections 9.6.5 to 9.6.7 require the start
// and end attributes of expand, limit-recurrence-set and limit-freebusy-set,
// as dates with UTC time, the end after the start.
func TestReportCalendarDataLimitsBadRequest(t *testing.T) {
	for _, tc := range []struct{ name, calendarData string }{{
		name: "expand and limit-recurrence-set",
		calendarData: `<C:calendar-data>
      <C:expand start="20060103T000000Z" end="20060105T000000Z"/>
      <C:limit-recurrence-set start="20060103T000000Z" end="20060105T000000Z"/>
    </C:calendar-data>`,
	}, {
		name:         "limit-recurrence-set without start",
		calendarData: `<C:calendar-data><C:limit-recurrence-set end="20060105T000000Z"/></C:calendar-data>`,
	}, {
		name:         "limit-recurrence-set without end",
		calendarData: `<C:calendar-data><C:limit-recurrence-set start="20060103T000000Z"/></C:calendar-data>`,
	}, {
		name:         "limit-freebusy-set without start",
		calendarData: `<C:calendar-data><C:limit-freebusy-set end="20060103T000000Z"/></C:calendar-data>`,
	}, {
		name:         "limit-freebusy-set without end",
		calendarData: `<C:calendar-data><C:limit-freebusy-set start="20060102T000000Z"/></C:calendar-data>`,
	}, {
		name:         "expand without start",
		calendarData: `<C:calendar-data><C:expand end="20060105T000000Z"/></C:calendar-data>`,
	}, {
		name:         "limit-recurrence-set ending at its start",
		calendarData: `<C:calendar-data><C:limit-recurrence-set start="20060103T000000Z" end="20060103T000000Z"/></C:calendar-data>`,
	}, {
		name:         "limit-freebusy-set with a floating start",
		calendarData: `<C:calendar-data><C:limit-freebusy-set start="20060102T000000" end="20060103T000000Z"/></C:calendar-data>`,
	}, {
		// At most one of each (RFC 4791 section 9.6), rather than the
		// attributes of the last one overwriting those of the first
		name: "two expand",
		calendarData: `<C:calendar-data>
      <C:expand start="20060103T000000Z" end="20060105T000000Z"/>
      <C:expand start="20060104T000000Z" end="20060106T000000Z"/>
    </C:calendar-data>`,
	}, {
		name: "two limit-recurrence-set",
		calendarData: `<C:calendar-data>
      <C:limit-recurrence-set start="20060103T000000Z" end="20060105T000000Z"/>
      <C:limit-recurrence-set start="20060104T000000Z" end="20060106T000000Z"/>
    </C:calendar-data>`,
	}, {
		name: "two limit-freebusy-set",
		calendarData: `<C:calendar-data>
      <C:limit-freebusy-set start="20060102T000000Z" end="20060103T000000Z"/>
      <C:limit-freebusy-set end="20060104T000000Z"/>
    </C:calendar-data>`,
	}} {
		for _, report := range calendarDataReports(tc.calendarData) {
			t.Run(tc.name+"/"+report.name, func(t *testing.T) {
				w, backend := serveCalendarDataReport(report.body)
				if w.Code != http.StatusBadRequest {
					t.Errorf("REPORT returned %v, want %v: %s", w.Code, http.StatusBadRequest, w.Body.String())
				}
				if backend.query != nil || len(backend.get) > 0 {
					t.Errorf("the backend was asked for calendar objects")
				}
			})
		}
	}
}

// jan2006 returns the given day of January 2006 at midnight UTC, as in the
// examples of RFC 4791.
func jan2006(day int) time.Time {
	return time.Date(2006, time.January, day, 0, 0, 0, 0, time.UTC)
}

// The limit-recurrence-set and limit-freebusy-set elements of calendar-data
// reach the backend, which applies them (RFC 4791 sections 9.6.6 and 9.6.7).
func TestReportCalendarDataLimits(t *testing.T) {
	for _, tc := range []struct {
		name         string
		calendarData string
		want         CalendarCompRequest
	}{{
		// RFC 4791 section 7.8.2
		name: "limit-recurrence-set",
		calendarData: `<C:calendar-data>
      <C:limit-recurrence-set start="20060103T000000Z" end="20060105T000000Z"/>
    </C:calendar-data>`,
		want: CalendarCompRequest{
			AllProps:           true,
			AllComps:           true,
			LimitRecurrenceSet: &CalendarLimitRequest{Start: jan2006(3), End: jan2006(5)},
		},
	}, {
		// RFC 4791 section 7.8.4
		name: "limit-freebusy-set",
		calendarData: `<C:calendar-data>
      <C:limit-freebusy-set start="20060102T000000Z" end="20060103T000000Z"/>
    </C:calendar-data>`,
		want: CalendarCompRequest{
			AllProps:         true,
			AllComps:         true,
			LimitFreeBusySet: &CalendarLimitRequest{Start: jan2006(2), End: jan2006(3)},
		},
	}, {
		name: "comp, limit-recurrence-set and limit-freebusy-set",
		calendarData: `<C:calendar-data>
      <C:comp name="VCALENDAR">
        <C:allprop/>
        <C:comp name="VEVENT"><C:allprop/></C:comp>
        <C:comp name="VFREEBUSY"><C:prop name="FREEBUSY"/></C:comp>
      </C:comp>
      <C:limit-recurrence-set start="20060103T000000Z" end="20060105T000000Z"/>
      <C:limit-freebusy-set start="20060102T000000Z" end="20060103T000000Z"/>
    </C:calendar-data>`,
		want: CalendarCompRequest{
			Name:     "VCALENDAR",
			AllProps: true,
			Comps: []CalendarCompRequest{
				{Name: "VEVENT", AllProps: true},
				{Name: "VFREEBUSY", Props: []string{"FREEBUSY"}},
			},
			LimitRecurrenceSet: &CalendarLimitRequest{Start: jan2006(3), End: jan2006(5)},
			LimitFreeBusySet:   &CalendarLimitRequest{Start: jan2006(2), End: jan2006(3)},
		},
	}, {
		// expand only excludes limit-recurrence-set
		name: "expand and limit-freebusy-set",
		calendarData: `<C:calendar-data>
      <C:expand start="20060103T000000Z" end="20060105T000000Z"/>
      <C:limit-freebusy-set start="20060102T000000Z" end="20060103T000000Z"/>
    </C:calendar-data>`,
		want: CalendarCompRequest{
			AllProps:         true,
			AllComps:         true,
			Expand:           &CalendarExpandRequest{Start: jan2006(3), End: jan2006(5)},
			LimitFreeBusySet: &CalendarLimitRequest{Start: jan2006(2), End: jan2006(3)},
		},
	}} {
		for _, report := range calendarDataReports(tc.calendarData) {
			t.Run(tc.name+"/"+report.name, func(t *testing.T) {
				w, backend := serveCalendarDataReport(report.body)
				if w.Code != http.StatusMultiStatus {
					t.Fatalf("REPORT returned %v: %s", w.Code, w.Body.String())
				}
				var got CalendarCompRequest
				switch {
				case backend.query != nil:
					got = backend.query.CompRequest
				case len(backend.get) == 1:
					got = backend.get[0]
				default:
					t.Fatalf("the backend wasn't asked for calendar objects")
				}
				if !reflect.DeepEqual(got, tc.want) {
					t.Errorf("data request = %+v, want %+v", got, tc.want)
				}
			})
		}
	}
}

// QueryCalendar sends the limit-recurrence-set and limit-freebusy-set of its
// request.
func TestQueryCalendarLimits(t *testing.T) {
	want := CalendarCompRequest{
		Name:               "VCALENDAR",
		AllProps:           true,
		AllComps:           true,
		LimitRecurrenceSet: &CalendarLimitRequest{Start: jan2006(3), End: jan2006(5)},
		LimitFreeBusySet:   &CalendarLimitRequest{Start: jan2006(2), End: jan2006(3)},
	}
	cq := queryCalendarRequest(t, &CalendarQuery{
		CompRequest: want,
		CompFilter:  CompFilter{Name: "VCALENDAR"},
	})
	if cq.Prop == nil {
		t.Fatalf("calendar-query without a prop element")
	}
	got, err := decodeCalendarDataProp(cq.Prop)
	if err != nil {
		t.Fatalf("failed to decode the calendar-data request: %v", err)
	}
	if !reflect.DeepEqual(*got, want) {
		t.Errorf("sent data request = %+v, want %+v", *got, want)
	}
}

// RFC 4791 section 9.6: a request can't ask for both expand and
// limit-recurrence-set.
func TestQueryCalendarExpandAndLimitRecurrenceSet(t *testing.T) {
	var sent bool
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sent = true
		http.Error(w, "unexpected request", http.StatusBadRequest)
	}))
	defer ts.Close()

	c, err := NewClient(nil, ts.URL)
	if err != nil {
		t.Fatal(err)
	}
	_, err = c.QueryCalendar(context.Background(), "/user/calendars/cal/", &CalendarQuery{
		CompRequest: CalendarCompRequest{
			Expand:             &CalendarExpandRequest{Start: jan2006(3), End: jan2006(5)},
			LimitRecurrenceSet: &CalendarLimitRequest{Start: jan2006(3), End: jan2006(5)},
		},
		CompFilter: CompFilter{Name: "VCALENDAR"},
	})
	if err == nil {
		t.Errorf("QueryCalendar() with expand and limit-recurrence-set succeeded")
	}
	if sent {
		t.Errorf("QueryCalendar() sent the request")
	}
}

// The client checks the time ranges of expand, limit-recurrence-set and
// limit-freebusy-set as the server does, a zero time being a missing
// attribute, rather than sending e.g. 00010101T000000Z (RFC 4791 sections
// 9.6.5 to 9.6.7).
func TestQueryCalendarInvalidTimeRange(t *testing.T) {
	var sent bool
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sent = true
		http.Error(w, "unexpected request", http.StatusBadRequest)
	}))
	defer ts.Close()

	c, err := NewClient(nil, ts.URL)
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name string
		req  CalendarCompRequest
	}{
		{"expand without start and end", CalendarCompRequest{Expand: &CalendarExpandRequest{}}},
		{"expand without start", CalendarCompRequest{Expand: &CalendarExpandRequest{End: jan2006(5)}}},
		{"expand without end", CalendarCompRequest{Expand: &CalendarExpandRequest{Start: jan2006(3)}}},
		{"expand ending before its start", CalendarCompRequest{Expand: &CalendarExpandRequest{Start: jan2006(5), End: jan2006(3)}}},
		{"limit-recurrence-set without end", CalendarCompRequest{LimitRecurrenceSet: &CalendarLimitRequest{Start: jan2006(3)}}},
		{"limit-recurrence-set ending at its start", CalendarCompRequest{LimitRecurrenceSet: &CalendarLimitRequest{Start: jan2006(3), End: jan2006(3)}}},
		{"limit-freebusy-set without start", CalendarCompRequest{LimitFreeBusySet: &CalendarLimitRequest{End: jan2006(3)}}},
		{"limit-freebusy-set ending before its start", CalendarCompRequest{
			Expand:           &CalendarExpandRequest{Start: jan2006(3), End: jan2006(5)},
			LimitFreeBusySet: &CalendarLimitRequest{Start: jan2006(3), End: jan2006(2)},
		}},
	} {
		sent = false
		_, err := c.QueryCalendar(context.Background(), "/user/calendars/cal/", &CalendarQuery{
			CompRequest: tc.req,
			CompFilter:  CompFilter{Name: "VCALENDAR"},
		})
		if err == nil {
			t.Errorf("%v: QueryCalendar() succeeded", tc.name)
		}
		if sent {
			t.Errorf("%v: QueryCalendar() sent the request, and got: %v", tc.name, err)
		}
	}
}
