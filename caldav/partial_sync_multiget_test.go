package caldav

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"

	"github.com/emersion/go-ical"
	"github.com/emersion/go-webdav"
)

type syncMultigetPartialBackend struct {
	testBackend
	syncRes     *SyncResponse
	syncErr     error
	multigetRes []CalendarObject
	multigetErr error
}

func (b *syncMultigetPartialBackend) SyncCollection(ctx context.Context, path string, query *SyncQuery) (*SyncResponse, error) {
	return b.syncRes, b.syncErr
}

func (b *syncMultigetPartialBackend) GetCalendarObjects(ctx context.Context, paths []string, req *CalendarCompRequest) ([]CalendarObject, error) {
	return b.multigetRes, b.multigetErr
}

func TestPartialHrefErrorsInSyncAndMultiget(t *testing.T) {
	calObj, err := ical.NewDecoder(strings.NewReader(`BEGIN:VCALENDAR
VERSION:2.0
PRODID:-//Test//EN
BEGIN:VEVENT
UID:ok-1
DTSTAMP:20260101T000000Z
DTSTART:20260102T100000Z
SUMMARY:OK Event
END:VEVENT
END:VCALENDAR`)).Decode()
	if err != nil {
		t.Fatal(err)
	}

	b := &syncMultigetPartialBackend{
		testBackend: testBackend{
			calendars: []Calendar{{Path: "/user/calendars/a/"}},
		},
		syncRes: &SyncResponse{
			SyncToken: "urn:sync:2",
			Updated: []CalendarObject{
				{Path: "/user/calendars/a/ok.ics", ETag: "e1", Data: calObj},
			},
			Deleted: []string{"/user/calendars/a/gone.ics"},
		},
		syncErr: webdav.NewHrefError("/user/calendars/a/corrupt.ics", webdav.NewHTTPError(http.StatusInternalServerError, errors.New("corrupt object"))),
		multigetRes: []CalendarObject{
			{Path: "/user/calendars/a/ok.ics", ETag: "e1", Data: calObj},
		},
		multigetErr: webdav.NewHrefError("/user/calendars/a/forbidden.ics", webdav.NewHTTPError(http.StatusForbidden, errors.New("access denied"))),
	}

	t.Run("SyncCollection includes HrefError responses in 207 Multi-Status", func(t *testing.T) {
		w := serveCalDAV(b, "REPORT", "/user/calendars/a/", "", syncCollectionBody("urn:sync:1", "1", 0))
		got := multiStatusCodes(t, w)
		want := map[string]int{
			"/user/calendars/a/ok.ics":      http.StatusOK,
			"/user/calendars/a/gone.ics":    http.StatusNotFound,
			"/user/calendars/a/corrupt.ics": http.StatusInternalServerError,
		}
		checkStatusCodes(t, got, want)
	})

	t.Run("CalendarObjectsBackend.GetCalendarObjects reports per-href errors", func(t *testing.T) {
		body := `<?xml version="1.0" encoding="utf-8"?>
<C:calendar-multiget xmlns:D="DAV:" xmlns:C="urn:ietf:params:xml:ns:caldav">
  <D:prop><D:getetag/><C:calendar-data/></D:prop>
  <D:href>/user/calendars/a/ok.ics</D:href>
  <D:href>/user/calendars/a/forbidden.ics</D:href>
  <D:href>/user/calendars/a/missing.ics</D:href>
</C:calendar-multiget>`
		w := serveCalDAV(b, "REPORT", "/user/calendars/a/", "", body)
		got := multiStatusCodes(t, w)
		want := map[string]int{
			"/user/calendars/a/ok.ics":        http.StatusOK,
			"/user/calendars/a/forbidden.ics": http.StatusForbidden,
			"/user/calendars/a/missing.ics":   http.StatusNotFound,
		}
		checkStatusCodes(t, got, want)
	})
}
