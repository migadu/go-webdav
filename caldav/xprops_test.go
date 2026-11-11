package caldav

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync"
	"testing"

	"github.com/emersion/go-ical"
	"github.com/emersion/go-webdav"
)

// xPropsICS is a reminder as Apple clients store it, with the properties and
// parameters of upstream issue 115: experimental properties (x-prop, RFC 5545
// section 3.8.8.2) on the calendar, the to-do and its alarm, experimental
// parameters (x-param, RFC 5545 section 3.2) on experimental and standard
// properties, a quoted parameter value with commas, a multi-valued parameter,
// a lowercase name and a folded line.
const xPropsICS = "BEGIN:VCALENDAR\r\n" +
	"VERSION:2.0\r\n" +
	"PRODID:-//Apple Inc.//iOS 16.1//EN\r\n" +
	"X-WR-CALNAME:Reminders\r\n" +
	"X-APPLE-CALENDAR-COLOR:#FF2968\r\n" +
	"BEGIN:VTODO\r\n" +
	"UID:5F0B6B38-0C48-4B5C-9E2A-6E3C5C1F2A10\r\n" +
	"DTSTAMP:20221108T100445Z\r\n" +
	"SUMMARY:Pick up the parcel\r\n" +
	"X-APPLE-SORT-ORDER:690545645\r\n" +
	"LOCATION:Apple Park\r\n" +
	"X-APPLE-STRUCTURED-LOCATION;VALUE=URI;X-ADDRESS=\"1 Apple Park Way, Cuper\r\n" +
	" tino, CA 95014, United States\";X-APPLE-RADIUS=70.58;X-APPLE-REFERENCEFRAME=1\r\n" +
	" ;X-TITLE=Apple Park:geo:37.334900,-122.009020\r\n" +
	"ATTENDEE;CN=Jane Doe;X-NUM-GUESTS=0:mailto:jane@example.com\r\n" +
	"x-vendor-flags;x-vendor-param=a,\"b;c\":pinned\r\n" +
	"BEGIN:VALARM\r\n" +
	"ACTION:DISPLAY\r\n" +
	"TRIGGER:-PT15M\r\n" +
	"DESCRIPTION:Reminder\r\n" +
	"X-WR-ALARMUID:2E2B1A4C-7F3D-4C1B-8F5E-1D2C3B4A5968\r\n" +
	"X-APPLE-DEFAULT-ALARM:TRUE\r\n" +
	"END:VALARM\r\n" +
	"END:VTODO\r\n" +
	"END:VCALENDAR\r\n"

func decodeXPropsICS(t *testing.T) *ical.Calendar {
	t.Helper()
	cal, err := ical.NewDecoder(strings.NewReader(xPropsICS)).Decode()
	if err != nil {
		t.Fatalf("decoding the test calendar: %v", err)
	}
	return cal
}

// checkXProps checks that cal is xPropsICS, with its experimental properties
// and parameters.
func checkXProps(t *testing.T, cal *ical.Calendar) {
	t.Helper()
	if cal == nil || cal.Component == nil {
		t.Fatalf("no calendar data")
	}
	want := decodeXPropsICS(t)

	if got := cal.Props.Get("X-WR-CALNAME"); got == nil || got.Value != "Reminders" {
		t.Errorf("X-WR-CALNAME = %v, want Reminders", got)
	}
	var todo *ical.Component
	for _, child := range cal.Children {
		if child.Name == ical.CompToDo {
			todo = child
		}
	}
	if todo == nil {
		t.Fatalf("no VTODO in %v", cal.Children)
	}
	if got := todo.Props.Get("X-APPLE-SORT-ORDER"); got == nil || got.Value != "690545645" {
		t.Errorf("X-APPLE-SORT-ORDER = %v, want 690545645", got)
	}
	if loc := todo.Props.Get("X-APPLE-STRUCTURED-LOCATION"); loc == nil {
		t.Errorf("X-APPLE-STRUCTURED-LOCATION is missing")
	} else {
		if loc.Value != "geo:37.334900,-122.009020" {
			t.Errorf("X-APPLE-STRUCTURED-LOCATION value = %q", loc.Value)
		}
		for name, want := range map[string]string{
			"VALUE":                  "URI",
			"X-ADDRESS":              "1 Apple Park Way, Cupertino, CA 95014, United States",
			"X-APPLE-RADIUS":         "70.58",
			"X-APPLE-REFERENCEFRAME": "1",
			"X-TITLE":                "Apple Park",
		} {
			if got := loc.Params.Get(name); got != want {
				t.Errorf("X-APPLE-STRUCTURED-LOCATION parameter %v = %q, want %q", name, got, want)
			}
		}
	}
	if got := todo.Props.Get(ical.PropAttendee); got == nil || got.Params.Get("X-NUM-GUESTS") != "0" {
		t.Errorf("ATTENDEE = %v, want an X-NUM-GUESTS parameter", got)
	}
	if got := todo.Props.Get("X-VENDOR-FLAGS"); got == nil || !reflect.DeepEqual(got.Params["X-VENDOR-PARAM"], []string{"a", "b;c"}) {
		t.Errorf("X-VENDOR-FLAGS = %v, want X-VENDOR-PARAM values a and b;c", got)
	}

	// Nothing else was lost or added on the way
	if !reflect.DeepEqual(cal, want) {
		var gotBuf, wantBuf strings.Builder
		ical.NewEncoder(&gotBuf).Encode(cal)
		ical.NewEncoder(&wantBuf).Encode(want)
		t.Errorf("calendar changed:\ngot:\n%v\nwant:\n%v", gotBuf.String(), wantBuf.String())
	}
}

const (
	xPropsPrincipal = "/user/"
	xPropsHomeSet   = "/user/calendars/"
	xPropsCalendar  = "/user/calendars/reminders/"
	xPropsObject    = "/user/calendars/reminders/parcel.ics"
)

// xPropsBackend stores calendar objects in memory, validating them with
// ValidateCalendarObject and querying them with Filter as backends do.
type xPropsBackend struct {
	mu      sync.Mutex
	objects map[string]*ical.Calendar
}

var _ SyncBackend = (*xPropsBackend)(nil)

func (b *xPropsBackend) CurrentUserPrincipal(ctx context.Context) (string, error) {
	return xPropsPrincipal, nil
}

func (b *xPropsBackend) CalendarHomeSetPath(ctx context.Context) (string, error) {
	return xPropsHomeSet, nil
}

func (b *xPropsBackend) CreateCalendar(ctx context.Context, calendar *Calendar) error {
	return webdav.NewHTTPError(http.StatusForbidden, nil)
}

func (b *xPropsBackend) ListCalendars(ctx context.Context) ([]Calendar, error) {
	return []Calendar{{Path: xPropsCalendar, SupportedComponentSet: []string{ical.CompToDo}, SyncToken: "1"}}, nil
}

func (b *xPropsBackend) GetCalendar(ctx context.Context, path string) (*Calendar, error) {
	if path != xPropsCalendar {
		return nil, webdav.NewHTTPError(http.StatusNotFound, nil)
	}
	cals, _ := b.ListCalendars(ctx)
	return &cals[0], nil
}

func (b *xPropsBackend) object(path string) (*CalendarObject, bool) {
	b.mu.Lock()
	defer b.mu.Unlock()
	cal, ok := b.objects[path]
	if !ok {
		return nil, false
	}
	return &CalendarObject{Path: path, ETag: "1", Data: cal}, true
}

func (b *xPropsBackend) GetCalendarObject(ctx context.Context, path string, req *CalendarCompRequest) (*CalendarObject, error) {
	co, ok := b.object(path)
	if !ok {
		return nil, webdav.NewHTTPError(http.StatusNotFound, nil)
	}
	return co, nil
}

func (b *xPropsBackend) ListCalendarObjects(ctx context.Context, path string, req *CalendarCompRequest) ([]CalendarObject, error) {
	if path != xPropsCalendar {
		return nil, webdav.NewHTTPError(http.StatusNotFound, nil)
	}
	co, ok := b.object(xPropsObject)
	if !ok {
		return nil, nil
	}
	return []CalendarObject{*co}, nil
}

func (b *xPropsBackend) QueryCalendarObjects(ctx context.Context, path string, query *CalendarQuery) ([]CalendarObject, error) {
	cos, err := b.ListCalendarObjects(ctx, path, &query.CompRequest)
	if err != nil {
		return nil, err
	}
	return Filter(query, cos)
}

func (b *xPropsBackend) PutCalendarObject(ctx context.Context, path string, calendar *ical.Calendar, opts *PutCalendarObjectOptions) (*CalendarObject, error) {
	if path != xPropsObject {
		return nil, webdav.NewHTTPError(http.StatusForbidden, nil)
	}
	if _, _, err := ValidateCalendarObject(calendar); err != nil {
		return nil, webdav.NewHTTPError(http.StatusBadRequest, err)
	}
	b.mu.Lock()
	b.objects[path] = calendar
	b.mu.Unlock()
	co, _ := b.object(path)
	return co, nil
}

func (b *xPropsBackend) DeleteCalendarObject(ctx context.Context, path string) error {
	return webdav.NewHTTPError(http.StatusForbidden, nil)
}

func (b *xPropsBackend) SyncCollection(ctx context.Context, path string, query *SyncQuery) (*SyncResponse, error) {
	cos, err := b.ListCalendarObjects(ctx, path, &query.CompRequest)
	if err != nil {
		return nil, err
	}
	return &SyncResponse{SyncToken: "1", Updated: cos}, nil
}

func newXPropsServer(t *testing.T) (*httptest.Server, *xPropsBackend) {
	b := &xPropsBackend{objects: make(map[string]*ical.Calendar)}
	ts := httptest.NewServer(&Handler{Backend: b})
	t.Cleanup(ts.Close)
	return ts, b
}

func doXPropsRequest(t *testing.T, ts *httptest.Server, method, path, contentType, body string, header ...string) (*http.Response, string) {
	t.Helper()
	req, err := http.NewRequest(method, ts.URL+path, strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	for i := 0; i+1 < len(header); i += 2 {
		req.Header.Set(header[i], header[i+1])
	}
	resp, err := ts.Client().Do(req)
	if err != nil {
		t.Fatalf("%v %v: %v", method, path, err)
	}
	defer resp.Body.Close()
	b, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	return resp, string(b)
}

// TestServerExperimentalProperties sends a calendar object with experimental
// properties and parameters to the server as a client would, and checks they
// come back unchanged in GET responses and REPORT calendar data, and that
// calendar queries can filter on them.
func TestServerExperimentalProperties(t *testing.T) {
	ts, b := newXPropsServer(t)

	resp, body := doXPropsRequest(t, ts, http.MethodPut, xPropsObject, ical.MIMEType, xPropsICS)
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("PUT: got status %v, want 201 Created: %v", resp.Status, body)
	}
	co, ok := b.object(xPropsObject)
	if !ok {
		t.Fatalf("PUT: the backend didn't store the object")
	}
	checkXProps(t, co.Data)

	resp, body = doXPropsRequest(t, ts, http.MethodGet, xPropsObject, "", "")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET: got status %v, want 200 OK: %v", resp.Status, body)
	}
	if !strings.Contains(body, "\r\nX-APPLE-SORT-ORDER:690545645\r\n") {
		t.Errorf("GET: X-APPLE-SORT-ORDER missing from the response:\n%v", body)
	}
	cal, err := ical.NewDecoder(strings.NewReader(body)).Decode()
	if err != nil {
		t.Fatalf("GET: decoding the response: %v", err)
	}
	checkXProps(t, cal)

	for _, tc := range []struct {
		name, body string
		want       bool
	}{
		{
			name: "calendar-multiget",
			body: `<?xml version="1.0" encoding="utf-8"?>
<C:calendar-multiget xmlns:D="DAV:" xmlns:C="urn:ietf:params:xml:ns:caldav">
  <D:prop><D:getetag/><C:calendar-data/></D:prop>
  <D:href>` + xPropsObject + `</D:href>
</C:calendar-multiget>`,
			want: true,
		},
		{
			name: "calendar-query on an x-prop",
			body: `<?xml version="1.0" encoding="utf-8"?>
<C:calendar-query xmlns:D="DAV:" xmlns:C="urn:ietf:params:xml:ns:caldav">
  <D:prop><C:calendar-data/></D:prop>
  <C:filter>
    <C:comp-filter name="VCALENDAR">
      <C:comp-filter name="VTODO">
        <C:prop-filter name="X-APPLE-SORT-ORDER">
          <C:text-match>690545645</C:text-match>
        </C:prop-filter>
        <C:prop-filter name="X-APPLE-STRUCTURED-LOCATION">
          <C:param-filter name="X-TITLE">
            <C:text-match>Apple Park</C:text-match>
          </C:param-filter>
        </C:prop-filter>
      </C:comp-filter>
    </C:comp-filter>
  </C:filter>
</C:calendar-query>`,
			want: true,
		},
		{
			name: "calendar-query on another x-prop value",
			body: `<?xml version="1.0" encoding="utf-8"?>
<C:calendar-query xmlns:D="DAV:" xmlns:C="urn:ietf:params:xml:ns:caldav">
  <D:prop><C:calendar-data/></D:prop>
  <C:filter>
    <C:comp-filter name="VCALENDAR">
      <C:comp-filter name="VTODO">
        <C:prop-filter name="X-APPLE-SORT-ORDER">
          <C:text-match>12345</C:text-match>
        </C:prop-filter>
      </C:comp-filter>
    </C:comp-filter>
  </C:filter>
</C:calendar-query>`,
			want: false,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			resp, body := doXPropsRequest(t, ts, "REPORT", xPropsCalendar, "application/xml", tc.body, "Depth", "1")
			if resp.StatusCode != http.StatusMultiStatus {
				t.Fatalf("got status %v, want 207 Multi-Status: %v", resp.Status, body)
			}
			got := strings.Contains(body, "X-APPLE-SORT-ORDER:690545645")
			if got != tc.want {
				t.Errorf("calendar data with X-APPLE-SORT-ORDER in the response: got %v, want %v:\n%v", got, tc.want, body)
			}
			if tc.want && !strings.Contains(body, "X-TITLE=Apple Park") {
				t.Errorf("X-APPLE-STRUCTURED-LOCATION parameters missing from the response:\n%v", body)
			}
		})
	}
}

// TestClientExperimentalProperties checks that the client sends and receives
// the experimental properties and parameters of calendar objects unchanged.
func TestClientExperimentalProperties(t *testing.T) {
	ts, b := newXPropsServer(t)
	ctx := context.Background()

	c, err := NewClient(ts.Client(), ts.URL)
	if err != nil {
		t.Fatal(err)
	}

	if _, err := c.PutCalendarObject(ctx, xPropsObject, decodeXPropsICS(t), nil); err != nil {
		t.Fatalf("PutCalendarObject: %v", err)
	}
	co, ok := b.object(xPropsObject)
	if !ok {
		t.Fatalf("PutCalendarObject: the backend didn't store the object")
	}
	checkXProps(t, co.Data)

	t.Run("GetCalendarObject", func(t *testing.T) {
		co, err := c.GetCalendarObject(ctx, xPropsObject)
		if err != nil {
			t.Fatal(err)
		}
		checkXProps(t, co.Data)
	})

	allData := CalendarCompRequest{Name: ical.CompCalendar, AllProps: true, AllComps: true}

	t.Run("MultiGetCalendar", func(t *testing.T) {
		cos, err := c.MultiGetCalendar(ctx, xPropsCalendar, &CalendarMultiGet{
			Paths:       []string{xPropsObject},
			CompRequest: allData,
		})
		if err != nil {
			t.Fatal(err)
		}
		if len(cos) != 1 {
			t.Fatalf("got %v calendar objects, want 1", len(cos))
		}
		checkXProps(t, cos[0].Data)
	})

	for _, tc := range []struct {
		name  string
		props []PropFilter
		want  int
	}{
		{
			name: "matching",
			props: []PropFilter{
				{Name: "X-APPLE-SORT-ORDER", TextMatch: &TextMatch{Text: "690545645"}},
				{Name: "X-APPLE-STRUCTURED-LOCATION", ParamFilter: []ParamFilter{
					{Name: "X-ADDRESS", TextMatch: &TextMatch{Text: "Cupertino"}},
				}},
				{Name: "x-vendor-flags", TextMatch: &TextMatch{Text: "pinned"}},
			},
			want: 1,
		},
		{
			name:  "not matching",
			props: []PropFilter{{Name: "X-APPLE-SORT-ORDER", TextMatch: &TextMatch{Text: "12345"}}},
			want:  0,
		},
	} {
		t.Run(fmt.Sprintf("QueryCalendar %v", tc.name), func(t *testing.T) {
			cos, err := c.QueryCalendar(ctx, xPropsCalendar, &CalendarQuery{
				CompRequest: allData,
				CompFilter: CompFilter{
					Name:  ical.CompCalendar,
					Comps: []CompFilter{{Name: ical.CompToDo, Props: tc.props}},
				},
			})
			if err != nil {
				t.Fatal(err)
			}
			if len(cos) != tc.want {
				t.Fatalf("got %v calendar objects, want %v", len(cos), tc.want)
			}
			if tc.want > 0 {
				checkXProps(t, cos[0].Data)
			}
		})
	}

	t.Run("SyncCollection", func(t *testing.T) {
		res, err := c.SyncCollection(ctx, xPropsCalendar, &SyncQuery{CompRequest: allData})
		if err != nil {
			t.Fatal(err)
		}
		if len(res.Updated) != 1 {
			t.Fatalf("got %v updated calendar objects, want 1", len(res.Updated))
		}
		checkXProps(t, res.Updated[0].Data)
	})
}
