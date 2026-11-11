package caldav

import (
	"bytes"
	"context"
	"encoding/xml"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/emersion/go-ical"
	"github.com/emersion/go-webdav"
	"github.com/emersion/go-webdav/internal"
)

// streamTimeout bounds the wait for a response in the streaming tests, so
// that they fail rather than hang if responses aren't streamed.
const streamTimeout = 10 * time.Second

// newLargeCalendarObject returns a calendar object whose calendar data is
// larger than the buffers of net/http (a few KiB), so that writing its
// response to an http.ResponseWriter sends it to the client.
func newLargeCalendarObject(path, uid string) CalendarObject {
	co := newMultiGetTestObject(path, uid)
	co.Data.Children[0].Props.SetText(ical.PropDescription, strings.Repeat("Streaming multistatus. ", 1500))
	return co
}

// blockingBackend blocks GetCalendarObject for the path block until release
// is closed.
type blockingBackend struct {
	testBackend
	block   string
	release <-chan struct{}
}

func (b *blockingBackend) GetCalendarObject(ctx context.Context, path string, req *CalendarCompRequest) (*CalendarObject, error) {
	if path == b.block {
		select {
		case <-b.release:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	return b.testBackend.GetCalendarObject(ctx, path, req)
}

// readUntil reads from r until what it read contains marker.
func readUntil(r io.Reader, marker string) ([]byte, error) {
	var b []byte
	buf := make([]byte, 512)
	for {
		n, err := r.Read(buf)
		b = append(b, buf[:n]...)
		if bytes.Contains(b, []byte(marker)) {
			return b, nil
		} else if err != nil {
			return b, err
		}
	}
}

// The response for each calendar object of a calendar-multiget is sent to
// the client as soon as it's built, before the next object is fetched.
func TestMultiGetStreaming(t *testing.T) {
	const calPath = "/user/calendars/a/"
	release := make(chan struct{})
	var releaseOnce sync.Once
	unblock := func() { releaseOnce.Do(func() { close(release) }) }

	backend := &blockingBackend{
		testBackend: testBackend{
			calendars: []Calendar{{Path: calPath}},
			objectMap: map[string][]CalendarObject{calPath: {
				newLargeCalendarObject(calPath+"1.ics", "uid-1"),
				newLargeCalendarObject(calPath+"2.ics", "uid-2"),
			}},
		},
		block:   calPath + "2.ics",
		release: release,
	}
	srv := httptest.NewServer(&Handler{Backend: backend})
	defer srv.Close()
	defer unblock()

	ctx, cancel := context.WithTimeout(context.Background(), streamTimeout)
	defer cancel()
	body := `<?xml version="1.0" encoding="UTF-8"?>
<C:calendar-multiget xmlns:D="DAV:" xmlns:C="urn:ietf:params:xml:ns:caldav">
  <D:prop><D:getetag/><C:calendar-data/></D:prop>
  <D:href>` + calPath + `1.ics</D:href>
  <D:href>` + calPath + `2.ics</D:href>
</C:calendar-multiget>`
	req, err := http.NewRequestWithContext(ctx, "REPORT", srv.URL+calPath, strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/xml")
	req.Header.Set("Depth", "1")
	resp, err := srv.Client().Do(req)
	if err != nil {
		t.Fatalf("no response while the second calendar object is being fetched: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusMultiStatus {
		t.Fatalf("REPORT returned %v, want 207", resp.Status)
	}

	first, err := readUntil(resp.Body, calPath+"1.ics</href>")
	if err != nil {
		t.Fatalf("response for the first calendar object not received while the second one is being fetched: %v", err)
	}
	unblock()
	rest, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("failed to read the rest of the response: %v", err)
	}

	var ms internal.MultiStatus
	if err := xml.Unmarshal(append(first, rest...), &ms); err != nil {
		t.Fatal(err)
	}
	if len(ms.Responses) != 2 {
		t.Fatalf("got %d responses, want 2", len(ms.Responses))
	}
	for i, uid := range []string{"uid-1", "uid-2"} {
		resp := &ms.Responses[i]
		var data calendarDataResp
		if err := resp.DecodeProp(&data); err != nil {
			t.Errorf("response %d: %v", i, err)
		} else if !bytes.Contains(data.Data, []byte("UID:"+uid)) {
			t.Errorf("response %d doesn't hold the calendar data of %s", i, uid)
		}
	}
}

// listErrorBackend fails to list the calendar objects.
type listErrorBackend struct {
	testBackend
}

func (b *listErrorBackend) ListCalendarObjects(ctx context.Context, path string, req *CalendarCompRequest) ([]CalendarObject, error) {
	return nil, webdav.NewHTTPError(http.StatusForbidden, errors.New("no listing for you"))
}

// The members of a calendar are listed before the response for the calendar
// is written, so that an error listing them is still served as the status of
// the request.
func TestPropFindListErrorStatus(t *testing.T) {
	const calPath = "/user/calendars/a/"
	backend := &listErrorBackend{testBackend{calendars: []Calendar{{Path: calPath}}}}
	handler := Handler{Backend: backend}

	req := httptest.NewRequest("PROPFIND", calPath, strings.NewReader(propFindUserPrincipal))
	req.Header.Set("Content-Type", "application/xml")
	req.Header.Set("Depth", "1")
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	if w.Code != http.StatusForbidden {
		t.Errorf("PROPFIND returned %v, want 403: %s", w.Code, w.Body.String())
	}
	if strings.Contains(w.Body.String(), "multistatus") {
		t.Errorf("PROPFIND returned a multistatus along with its error: %s", w.Body.String())
	}
}

// A calendar-multiget asking for no properties is refused before any
// response is written, whether or not the first object exists.
func TestMultiGetWithoutProp(t *testing.T) {
	const calPath = "/user/calendars/a/"
	backend := testBackend{
		calendars: []Calendar{{Path: calPath}},
		objectMap: map[string][]CalendarObject{calPath: {
			newMultiGetTestObject(calPath+"1.ics", "uid-1"),
		}},
	}
	handler := Handler{Backend: backend}

	body := `<?xml version="1.0" encoding="UTF-8"?>
<C:calendar-multiget xmlns:D="DAV:" xmlns:C="urn:ietf:params:xml:ns:caldav">
  <D:href>` + calPath + `missing.ics</D:href>
  <D:href>` + calPath + `1.ics</D:href>
</C:calendar-multiget>`
	req := httptest.NewRequest("REPORT", calPath, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/xml")
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	if w.Code != http.StatusBadRequest {
		t.Errorf("REPORT returned %v, want 400: %s", w.Code, w.Body.String())
	}
}
