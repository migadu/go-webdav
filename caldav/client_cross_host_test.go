package caldav

import (
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync"
	"testing"
)

// fakeHost answers the requests for a host name with canned responses, by
// method and request URI, and records the other requests it gets.
type fakeHost struct {
	name   string                      // e.g. "dav.test"
	routes map[string]http.HandlerFunc // e.g. "GET /a.ics"

	mu         sync.Mutex
	unexpected []string
}

func (h *fakeHost) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	key := r.Method + " " + r.URL.RequestURI()
	if f, ok := h.routes[key]; ok {
		f(w, r)
		return
	}
	h.mu.Lock()
	h.unexpected = append(h.unexpected, key)
	h.mu.Unlock()
	http.Error(w, "unexpected request to "+h.name, http.StatusNotFound)
}

// newFakeHostsClient starts a server for each host, and returns an HTTP client
// reaching them by their names on port 80. The test fails if a host gets a
// request it has no response for.
func newFakeHostsClient(t *testing.T, hosts ...*fakeHost) *http.Client {
	addrs := make(map[string]string)
	for _, h := range hosts {
		ts := httptest.NewServer(h)
		t.Cleanup(ts.Close)
		t.Cleanup(func() {
			h.mu.Lock()
			defer h.mu.Unlock()
			if len(h.unexpected) > 0 {
				t.Errorf("%v got unexpected requests: %q", h.name, h.unexpected)
			}
		})
		addrs[h.name+":80"] = ts.Listener.Addr().String()
	}

	var dialer net.Dialer
	transport := &http.Transport{
		DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
			a, ok := addrs[addr]
			if !ok {
				return nil, fmt.Errorf("unknown host %v", addr)
			}
			return dialer.DialContext(ctx, network, a)
		},
	}
	t.Cleanup(transport.CloseIdleConnections)
	return &http.Client{Transport: transport}
}

func multiStatusHandler(body string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", `application/xml; charset="utf-8"`)
		w.WriteHeader(http.StatusMultiStatus)
		io.WriteString(w, `<?xml version="1.0" encoding="utf-8"?>
<d:multistatus xmlns:d="DAV:" xmlns:cal="urn:ietf:params:xml:ns:caldav">`+body+`</d:multistatus>`)
	}
}

// reportHandler answers REPORT requests with the multi-status body for the
// report's name, found in the request body.
func reportHandler(t *testing.T, bodies map[string]string, check func(report, reqBody string)) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		b, err := io.ReadAll(r.Body)
		if err != nil {
			t.Errorf("failed to read REPORT body: %v", err)
		}
		for report, body := range bodies {
			if strings.Contains(string(b), report) {
				if check != nil {
					check(report, string(b))
				}
				multiStatusHandler(body)(w, r)
				return
			}
		}
		t.Errorf("unexpected REPORT %v: %s", r.URL, b)
		http.Error(w, "unexpected REPORT", http.StatusBadRequest)
	}
}

func propStat(props string) string {
	return `<d:propstat><d:prop>` + props + `</d:prop><d:status>HTTP/1.1 200 OK</d:status></d:propstat>`
}

const crossHostEvent = "BEGIN:VCALENDAR\nVERSION:2.0\nPRODID:-//test//EN\n" +
	"BEGIN:VEVENT\nUID:event\nDTSTAMP:20260101T000000Z\nDTSTART:20260101T100000Z\nSUMMARY:Meeting\nEND:VEVENT\n" +
	"END:VCALENDAR\n"

func icalHandler(etag string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/calendar")
		w.Header().Set("ETag", etag)
		io.WriteString(w, crossHostEvent)
	}
}

const calendarResourceType = `<d:resourcetype><d:collection/><cal:calendar/></d:resourcetype><d:displayname>Work</d:displayname>`

// TestClientCrossHost reproduces iCloud, whose principal on caldav.icloud.com
// has its calendar home set on another host, e.g. p37-caldav.icloud.com,
// itself listing its resources with relative hrefs. Every request for the home
// set and its resources must go to that other host.
func TestClientCrossHost(t *testing.T) {
	const (
		calPath   = "/1234567/calendars/work/"
		eventPath = calPath + "event.ics"
	)
	const (
		homeSet = "http://p37-caldav.test/1234567/calendars/"
		cal     = "http://p37-caldav.test" + calPath
		event   = "http://p37-caldav.test" + eventPath
		other   = "http://p37-caldav.test" + calPath + "other.ics"
		gone    = "http://p37-caldav.test" + calPath + "gone.ics"
	)

	principalHost := &fakeHost{name: "caldav.test", routes: map[string]http.HandlerFunc{
		"PROPFIND /": multiStatusHandler(`<d:response><d:href>/</d:href>` +
			propStat(`<d:current-user-principal><d:href>/1234567/principal/</d:href></d:current-user-principal>`) +
			`</d:response>`),
		// The default port is explicit, as with iCloud
		"PROPFIND /1234567/principal/": multiStatusHandler(`<d:response><d:href>/1234567/principal/</d:href>` +
			propStat(`<cal:calendar-home-set><d:href>http://p37-caldav.test:80/1234567/calendars/</d:href></cal:calendar-home-set>`) +
			`</d:response>`),
	}}

	var multiGetBody string
	homeHost := &fakeHost{name: "p37-caldav.test", routes: map[string]http.HandlerFunc{
		"PROPFIND /1234567/calendars/": multiStatusHandler(`
			<d:response><d:href>/1234567/calendars/</d:href>` +
			propStat(`<d:resourcetype><d:collection/></d:resourcetype>`) + `</d:response>
			<d:response><d:href>/1234567/calendars/work/</d:href>` + propStat(calendarResourceType) + `</d:response>`),
		"PROPFIND " + calPath: multiStatusHandler(`<d:response><d:href>` + calPath + `</d:href>` +
			propStat(calendarResourceType) + `</d:response>`),
		"REPORT " + calPath: reportHandler(t, map[string]string{
			// A reference relative to the request URI
			"calendar-query": `<d:response><d:href>event.ics</d:href>` +
				propStat(`<d:getetag>"1"</d:getetag><cal:calendar-data>`+crossHostEvent+`</cal:calendar-data>`) +
				`</d:response>`,
			"calendar-multiget": `<d:response><d:href>` + eventPath + `</d:href>` +
				propStat(`<d:getetag>"1"</d:getetag><cal:calendar-data>`+crossHostEvent+`</cal:calendar-data>`) +
				`</d:response>`,
			"sync-collection": `
				<d:response><d:href>` + calPath + `</d:href>` + propStat(`<d:getetag>"0"</d:getetag>`) + `</d:response>
				<d:response><d:href>` + eventPath + `</d:href>` + propStat(`<d:getetag>"1"</d:getetag>`) + `</d:response>
				<d:response><d:href>` + calPath + `gone.ics</d:href><d:status>HTTP/1.1 404 Not Found</d:status></d:response>
				<d:sync-token>http://p37-caldav.test/sync/2</d:sync-token>`,
		}, func(report, reqBody string) {
			if report == "calendar-multiget" {
				multiGetBody = reqBody
			}
		}),
		"GET " + eventPath: icalHandler(`"1"`),
		"PUT " + calPath + "other.ics": func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("ETag", `"2"`)
			w.Header().Set("Location", calPath+"other.ics")
			w.WriteHeader(http.StatusCreated)
		},
	}}

	ctx := context.Background()
	c, err := NewClient(newFakeHostsClient(t, principalHost, homeHost), "http://caldav.test")
	if err != nil {
		t.Fatal(err)
	}

	principal, err := c.FindCurrentUserPrincipal(ctx)
	if err != nil {
		t.Fatalf("FindCurrentUserPrincipal() = %v", err)
	} else if principal != "/1234567/principal/" {
		t.Errorf("FindCurrentUserPrincipal() = %q, want %q", principal, "/1234567/principal/")
	}

	gotHomeSet, err := c.FindCalendarHomeSet(ctx, principal)
	if err != nil {
		t.Fatalf("FindCalendarHomeSet() = %v", err)
	} else if gotHomeSet != homeSet {
		t.Errorf("FindCalendarHomeSet() = %q, want %q", gotHomeSet, homeSet)
	}

	cals, err := c.FindCalendars(ctx, gotHomeSet)
	if err != nil {
		t.Fatalf("FindCalendars() = %v", err)
	}
	if len(cals) != 1 || cals[0].Path != cal || cals[0].Name != "Work" {
		t.Fatalf("FindCalendars() = %+v, want a calendar at %q", cals, cal)
	}

	found, err := c.FindCalendar(ctx, cals[0].Path)
	if err != nil {
		t.Fatalf("FindCalendar() = %v", err)
	}
	if found.Path != cal || found.Name != "Work" {
		t.Errorf("FindCalendar() = %+v, want a calendar at %q", found, cal)
	}

	objs, err := c.QueryCalendar(ctx, cals[0].Path, &CalendarQuery{CompFilter: CompFilter{Name: "VCALENDAR"}})
	if err != nil {
		t.Fatalf("QueryCalendar() = %v", err)
	}
	if len(objs) != 1 || objs[0].Path != event || objs[0].ETag != "1" {
		t.Fatalf("QueryCalendar() = %+v, want a calendar object at %q", objs, event)
	}

	co, err := c.GetCalendarObject(ctx, objs[0].Path)
	if err != nil {
		t.Fatalf("GetCalendarObject() = %v", err)
	}
	if co.Path != event || co.ETag != "1" {
		t.Errorf("GetCalendarObject() = %+v, want a calendar object at %q with ETag 1", co, event)
	}

	objs, err = c.MultiGetCalendar(ctx, cals[0].Path, &CalendarMultiGet{Paths: []string{event}})
	if err != nil {
		t.Fatalf("MultiGetCalendar() = %v", err)
	}
	if len(objs) != 1 || objs[0].Path != event {
		t.Errorf("MultiGetCalendar() = %+v, want a calendar object at %q", objs, event)
	}
	// The resources are on the host the request is sent to: their paths are
	// enough
	if want := ">" + eventPath + "<"; !strings.Contains(multiGetBody, want) {
		t.Errorf("calendar-multiget body = %s, want an href %q", multiGetBody, eventPath)
	}

	co, err = c.PutCalendarObject(ctx, cals[0].Path+"other.ics", co.Data, nil)
	if err != nil {
		t.Fatalf("PutCalendarObject() = %v", err)
	}
	if co.Path != other || co.ETag != "2" {
		t.Errorf("PutCalendarObject() = %+v, want a calendar object at %q with ETag 2", co, other)
	}

	syncRes, err := c.SyncCollection(ctx, cals[0].Path, &SyncQuery{})
	if err != nil {
		t.Fatalf("SyncCollection() = %v", err)
	}
	if len(syncRes.Updated) != 1 || syncRes.Updated[0].Path != event {
		t.Errorf("SyncCollection() updated %+v, want a calendar object at %q", syncRes.Updated, event)
	}
	if want := []string{gone}; !reflect.DeepEqual(syncRes.Deleted, want) {
		t.Errorf("SyncCollection() deleted %q, want %q", syncRes.Deleted, want)
	}
}

// TestClientSameHostAbsoluteHrefs checks that absolute hrefs on the endpoint's
// host are reduced to their paths, even with another scheme or port, as
// reported by some servers behind a reverse proxy.
func TestClientSameHostAbsoluteHrefs(t *testing.T) {
	const (
		calPath   = "/1234567/calendars/work/"
		eventPath = calPath + "event.ics"
	)

	host := &fakeHost{name: "dav.test", routes: map[string]http.HandlerFunc{
		"PROPFIND /": multiStatusHandler(`<d:response><d:href>http://dav.test/</d:href>` +
			propStat(`<d:current-user-principal><d:href>http://dav.test/1234567/principal/</d:href></d:current-user-principal>`) +
			`</d:response>`),
		"PROPFIND /1234567/principal/": multiStatusHandler(`<d:response><d:href>http://dav.test/1234567/principal/</d:href>` +
			propStat(`<cal:calendar-home-set><d:href>http://DAV.test:80/1234567/calendars/</d:href></cal:calendar-home-set>`) +
			`</d:response>`),
		"PROPFIND /1234567/calendars/": multiStatusHandler(`
			<d:response><d:href>http://dav.test/1234567/calendars/</d:href>` +
			propStat(`<d:resourcetype><d:collection/></d:resourcetype>`) + `</d:response>
			<d:response><d:href>https://dav.test:8443/1234567/calendars/work/</d:href>` + propStat(calendarResourceType) +
			`</d:response>`),
		"REPORT " + calPath: reportHandler(t, map[string]string{
			"calendar-query": `<d:response><d:href>http://dav.test` + eventPath + `</d:href>` +
				propStat(`<d:getetag>"1"</d:getetag><cal:calendar-data>`+crossHostEvent+`</cal:calendar-data>`) +
				`</d:response>`,
		}, nil),
		"GET " + eventPath: icalHandler(`"1"`),
	}}

	ctx := context.Background()
	c, err := NewClient(newFakeHostsClient(t, host), "http://dav.test/")
	if err != nil {
		t.Fatal(err)
	}

	principal, err := c.FindCurrentUserPrincipal(ctx)
	if err != nil {
		t.Fatalf("FindCurrentUserPrincipal() = %v", err)
	} else if principal != "/1234567/principal/" {
		t.Errorf("FindCurrentUserPrincipal() = %q, want %q", principal, "/1234567/principal/")
	}

	homeSet, err := c.FindCalendarHomeSet(ctx, principal)
	if err != nil {
		t.Fatalf("FindCalendarHomeSet() = %v", err)
	} else if homeSet != "/1234567/calendars/" {
		t.Errorf("FindCalendarHomeSet() = %q, want %q", homeSet, "/1234567/calendars/")
	}

	cals, err := c.FindCalendars(ctx, homeSet)
	if err != nil {
		t.Fatalf("FindCalendars() = %v", err)
	}
	if len(cals) != 1 || cals[0].Path != calPath {
		t.Fatalf("FindCalendars() = %+v, want a calendar at %q", cals, calPath)
	}

	objs, err := c.QueryCalendar(ctx, cals[0].Path, &CalendarQuery{CompFilter: CompFilter{Name: "VCALENDAR"}})
	if err != nil {
		t.Fatalf("QueryCalendar() = %v", err)
	}
	if len(objs) != 1 || objs[0].Path != eventPath {
		t.Fatalf("QueryCalendar() = %+v, want a calendar object at %q", objs, eventPath)
	}

	co, err := c.GetCalendarObject(ctx, objs[0].Path)
	if err != nil {
		t.Fatalf("GetCalendarObject() = %v", err)
	}
	if co.Path != eventPath {
		t.Errorf("GetCalendarObject() = %+v, want a calendar object at %q", co, eventPath)
	}
}
