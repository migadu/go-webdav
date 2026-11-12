package caldav

import (
	"context"
	"encoding/xml"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/emersion/go-ical"
	"github.com/migadu/go-webdav/internal"
)

const (
	syncCalendarPath = "/user/calendars/sync"

	syncToken1       = "http://example.com/ns/sync/1"
	syncToken2       = "http://example.com/ns/sync/2"
	syncTokenPartial = "http://example.com/ns/sync/partial"
)

func newSyncTestObject(name, summary, etag string) CalendarObject {
	event := ical.NewEvent()
	event.Props.SetText(ical.PropUID, name)
	event.Props.SetDateTime(ical.PropDateTimeStamp, time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC))
	event.Props.SetText(ical.PropSummary, summary)
	cal := ical.NewCalendar()
	cal.Props.SetText(ical.PropVersion, "2.0")
	cal.Props.SetText(ical.PropProductID, "-//go-webdav//sync test//EN")
	cal.Children = []*ical.Component{event.Component}
	return CalendarObject{
		Path: syncCalendarPath + "/" + name + ".ics",
		ETag: etag,
		Data: cal,
	}
}

// syncTestBackend is a testBackend that also supports sync-collection. It
// serves a fixed change log:
//
//   - "" (initial sync): every object, new token syncToken2
//   - syncToken1: "two" was updated, "gone" was deleted, new token syncToken2
//   - syncToken2: no changes
//   - anything else: invalid token
//
// An initial sync with a Limit returns that many objects, flagged as
// truncated.
type syncTestBackend struct {
	testBackend

	mu      sync.Mutex
	queries []SyncQuery
}

func newSyncTestBackend() *syncTestBackend {
	one := newSyncTestObject("one", "Event one", "etag-one")
	two := newSyncTestObject("two", "Event two", "etag-two")
	cal := Calendar{Path: syncCalendarPath, Name: "Sync", SyncToken: syncToken2}
	return &syncTestBackend{testBackend: testBackend{
		calendars: []Calendar{cal},
		objectMap: map[string][]CalendarObject{
			syncCalendarPath: {one, two},
		},
	}}
}

func (b *syncTestBackend) SyncCollection(ctx context.Context, path string, query *SyncQuery) (*SyncResponse, error) {
	b.mu.Lock()
	b.queries = append(b.queries, *query)
	b.mu.Unlock()

	if path != syncCalendarPath {
		return nil, internal.HTTPErrorf(http.StatusNotFound, "no calendar at %q", path)
	}
	objs := b.objectMap[path]

	switch query.SyncToken {
	case "":
		if query.Limit > 0 && query.Limit < len(objs) {
			return &SyncResponse{
				SyncToken: syncTokenPartial,
				Updated:   objs[:query.Limit],
				Truncated: true,
			}, nil
		}
		return &SyncResponse{SyncToken: syncToken2, Updated: objs}, nil
	case syncToken1:
		return &SyncResponse{
			SyncToken: syncToken2,
			Updated:   objs[1:],
			Deleted:   []string{syncCalendarPath + "/gone.ics"},
		}, nil
	case syncToken2:
		return &SyncResponse{SyncToken: syncToken2}, nil
	default:
		return nil, NewInvalidSyncTokenError()
	}
}

func (b *syncTestBackend) lastQuery(t *testing.T) SyncQuery {
	t.Helper()
	b.mu.Lock()
	defer b.mu.Unlock()
	if len(b.queries) == 0 {
		t.Fatalf("backend SyncCollection was not called")
	}
	return b.queries[len(b.queries)-1]
}

const syncCollectionRequest = `<?xml version="1.0" encoding="utf-8" ?>
<D:sync-collection xmlns:D="DAV:" xmlns:C="urn:ietf:params:xml:ns:caldav">
  <D:sync-token>%s</D:sync-token>
  <D:sync-level>%s</D:sync-level>%s
  <D:prop>
    <D:getetag/>
    <C:calendar-data/>
  </D:prop>
</D:sync-collection>`

func syncCollectionBody(token, level string, limit int) string {
	var limitEl string
	if limit > 0 {
		limitEl = fmt.Sprintf("\n  <D:limit><D:nresults>%d</D:nresults></D:limit>", limit)
	}
	return fmt.Sprintf(syncCollectionRequest, token, level, limitEl)
}

func serveTestRequest(t *testing.T, h http.Handler, method, path, body string) *http.Response {
	t.Helper()
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/xml")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	return w.Result()
}

func decodeTestMultiStatus(t *testing.T, res *http.Response) *internal.MultiStatus {
	t.Helper()
	defer res.Body.Close()
	if res.StatusCode != http.StatusMultiStatus {
		t.Fatalf("got HTTP status %v, want %v", res.Status, http.StatusMultiStatus)
	}
	var ms internal.MultiStatus
	if err := xml.NewDecoder(res.Body).Decode(&ms); err != nil {
		t.Fatalf("failed to decode multistatus: %v", err)
	}
	return &ms
}

// decodeTestError checks that res is an error response with the given status
// carrying a DAV:error with the given DAV: precondition element.
func decodeTestError(t *testing.T, res *http.Response, code int, precondition string) {
	t.Helper()
	defer res.Body.Close()
	if res.StatusCode != code {
		t.Fatalf("got HTTP status %v, want %v", res.Status, code)
	}
	var davErr internal.Error
	if err := xml.NewDecoder(res.Body).Decode(&davErr); err != nil {
		t.Fatalf("failed to decode DAV:error body: %v", err)
	}
	want := xml.Name{Space: "DAV:", Local: precondition}
	for _, raw := range davErr.Raw {
		if name, ok := raw.XMLName(); ok && name == want {
			return
		}
	}
	t.Fatalf("DAV:error does not contain %v: %v", want, davErr.Error())
}

func responsesByPath(t *testing.T, ms *internal.MultiStatus) map[string]*internal.Response {
	t.Helper()
	m := make(map[string]*internal.Response)
	for i := range ms.Responses {
		resp := &ms.Responses[i]
		if len(resp.Hrefs) != 1 {
			t.Fatalf("response has %d hrefs, want 1", len(resp.Hrefs))
		}
		m[resp.Hrefs[0].Path] = resp
	}
	return m
}

func checkUpdatedResponse(t *testing.T, resp *internal.Response, obj CalendarObject) {
	t.Helper()
	if resp == nil {
		t.Fatalf("missing response for %v", obj.Path)
	}
	var getETag internal.GetETag
	if err := resp.DecodeProp(&getETag); err != nil {
		t.Fatalf("%v: getetag: %v", obj.Path, err)
	}
	if string(getETag.ETag) != obj.ETag {
		t.Errorf("%v: got ETag %q, want %q", obj.Path, getETag.ETag, obj.ETag)
	}
	var calData calendarDataResp
	if err := resp.DecodeProp(&calData); err != nil {
		t.Fatalf("%v: calendar-data: %v", obj.Path, err)
	}
	summary, _ := obj.Data.Children[0].Props.Text(ical.PropSummary)
	if !strings.Contains(string(calData.Data), "SUMMARY:"+summary) {
		t.Errorf("%v: calendar-data does not contain summary %q:\n%s", obj.Path, summary, calData.Data)
	}
}

func TestSyncCollectionInitial(t *testing.T) {
	b := newSyncTestBackend()
	h := &Handler{Backend: b}

	res := serveTestRequest(t, h, "REPORT", syncCalendarPath, syncCollectionBody("", "1", 0))
	ms := decodeTestMultiStatus(t, res)

	if ms.SyncToken != syncToken2 {
		t.Errorf("got sync-token %q, want %q", ms.SyncToken, syncToken2)
	}
	resps := responsesByPath(t, ms)
	if len(resps) != 2 {
		t.Errorf("got %d responses, want 2", len(resps))
	}
	for _, obj := range b.objectMap[syncCalendarPath] {
		checkUpdatedResponse(t, resps[obj.Path], obj)
	}

	q := b.lastQuery(t)
	if q.SyncToken != "" || q.Limit != 0 {
		t.Errorf("backend got SyncToken %q, Limit %d; want empty token and no limit", q.SyncToken, q.Limit)
	}
	wantReq := CalendarCompRequest{AllProps: true, AllComps: true}
	if !reflect.DeepEqual(q.CompRequest, wantReq) {
		t.Errorf("backend got CompRequest %+v, want %+v", q.CompRequest, wantReq)
	}
}

func TestSyncCollectionIncremental(t *testing.T) {
	b := newSyncTestBackend()
	h := &Handler{Backend: b}

	// "infinite" is equivalent to "1": calendars have no sub-collections.
	for _, level := range []string{"1", "infinite"} {
		t.Run(level, func(t *testing.T) {
			res := serveTestRequest(t, h, "REPORT", syncCalendarPath, syncCollectionBody(syncToken1, level, 0))
			ms := decodeTestMultiStatus(t, res)

			if ms.SyncToken != syncToken2 {
				t.Errorf("got sync-token %q, want %q", ms.SyncToken, syncToken2)
			}
			resps := responsesByPath(t, ms)
			if len(resps) != 2 {
				t.Errorf("got %d responses, want 2", len(resps))
			}
			checkUpdatedResponse(t, resps[syncCalendarPath+"/two.ics"], b.objectMap[syncCalendarPath][1])

			gone := resps[syncCalendarPath+"/gone.ics"]
			if gone == nil {
				t.Fatalf("missing response for deleted member")
			}
			if gone.Status == nil || gone.Status.Code != http.StatusNotFound {
				t.Errorf("deleted member: got status %+v, want 404", gone.Status)
			}
			if len(gone.PropStats) != 0 {
				t.Errorf("deleted member: got %d propstats, want none", len(gone.PropStats))
			}

			if q := b.lastQuery(t); q.SyncToken != syncToken1 {
				t.Errorf("backend got SyncToken %q, want %q", q.SyncToken, syncToken1)
			}
		})
	}
}

func TestSyncCollectionNoChanges(t *testing.T) {
	h := &Handler{Backend: newSyncTestBackend()}

	res := serveTestRequest(t, h, "REPORT", syncCalendarPath, syncCollectionBody(syncToken2, "1", 0))
	ms := decodeTestMultiStatus(t, res)
	if ms.SyncToken != syncToken2 {
		t.Errorf("got sync-token %q, want %q", ms.SyncToken, syncToken2)
	}
	if len(ms.Responses) != 0 {
		t.Errorf("got %d responses, want none", len(ms.Responses))
	}
}

func TestSyncCollectionTruncated(t *testing.T) {
	b := newSyncTestBackend()
	h := &Handler{Backend: b}

	res := serveTestRequest(t, h, "REPORT", syncCalendarPath, syncCollectionBody("", "1", 1))
	ms := decodeTestMultiStatus(t, res)

	if q := b.lastQuery(t); q.Limit != 1 {
		t.Errorf("backend got Limit %d, want 1", q.Limit)
	}
	if ms.SyncToken != syncTokenPartial {
		t.Errorf("got sync-token %q, want %q", ms.SyncToken, syncTokenPartial)
	}
	resps := responsesByPath(t, ms)
	if len(resps) != 2 {
		t.Fatalf("got %d responses, want 2 (one member, one for the truncated collection)", len(resps))
	}
	checkUpdatedResponse(t, resps[b.objectMap[syncCalendarPath][0].Path], b.objectMap[syncCalendarPath][0])

	// RFC 6578 section 3.6: the request-URI gets a 507 response.
	coll := resps[syncCalendarPath]
	if coll == nil {
		t.Fatalf("missing 507 response for the request-URI")
	}
	if coll.Status == nil || coll.Status.Code != http.StatusInsufficientStorage {
		t.Errorf("request-URI: got status %+v, want 507", coll.Status)
	}
	want := xml.Name{Space: "DAV:", Local: "number-of-matches-within-limits"}
	if coll.Error == nil || len(coll.Error.Raw) != 1 {
		t.Fatalf("request-URI: got error %+v, want %v", coll.Error, want)
	}
	if name, _ := coll.Error.Raw[0].XMLName(); name != want {
		t.Errorf("request-URI: got error %v, want %v", name, want)
	}
}

func TestSyncCollectionInvalidToken(t *testing.T) {
	h := &Handler{Backend: newSyncTestBackend()}

	res := serveTestRequest(t, h, "REPORT", syncCalendarPath, syncCollectionBody("http://example.com/ns/sync/bogus", "1", 0))
	decodeTestError(t, res, http.StatusForbidden, "valid-sync-token")
}

func TestSyncCollectionUnsupported(t *testing.T) {
	// A backend that doesn't implement SyncBackend.
	tb := newSyncTestBackend().testBackend
	h := &Handler{Backend: tb}

	res := serveTestRequest(t, h, "REPORT", syncCalendarPath, syncCollectionBody("", "1", 0))
	decodeTestError(t, res, http.StatusForbidden, "supported-report")
}

func TestSyncCollectionNotCalendar(t *testing.T) {
	b := newSyncTestBackend()
	h := &Handler{Backend: b}

	for _, p := range []string{"/user/", "/user/calendars/", syncCalendarPath + "/one.ics"} {
		t.Run(p, func(t *testing.T) {
			res := serveTestRequest(t, h, "REPORT", p, syncCollectionBody("", "1", 0))
			decodeTestError(t, res, http.StatusForbidden, "supported-report")
		})
	}

	b.mu.Lock()
	defer b.mu.Unlock()
	if len(b.queries) != 0 {
		t.Errorf("backend SyncCollection called %d times, want 0", len(b.queries))
	}
}

func TestSyncCollectionBadSyncLevel(t *testing.T) {
	h := &Handler{Backend: newSyncTestBackend()}

	res := serveTestRequest(t, h, "REPORT", syncCalendarPath, syncCollectionBody("", "2", 0))
	res.Body.Close()
	if res.StatusCode != http.StatusBadRequest {
		t.Errorf("got HTTP status %v, want %v", res.Status, http.StatusBadRequest)
	}
}

const propFindSyncProps = `<?xml version="1.0" encoding="utf-8" ?>
<D:propfind xmlns:D="DAV:">
  <D:prop>
    <D:sync-token/>
    <D:supported-report-set/>
  </D:prop>
</D:propfind>`

// Test-local decoding of the RFC 6578 / RFC 3253 properties, independent of
// the server's own types.
type testSyncTokenProp struct {
	XMLName xml.Name `xml:"DAV: sync-token"`
	Token   string   `xml:",chardata"`
}

type testSupportedReportSet struct {
	XMLName xml.Name `xml:"DAV: supported-report-set"`
	Reports []struct {
		Report struct {
			Raw []internal.RawXMLValue `xml:",any"`
		} `xml:"DAV: report"`
	} `xml:"DAV: supported-report"`
}

func (s *testSupportedReportSet) names() []string {
	var names []string
	for _, r := range s.Reports {
		for _, raw := range r.Report.Raw {
			if name, ok := raw.XMLName(); ok {
				names = append(names, name.Space+" "+name.Local)
			}
		}
	}
	sort.Strings(names)
	return names
}

func TestPropFindCalendarSyncProps(t *testing.T) {
	// A backend without sync support, whose calendar has no sync-token.
	noSync := testBackend{calendars: []Calendar{{Path: syncCalendarPath}}}

	for _, tc := range []struct {
		name        string
		backend     Backend
		syncToken   string
		wantReports []string
	}{
		{
			name:      "sync",
			backend:   newSyncTestBackend(),
			syncToken: syncToken2,
			wantReports: []string{
				"DAV: sync-collection",
				"urn:ietf:params:xml:ns:caldav calendar-multiget",
				"urn:ietf:params:xml:ns:caldav calendar-query",
			},
		},
		{
			name:    "no-sync",
			backend: noSync,
			wantReports: []string{
				"urn:ietf:params:xml:ns:caldav calendar-multiget",
				"urn:ietf:params:xml:ns:caldav calendar-query",
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := &Handler{Backend: tc.backend}

			req := httptest.NewRequest("PROPFIND", syncCalendarPath, strings.NewReader(propFindSyncProps))
			req.Header.Set("Content-Type", "application/xml")
			req.Header.Set("Depth", "0")
			w := httptest.NewRecorder()
			h.ServeHTTP(w, req)
			ms := decodeTestMultiStatus(t, w.Result())

			if len(ms.Responses) != 1 {
				t.Fatalf("got %d responses, want 1", len(ms.Responses))
			}
			resp := &ms.Responses[0]

			var tok testSyncTokenProp
			err := resp.DecodeProp(&tok)
			if tc.syncToken != "" {
				if err != nil {
					t.Errorf("sync-token: %v", err)
				} else if tok.Token != tc.syncToken {
					t.Errorf("got sync-token %q, want %q", tok.Token, tc.syncToken)
				}
			} else if !internal.IsNotFound(err) {
				t.Errorf("sync-token: got %v, want 404", err)
			}

			var reports testSupportedReportSet
			if err := resp.DecodeProp(&reports); err != nil {
				t.Fatalf("supported-report-set: %v", err)
			}
			if got := reports.names(); !reflect.DeepEqual(got, tc.wantReports) {
				t.Errorf("got supported reports %q, want %q", got, tc.wantReports)
			}
		})
	}
}

func TestClientSyncCollectionRoundTrip(t *testing.T) {
	b := newSyncTestBackend()
	ts := httptest.NewServer(&Handler{Backend: b})
	defer ts.Close()

	ctx := context.Background()
	c, err := NewClient(nil, ts.URL)
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}

	objs := b.objectMap[syncCalendarPath]
	checkObject := func(t *testing.T, got, want CalendarObject) {
		t.Helper()
		if got.Path != want.Path || got.ETag != want.ETag {
			t.Errorf("got object %q (ETag %q), want %q (ETag %q)", got.Path, got.ETag, want.Path, want.ETag)
		}
		if got.Data == nil {
			t.Fatalf("%v: calendar data not decoded", got.Path)
		}
		gotSummary, _ := got.Data.Events()[0].Props.Text(ical.PropSummary)
		wantSummary, _ := want.Data.Children[0].Props.Text(ical.PropSummary)
		if gotSummary != wantSummary {
			t.Errorf("%v: got summary %q, want %q", got.Path, gotSummary, wantSummary)
		}
	}

	t.Run("initial", func(t *testing.T) {
		res, err := c.SyncCollection(ctx, syncCalendarPath, &SyncQuery{})
		if err != nil {
			t.Fatalf("SyncCollection: %v", err)
		}
		if res.SyncToken != syncToken2 {
			t.Errorf("got sync-token %q, want %q", res.SyncToken, syncToken2)
		}
		if len(res.Updated) != len(objs) || len(res.Deleted) != 0 || res.Truncated {
			t.Fatalf("got %d updated, %d deleted, truncated=%v; want %d, 0, false", len(res.Updated), len(res.Deleted), res.Truncated, len(objs))
		}
		for i := range objs {
			checkObject(t, res.Updated[i], objs[i])
		}
	})

	t.Run("incremental", func(t *testing.T) {
		res, err := c.SyncCollection(ctx, syncCalendarPath, &SyncQuery{SyncToken: syncToken1})
		if err != nil {
			t.Fatalf("SyncCollection: %v", err)
		}
		if res.SyncToken != syncToken2 {
			t.Errorf("got sync-token %q, want %q", res.SyncToken, syncToken2)
		}
		if len(res.Updated) != 1 {
			t.Fatalf("got %d updated, want 1", len(res.Updated))
		}
		checkObject(t, res.Updated[0], objs[1])
		if want := []string{syncCalendarPath + "/gone.ics"}; !reflect.DeepEqual(res.Deleted, want) {
			t.Errorf("got deleted %q, want %q", res.Deleted, want)
		}
	})

	t.Run("truncated", func(t *testing.T) {
		res, err := c.SyncCollection(ctx, syncCalendarPath, &SyncQuery{Limit: 1})
		if err != nil {
			t.Fatalf("SyncCollection: %v", err)
		}
		if !res.Truncated {
			t.Errorf("Truncated not set")
		}
		if res.SyncToken != syncTokenPartial {
			t.Errorf("got sync-token %q, want %q", res.SyncToken, syncTokenPartial)
		}
		if len(res.Updated) != 1 {
			t.Fatalf("got %d updated, want 1", len(res.Updated))
		}
		checkObject(t, res.Updated[0], objs[0])
	})

	t.Run("invalid-token", func(t *testing.T) {
		_, err := c.SyncCollection(ctx, syncCalendarPath, &SyncQuery{SyncToken: "http://example.com/ns/sync/bogus"})
		var httpErr *internal.HTTPError
		if !errors.As(err, &httpErr) || httpErr.Code != http.StatusForbidden {
			t.Fatalf("got error %v, want 403", err)
		}
		var davErr *internal.Error
		if !errors.As(err, &davErr) || !strings.Contains(davErr.Error(), "valid-sync-token") {
			t.Errorf("got error %v, want DAV:valid-sync-token", err)
		}
	})
}
