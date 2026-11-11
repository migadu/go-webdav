package caldav

import (
	"context"
	"encoding/xml"
	"errors"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"

	"github.com/emersion/go-webdav"
	"github.com/emersion/go-webdav/internal"
)

// partialBackend is a testBackend whose collection listings, queries and
// deletions return canned errors.
type partialBackend struct {
	testBackend
	listCalendarsErr error
	listObjectsErr   error
	queryObjects     []CalendarObject
	queryErr         error
	deleteErr        error
}

func (b partialBackend) ListCalendars(ctx context.Context) ([]Calendar, error) {
	return b.calendars, b.listCalendarsErr
}

func (b partialBackend) ListCalendarObjects(ctx context.Context, path string, req *CalendarCompRequest) ([]CalendarObject, error) {
	return b.objectMap[path], b.listObjectsErr
}

func (b partialBackend) QueryCalendarObjects(ctx context.Context, path string, query *CalendarQuery) ([]CalendarObject, error) {
	return b.queryObjects, b.queryErr
}

func (b partialBackend) DeleteCalendarObject(ctx context.Context, path string) error {
	return b.deleteErr
}

const propFindETag = `<?xml version="1.0" encoding="UTF-8"?>
<d:propfind xmlns:d="DAV:">
  <d:prop>
    <d:resourcetype/>
    <d:getetag/>
  </d:prop>
</d:propfind>`

const calendarQueryETag = `<?xml version="1.0" encoding="UTF-8"?>
<c:calendar-query xmlns:d="DAV:" xmlns:c="urn:ietf:params:xml:ns:caldav">
  <d:prop>
    <d:getetag/>
  </d:prop>
  <c:filter>
    <c:comp-filter name="VCALENDAR"/>
  </c:filter>
</c:calendar-query>`

var (
	partialCalendar = Calendar{Path: "/user/calendars/a/"}
	partialObjects  = []CalendarObject{
		{Path: "/user/calendars/a/1.ics", ETag: "1"},
		{Path: "/user/calendars/a/2.ics", ETag: "2"},
	}
)

func newPartialBackend() partialBackend {
	return partialBackend{testBackend: testBackend{
		calendars: []Calendar{partialCalendar},
		objectMap: map[string][]CalendarObject{partialCalendar.Path: partialObjects},
	}}
}

func serveCalDAV(b Backend, method, target, depth, body string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, target, strings.NewReader(body))
	if body != "" {
		req.Header.Set("Content-Type", "application/xml")
	}
	if depth != "" {
		req.Header.Set("Depth", depth)
	}
	w := httptest.NewRecorder()
	(&Handler{Backend: b}).ServeHTTP(w, req)
	return w
}

// multiStatusCodes decodes a multistatus body and maps each href to the
// status of its response; a response carrying propstats is reported as 200.
func multiStatusCodes(t *testing.T, w *httptest.ResponseRecorder) map[string]int {
	t.Helper()
	if w.Code != http.StatusMultiStatus {
		t.Fatalf("status = %d, want %d; body:\n%s", w.Code, http.StatusMultiStatus, w.Body.String())
	}
	var ms internal.MultiStatus
	if err := xml.NewDecoder(w.Body).Decode(&ms); err != nil {
		t.Fatalf("failed to decode multistatus: %v", err)
	}
	codes := make(map[string]int)
	for _, resp := range ms.Responses {
		for _, href := range resp.Hrefs {
			code := http.StatusOK
			if resp.Status != nil {
				code = resp.Status.Code
			}
			codes[href.Path] = code
		}
	}
	return codes
}

func checkStatusCodes(t *testing.T, got, want map[string]int) {
	t.Helper()
	if !reflect.DeepEqual(got, want) {
		t.Errorf("response statuses = %v, want %v", got, want)
	}
}

func TestPropFindCalendarPartialFailure(t *testing.T) {
	b := newPartialBackend()
	b.listObjectsErr = webdav.NewHrefError("/user/calendars/a/3.ics", webdav.NewHTTPError(http.StatusForbidden, nil))

	w := serveCalDAV(b, "PROPFIND", partialCalendar.Path, "1", propFindETag)
	checkStatusCodes(t, multiStatusCodes(t, w), map[string]int{
		"/user/calendars/a/":      http.StatusOK,
		"/user/calendars/a/1.ics": http.StatusOK,
		"/user/calendars/a/2.ics": http.StatusOK,
		"/user/calendars/a/3.ics": http.StatusForbidden,
	})
}

func TestPropFindCalendarHomeSetPartialFailure(t *testing.T) {
	b := newPartialBackend()
	b.listCalendarsErr = webdav.NewHrefError("/user/calendars/b/", webdav.NewHTTPError(http.StatusForbidden, nil))

	w := serveCalDAV(b, "PROPFIND", "/user/calendars/", "1", propFindETag)
	checkStatusCodes(t, multiStatusCodes(t, w), map[string]int{
		"/user/calendars/":   http.StatusOK,
		"/user/calendars/a/": http.StatusOK,
		"/user/calendars/b/": http.StatusForbidden,
	})
}

func TestPropFindCalendarPlainError(t *testing.T) {
	b := newPartialBackend()
	b.listObjectsErr = webdav.NewHTTPError(http.StatusForbidden, nil)

	w := serveCalDAV(b, "PROPFIND", partialCalendar.Path, "1", propFindETag)
	if w.Code != http.StatusForbidden {
		t.Errorf("status = %d, want %d; body:\n%s", w.Code, http.StatusForbidden, w.Body.String())
	}
}

func TestCalendarQueryPartialFailure(t *testing.T) {
	b := newPartialBackend()
	b.queryObjects = partialObjects[:1]
	b.queryErr = errors.Join(
		webdav.NewHrefError("/user/calendars/a/2.ics", webdav.NewHTTPError(http.StatusForbidden, nil)),
		webdav.NewHrefError("/user/calendars/a/3.ics", errors.New("corrupt data")),
	)

	w := serveCalDAV(b, "REPORT", partialCalendar.Path, "1", calendarQueryETag)
	checkStatusCodes(t, multiStatusCodes(t, w), map[string]int{
		"/user/calendars/a/1.ics": http.StatusOK,
		"/user/calendars/a/2.ics": http.StatusForbidden,
		"/user/calendars/a/3.ics": http.StatusInternalServerError,
	})
}

func TestCalendarQueryPlainError(t *testing.T) {
	b := newPartialBackend()
	b.queryObjects = partialObjects[:1]
	b.queryErr = webdav.NewHTTPError(http.StatusForbidden, nil)

	w := serveCalDAV(b, "REPORT", partialCalendar.Path, "1", calendarQueryETag)
	if w.Code != http.StatusForbidden {
		t.Errorf("status = %d, want %d; body:\n%s", w.Code, http.StatusForbidden, w.Body.String())
	}
}

func TestDeleteCalendarPartialFailure(t *testing.T) {
	b := newPartialBackend()
	b.deleteErr = webdav.NewHrefError("/user/calendars/a/1.ics", webdav.NewHTTPError(http.StatusLocked, nil))

	w := serveCalDAV(b, http.MethodDelete, partialCalendar.Path, "", "")
	checkStatusCodes(t, multiStatusCodes(t, w), map[string]int{
		"/user/calendars/a/1.ics": http.StatusLocked,
	})
}

func TestDeleteCalendarPlainError(t *testing.T) {
	b := newPartialBackend()
	b.deleteErr = webdav.NewHTTPError(http.StatusLocked, nil)

	w := serveCalDAV(b, http.MethodDelete, partialCalendar.Path, "", "")
	if w.Code != http.StatusLocked {
		t.Errorf("status = %d, want %d; body:\n%s", w.Code, http.StatusLocked, w.Body.String())
	}
}
