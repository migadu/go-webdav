package caldav

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"
)

// TestClientRedirect checks that a calendar query and a calendar object put,
// redirected with 301 Moved Permanently e.g. because the calendar moved,
// follow the redirect with their own method, rather than with a GET request.
func TestClientRedirect(t *testing.T) {
	const (
		oldCal = "/calendars/old/"
		cal    = "/calendars/work/"
	)
	movedTo := func(location string) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Location", location)
			w.WriteHeader(http.StatusMovedPermanently)
		}
	}

	var putBody string
	host := &fakeHost{name: "caldav.test", routes: map[string]http.HandlerFunc{
		"REPORT " + oldCal:            movedTo(cal),
		"PUT " + oldCal + "event.ics": movedTo("http://caldav.test" + cal + "event.ics"),
		"GET " + cal: func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "text/html")
			io.WriteString(w, "<html><body>Work</body></html>")
		},
		"GET " + cal + "event.ics": icalHandler(`"1"`),
		"REPORT " + cal: reportHandler(t, map[string]string{
			"calendar-query": `<d:response><d:href>` + cal + `event.ics</d:href>` +
				propStat(`<d:getetag>"1"</d:getetag><cal:calendar-data>`+crossHostEvent+`</cal:calendar-data>`) +
				`</d:response>`,
		}, nil),
		"PUT " + cal + "event.ics": func(w http.ResponseWriter, r *http.Request) {
			b, _ := io.ReadAll(r.Body)
			putBody = string(b)
			w.Header().Set("ETag", `"2"`)
			w.WriteHeader(http.StatusCreated)
		},
	}}

	ctx := context.Background()
	c, err := NewClient(newFakeHostsClient(t, host), "http://caldav.test")
	if err != nil {
		t.Fatal(err)
	}

	objs, err := c.QueryCalendar(ctx, oldCal, &CalendarQuery{CompFilter: CompFilter{Name: "VCALENDAR"}})
	if err != nil {
		t.Fatalf("QueryCalendar() = %v", err)
	}
	if len(objs) != 1 || objs[0].Path != cal+"event.ics" || objs[0].ETag != "1" {
		t.Fatalf("QueryCalendar() = %+v, want a calendar object at %q", objs, cal+"event.ics")
	}

	co, err := c.PutCalendarObject(ctx, oldCal+"event.ics", objs[0].Data, nil)
	if err != nil {
		t.Fatalf("PutCalendarObject() = %v", err)
	}
	if co.ETag != "2" {
		t.Errorf("PutCalendarObject() = %+v, want the ETag of the object put, 2", co)
	}
	if !strings.Contains(putBody, "UID:event") {
		t.Errorf("PUT body at the redirect target = %q, want the calendar", putBody)
	}
}
