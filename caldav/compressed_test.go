package caldav

import (
	"bytes"
	"compress/gzip"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/emersion/go-ical"
)

func TestReportCompressedBody(t *testing.T) {
	calendar := Calendar{Path: "/user/calendars/a"}
	event := ical.NewEvent()
	event.Props.SetText(ical.PropUID, "e7dbb4e2-3d1c-4f6e-9b67-2f2f6a6c1f0e")
	event.Props.SetDateTime(ical.PropDateTimeStamp, time.Now())
	event.Props.SetText(ical.PropSummary, "Compressed REPORT")
	cal := ical.NewCalendar()
	cal.Props.SetText(ical.PropVersion, "2.0")
	cal.Props.SetText(ical.PropProductID, "-//xyz Corp//NONSGML PDA Calendar Version 1.0//EN")
	cal.Children = []*ical.Component{event.Component}
	object := CalendarObject{Path: "/user/calendars/a/test.ics", Data: cal}
	handler := Handler{Backend: testBackend{
		calendars: []Calendar{calendar},
		objectMap: map[string][]CalendarObject{calendar.Path: {object}},
	}}

	var buf bytes.Buffer
	zw := gzip.NewWriter(&buf)
	if _, err := fmt.Fprintf(zw, reportCalendarData, object.Path); err != nil {
		t.Fatal(err)
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}

	req := httptest.NewRequest("REPORT", calendar.Path, &buf)
	req.Header.Set("Content-Type", "application/xml")
	req.Header.Set("Content-Encoding", "gzip")
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	if w.Code != http.StatusMultiStatus {
		t.Fatalf("REPORT status = %d, want %d; body:\n%s", w.Code, http.StatusMultiStatus, w.Body)
	}
	if !strings.Contains(w.Body.String(), "SUMMARY:Compressed REPORT") {
		t.Errorf("REPORT response lacks calendar data:\n%s", w.Body)
	}

	req = httptest.NewRequest("REPORT", calendar.Path, strings.NewReader(fmt.Sprintf(reportCalendarData, object.Path)))
	req.Header.Set("Content-Type", "application/xml")
	req.Header.Set("Content-Encoding", "br")
	w = httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	if w.Code != http.StatusUnsupportedMediaType {
		t.Fatalf("REPORT status = %d, want %d; body:\n%s", w.Code, http.StatusUnsupportedMediaType, w.Body)
	}
}
