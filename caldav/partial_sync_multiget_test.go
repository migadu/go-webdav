package caldav

import (
	"context"
	"encoding/xml"
	"errors"
	"net/http"
	"net/http/httptest"
	"reflect"
	"sort"
	"strings"
	"testing"

	"github.com/emersion/go-ical"
	"github.com/migadu/go-webdav"
	"github.com/migadu/go-webdav/internal"
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
		got := syncStatusCodes(decodeSyncResponses(t, w))
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

// TestSyncCollectionHrefErrors checks the responses for the members a
// SyncBackend reports with HrefErrors. RFC 6578 section 3.2 only allows a
// DAV:status, 404 Not Found, for removed members: a changed member is
// reported with propstats instead. A member URL must appear only once.
func TestSyncCollectionHrefErrors(t *testing.T) {
	const cal = "/user/calendars/a/"
	needPrivileges := xml.Name{Space: "DAV:", Local: "need-privileges"}
	ok := newSyncTestObject("ok", "OK", "e1")
	ok.Path = cal + "ok.ics"
	b := &syncMultigetPartialBackend{
		testBackend: testBackend{calendars: []Calendar{{Path: cal}}},
		syncRes: &SyncResponse{
			SyncToken: "urn:sync:2",
			Updated:   []CalendarObject{ok},
			Deleted:   []string{cal + "gone.ics"},
		},
		syncErr: errors.Join(
			webdav.NewHrefError(cal+"corrupt.ics", webdav.NewHTTPError(http.StatusInternalServerError, errors.New("corrupt object"))),
			webdav.NewHrefError(cal+"denied.ics", internal.NewConditionError(http.StatusForbidden, needPrivileges)),
			webdav.NewHrefError(cal+"vanished.ics", webdav.NewHTTPError(http.StatusNotFound, nil)),
			// Members already listed, which mustn't be listed again
			webdav.NewHrefError(cal+"ok.ics", webdav.NewHTTPError(http.StatusInternalServerError, nil)),
			webdav.NewHrefError(cal+"gone.ics", webdav.NewHTTPError(http.StatusInternalServerError, nil)),
			webdav.NewHrefError(cal+"corrupt.ics", webdav.NewHTTPError(http.StatusForbidden, nil)),
		),
	}

	w := serveCalDAV(b, "REPORT", cal, "", syncCollectionBody("urn:sync:1", "1", 0))
	resps := decodeSyncResponses(t, w)
	checkStatusCodes(t, syncStatusCodes(resps), map[string]int{
		cal + "ok.ics":       http.StatusOK,
		cal + "gone.ics":     http.StatusNotFound,
		cal + "corrupt.ics":  http.StatusInternalServerError,
		cal + "denied.ics":   http.StatusForbidden,
		cal + "vanished.ics": http.StatusNotFound,
	})

	// The propstat of a changed member which failed lists the requested
	// properties, along with the description and the DAV:error of the error
	wantNames := []xml.Name{internal.GetETagName, calendarDataName}
	if propstat := failedPropStat(t, resps[cal+"corrupt.ics"], wantNames); propstat != nil {
		if propstat.ResponseDescription != "corrupt object" {
			t.Errorf("corrupt.ics: propstat responsedescription = %q, want %q", propstat.ResponseDescription, "corrupt object")
		}
	}
	if propstat := failedPropStat(t, resps[cal+"denied.ics"], wantNames); propstat != nil {
		if propstat.Error == nil || len(propstat.Error.Raw) != 1 {
			t.Errorf("denied.ics: propstat error = %v, want %v", propstat.Error, needPrivileges)
		} else if name, _ := propstat.Error.Raw[0].XMLName(); name != needPrivileges {
			t.Errorf("denied.ics: propstat error = %v, want %v", name, needPrivileges)
		}
	}

	t.Run("Client", func(t *testing.T) {
		srv := httptest.NewServer(&Handler{Backend: b})
		defer srv.Close()
		c, err := NewClient(srv.Client(), srv.URL)
		if err != nil {
			t.Fatal(err)
		}
		res, err := c.SyncCollection(context.Background(), cal, &SyncQuery{SyncToken: "urn:sync:1"})
		if res == nil {
			t.Fatalf("SyncCollection() = %v, want a partial result", err)
		}
		var updated []string
		for _, co := range res.Updated {
			updated = append(updated, co.Path)
		}
		if want := []string{cal + "ok.ics"}; !reflect.DeepEqual(updated, want) {
			t.Errorf("Updated = %v, want %v", updated, want)
		}
		sort.Strings(res.Deleted)
		if want := []string{cal + "gone.ics", cal + "vanished.ics"}; !reflect.DeepEqual(res.Deleted, want) {
			t.Errorf("Deleted = %v, want %v", res.Deleted, want)
		}
		checkStatusCodes(t, hrefErrorCodes(err), map[string]int{
			cal + "corrupt.ics": http.StatusInternalServerError,
			cal + "denied.ics":  http.StatusForbidden,
		})
	})

	// With an empty DAV:prop, the propstat of a changed member which failed
	// names no property: Response.PropStatErr still reports its error
	t.Run("empty prop", func(t *testing.T) {
		srv := httptest.NewServer(&Handler{Backend: b})
		defer srv.Close()
		c, err := internal.NewClient(srv.Client(), srv.URL)
		if err != nil {
			t.Fatal(err)
		}
		var errs []error
		_, err = c.SyncCollection(context.Background(), cal, "urn:sync:1", internal.DepthOne, nil, &internal.Prop{}, func(resp *internal.Response) error {
			_, err := resp.Path()
			if err == nil {
				err = resp.PropStatErr()
			}
			errs = append(errs, err)
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
		checkStatusCodes(t, hrefErrorCodes(errors.Join(errs...)), map[string]int{
			cal + "gone.ics":     http.StatusNotFound,
			cal + "corrupt.ics":  http.StatusInternalServerError,
			cal + "denied.ics":   http.StatusForbidden,
			cal + "vanished.ics": http.StatusNotFound,
		})
	})
}

// A sync-collection REPORT lists the members of the calendar (RFC 6578
// section 3.2), and the only response for the request-URI is the 507
// Insufficient Storage of a truncated result (section 3.6). An HrefError
// about the calendar itself, such as a 404 Not Found for a calendar which
// doesn't exist, fails the whole request instead, with its status and
// description.
func TestSyncCollectionCollectionHrefError(t *testing.T) {
	const cal = "/user/calendars/a/"
	ok := newSyncTestObject("ok", "OK", "e1")
	ok.Path = cal + "ok.ics"
	for _, tc := range []struct {
		name      string
		href      string
		code      int
		truncated bool
	}{
		{"error", cal, http.StatusInternalServerError, false},
		{"error in a truncated result", cal, http.StatusInternalServerError, true},
		{"error without the trailing slash", strings.TrimSuffix(cal, "/"), http.StatusForbidden, true},
		{"not found", cal, http.StatusNotFound, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			b := &syncMultigetPartialBackend{
				testBackend: testBackend{calendars: []Calendar{{Path: cal}}},
				syncRes: &SyncResponse{
					SyncToken: "urn:sync:2",
					Updated:   []CalendarObject{ok},
					Truncated: tc.truncated,
				},
				syncErr: errors.Join(
					webdav.NewHrefError(cal+"corrupt.ics", webdav.NewHTTPError(http.StatusInternalServerError, nil)),
					webdav.NewHrefError(tc.href, webdav.NewHTTPError(tc.code, errors.New("calendar unavailable"))),
				),
			}
			w := serveCalDAV(b, "REPORT", cal, "", syncCollectionBody("urn:sync:1", "1", 0))
			if w.Code != tc.code || !strings.Contains(w.Body.String(), "calendar unavailable") {
				t.Errorf("got status %v and body:\n%s\nwant status %v with the error description", w.Code, w.Body.String(), tc.code)
			}
		})
	}
}

// decodeSyncResponses decodes the multistatus body of a sync-collection
// REPORT, checks that each response has a shape RFC 6578 section 3.2 allows,
// and maps each href to its response.
func decodeSyncResponses(t *testing.T, w *httptest.ResponseRecorder) map[string]*internal.Response {
	t.Helper()
	if w.Code != http.StatusMultiStatus {
		t.Fatalf("status = %d, want %d; body:\n%s", w.Code, http.StatusMultiStatus, w.Body.String())
	}
	var ms internal.MultiStatus
	if err := xml.NewDecoder(w.Body).Decode(&ms); err != nil {
		t.Fatalf("failed to decode multistatus: %v", err)
	}
	resps := make(map[string]*internal.Response)
	for i := range ms.Responses {
		resp := &ms.Responses[i]
		if len(resp.Hrefs) != 1 {
			t.Fatalf("response has %d hrefs, want 1", len(resp.Hrefs))
		}
		href := resp.Hrefs[0].Path
		if _, dup := resps[href]; dup {
			t.Errorf("%v: listed twice, a member URL must appear only once", href)
		}
		resps[href] = resp
		switch {
		case resp.Status != nil:
			// 507 Insufficient Storage is for the request-URI of a
			// truncated response (section 3.6)
			if code := resp.Status.Code; code != http.StatusNotFound && code != http.StatusInsufficientStorage {
				t.Errorf("%v: DAV:status %v, only removed members have one, 404 Not Found", href, code)
			}
			if len(resp.PropStats) > 0 {
				t.Errorf("%v: removed member with a DAV:propstat", href)
			}
		case len(resp.PropStats) == 0:
			t.Errorf("%v: changed member without a DAV:propstat", href)
		}
	}
	return resps
}

// syncStatusCodes maps each href of resps, as returned by
// decodeSyncResponses, to its status: the DAV:status of a removed member,
// or, for a changed member, the first status of its propstats other than
// 200 OK, if any.
func syncStatusCodes(resps map[string]*internal.Response) map[string]int {
	codes := make(map[string]int, len(resps))
	for href, resp := range resps {
		code := http.StatusOK
		if resp.Status != nil {
			code = resp.Status.Code
		}
		for _, propstat := range resp.PropStats {
			if propstat.Status.Code != http.StatusOK {
				code = propstat.Status.Code
				break
			}
		}
		codes[href] = code
	}
	return codes
}

// failedPropStat checks that resp, the response for a changed member which
// failed, has a single propstat listing the properties names, and returns
// it.
func failedPropStat(t *testing.T, resp *internal.Response, names []xml.Name) *internal.PropStat {
	t.Helper()
	if resp == nil {
		t.Errorf("missing response")
		return nil
	}
	if resp.Status != nil || len(resp.PropStats) != 1 {
		t.Errorf("%v: got a DAV:status %v and %d propstats, want a single propstat", resp.Hrefs[0].Path, resp.Status, len(resp.PropStats))
		return nil
	}
	propstat := &resp.PropStats[0]
	var got []xml.Name
	for _, raw := range propstat.Prop.Raw {
		if name, ok := raw.XMLName(); ok {
			got = append(got, name)
		}
	}
	if !reflect.DeepEqual(got, names) {
		t.Errorf("%v: propstat lists %v, want %v", resp.Hrefs[0].Path, got, names)
	}
	return propstat
}

// hrefErrorCodes maps the href of each HrefError err is made of to the
// status of its HTTPError.
func hrefErrorCodes(err error) map[string]int {
	codes := make(map[string]int)
	var walk func(err error)
	walk = func(err error) {
		var hrefErr *webdav.HrefError
		if multi, ok := err.(interface{ Unwrap() []error }); ok {
			for _, err := range multi.Unwrap() {
				walk(err)
			}
		} else if errors.As(err, &hrefErr) {
			code := 0
			var httpErr *webdav.HTTPError
			if errors.As(hrefErr.Err, &httpErr) {
				code = httpErr.Code
			}
			codes[hrefErr.Href.Path] = code
		}
	}
	walk(err)
	return codes
}
