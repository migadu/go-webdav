package caldav

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

const etagTestCalendar = "BEGIN:VCALENDAR\r\nVERSION:2.0\r\nPRODID:-//Example Corp.//CalDAV Client//EN\r\n" +
	"BEGIN:VEVENT\r\nDTSTAMP:20060206T001102Z\r\nUID:etag\r\nDTSTART:20060102T100000Z\r\nSUMMARY:ETag\r\n" +
	"END:VEVENT\r\nEND:VCALENDAR\r\n"

func TestGetCalendarObjectETag(t *testing.T) {
	for _, tc := range []struct {
		header, want string
	}{
		{`"abc"`, "abc"},
		{`"a\b"`, `a\b`},
		// Out of spec, but sent by some servers
		{`abc`, "abc"},
	} {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "text/calendar")
			w.Header().Set("ETag", tc.header)
			w.Write([]byte(etagTestCalendar))
		}))

		c, err := NewClient(nil, srv.URL)
		if err != nil {
			t.Fatal(err)
		}
		co, err := c.GetCalendarObject(context.Background(), "/cal/etag.ics")
		srv.Close()
		if err != nil {
			t.Errorf("ETag: %s: GetCalendarObject() = %v", tc.header, err)
			continue
		}
		if co.ETag != tc.want {
			t.Errorf("ETag: %s: got %q, want %q", tc.header, co.ETag, tc.want)
		}
	}
}
