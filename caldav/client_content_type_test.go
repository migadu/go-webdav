package caldav

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/emersion/go-ical"
)

// getCalendarObjectWith runs GetCalendarObject against a server answering
// with the given Content-Type and body. An empty contentType sends no
// Content-Type at all.
func getCalendarObjectWith(t *testing.T, contentType, body string) (*CalendarObject, error) {
	t.Helper()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if contentType == "" {
			// Stop net/http from sniffing one
			w.Header()["Content-Type"] = nil
		} else {
			w.Header().Set("Content-Type", contentType)
		}
		w.Write([]byte(body))
	}))
	defer srv.Close()

	c, err := NewClient(nil, srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	return c.GetCalendarObject(context.Background(), "/cal/etag.ics")
}

func TestGetCalendarObjectContentType(t *testing.T) {
	for _, contentType := range []string{
		"text/calendar",
		"text/calendar; charset=utf-8",
		"Text/Calendar",
		// Out of spec, but sent by some servers such as iCloud
		"text/plain",
		"text/plain; charset=utf-8",
		"",
	} {
		co, err := getCalendarObjectWith(t, contentType, etagTestCalendar)
		if err != nil {
			t.Errorf("Content-Type %q: GetCalendarObject() = %v", contentType, err)
			continue
		}
		events := co.Data.Events()
		if len(events) != 1 {
			t.Errorf("Content-Type %q: got %d events, want 1", contentType, len(events))
			continue
		}
		if got, err := events[0].Props.Text(ical.PropUID); err != nil || got != "etag" {
			t.Errorf("Content-Type %q: got UID %q (%v), want %q", contentType, got, err, "etag")
		}
		if got, want := co.Path, "/cal/etag.ics"; got != want {
			t.Errorf("Content-Type %q: got path %q, want %q", contentType, got, want)
		}
	}
}

func TestGetCalendarObjectContentTypeMismatch(t *testing.T) {
	for _, contentType := range []string{
		"text/html",
		"text/html; charset=utf-8",
		"application/json",
	} {
		_, err := getCalendarObjectWith(t, contentType, etagTestCalendar)
		if err == nil {
			t.Errorf("Content-Type %q: GetCalendarObject() succeeded, want an error", contentType)
			continue
		}
		if want := `caldav: expected Content-Type "text/calendar"`; !strings.Contains(err.Error(), want) {
			t.Errorf("Content-Type %q: GetCalendarObject() = %q, want an error containing %q", contentType, err, want)
		}
	}
}

func TestGetCalendarObjectNotICalendar(t *testing.T) {
	for _, tc := range []struct {
		contentType, body string
	}{
		{"text/plain", "Service temporarily unavailable\n"},
		{"text/plain", "Error: no such event\n"},
		{"text/plain", ""},
		{"", ""},
		{"text/calendar", "BEGIN:VCALENDAR\r\nVERSION:2.0\r\n"},
	} {
		_, err := getCalendarObjectWith(t, tc.contentType, tc.body)
		if err == nil {
			t.Errorf("Content-Type %q, body %q: GetCalendarObject() succeeded, want an error", tc.contentType, tc.body)
			continue
		}
		// The error must say what was expected and what the server sent
		for _, want := range []string{"caldav: ", "iCalendar", `"` + tc.contentType + `"`} {
			if !strings.Contains(err.Error(), want) {
				t.Errorf("Content-Type %q, body %q: GetCalendarObject() = %q, want an error containing %q", tc.contentType, tc.body, err, want)
			}
		}
	}
}
