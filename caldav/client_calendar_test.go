package caldav

import (
	"context"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"sort"
	"strings"
	"sync"
	"testing"

	"github.com/emersion/go-ical"
	"github.com/emersion/go-webdav"
)

const parisTimezone = "BEGIN:VCALENDAR\r\n" +
	"VERSION:2.0\r\n" +
	"PRODID:-//Example Corp.//CalDAV Client//EN\r\n" +
	"BEGIN:VTIMEZONE\r\n" +
	"TZID:Europe/Paris\r\n" +
	"BEGIN:DAYLIGHT\r\n" +
	"TZOFFSETFROM:+0100\r\n" +
	"RRULE:FREQ=YEARLY;BYMONTH=3;BYDAY=-1SU\r\n" +
	"DTSTART:19810329T020000\r\n" +
	"TZNAME:CEST\r\n" +
	"TZOFFSETTO:+0200\r\n" +
	"END:DAYLIGHT\r\n" +
	"BEGIN:STANDARD\r\n" +
	"TZOFFSETFROM:+0200\r\n" +
	"RRULE:FREQ=YEARLY;BYMONTH=10;BYDAY=-1SU\r\n" +
	"DTSTART:19961027T030000\r\n" +
	"TZNAME:CET\r\n" +
	"TZOFFSETTO:+0100\r\n" +
	"END:STANDARD\r\n" +
	"END:VTIMEZONE\r\n" +
	"END:VCALENDAR\r\n"

// twoTimezones is parisTimezone with a second VTIMEZONE component, which the
// CALDAV:calendar-timezone property can't hold (RFC 4791 section 5.2.2).
var twoTimezones = strings.Replace(parisTimezone, "END:VTIMEZONE\r\n", "END:VTIMEZONE\r\n"+
	"BEGIN:VTIMEZONE\r\nTZID:UTC\r\nBEGIN:STANDARD\r\nDTSTART:19700101T000000\r\n"+
	"TZOFFSETFROM:+0000\r\nTZOFFSETTO:+0000\r\nEND:STANDARD\r\nEND:VTIMEZONE\r\n", 1)

func decodeICal(t *testing.T, s string) *ical.Calendar {
	t.Helper()
	cal, err := ical.NewDecoder(strings.NewReader(s)).Decode()
	if err != nil {
		t.Fatal(err)
	}
	return cal
}

func timezoneID(cal *ical.Calendar) string {
	if cal == nil || len(cal.Children) == 0 {
		return ""
	}
	if prop := cal.Children[0].Props.Get(ical.PropTimezoneID); prop != nil {
		return prop.Value
	}
	return ""
}

// memCalendarBackend stores calendars in memory, and lets clients create,
// update and delete them.
type memCalendarBackend struct {
	testBackend

	mu        sync.Mutex
	calendars map[string]Calendar
	updates   []CalendarUpdate
	// updateErr, if set, is returned by UpdateCalendar
	updateErr error
}

var _ CalendarUpdater = (*memCalendarBackend)(nil)

func newMemCalendarBackend() *memCalendarBackend {
	return &memCalendarBackend{calendars: make(map[string]Calendar)}
}

func (b *memCalendarBackend) CreateCalendar(ctx context.Context, cal *Calendar) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	if _, ok := b.calendars[cal.Path]; ok {
		return webdav.NewHTTPError(http.StatusMethodNotAllowed, fmt.Errorf("calendar %q already exists", cal.Path))
	}
	b.calendars[cal.Path] = *cal
	return nil
}

func (b *memCalendarBackend) ListCalendars(ctx context.Context) ([]Calendar, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	l := make([]Calendar, 0, len(b.calendars))
	for _, cal := range b.calendars {
		l = append(l, cal)
	}
	sort.Slice(l, func(i, j int) bool { return l[i].Path < l[j].Path })
	return l, nil
}

func (b *memCalendarBackend) GetCalendar(ctx context.Context, path string) (*Calendar, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	cal, ok := b.calendars[path]
	if !ok {
		return nil, webdav.NewHTTPError(http.StatusNotFound, fmt.Errorf("no calendar at %q", path))
	}
	return &cal, nil
}

func (b *memCalendarBackend) DeleteCalendarObject(ctx context.Context, path string) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	if _, ok := b.calendars[path]; !ok {
		return webdav.NewHTTPError(http.StatusNotFound, fmt.Errorf("no calendar at %q", path))
	}
	delete(b.calendars, path)
	return nil
}

func (b *memCalendarBackend) UpdateCalendar(ctx context.Context, path string, update *CalendarUpdate) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.updateErr != nil {
		return b.updateErr
	}
	cal, ok := b.calendars[path]
	if !ok {
		return webdav.NewHTTPError(http.StatusNotFound, fmt.Errorf("no calendar at %q", path))
	}
	b.updates = append(b.updates, *update)
	if update.Name != nil {
		cal.Name = *update.Name
	}
	if update.Description != nil {
		cal.Description = *update.Description
	}
	if update.Color != nil {
		cal.Color = *update.Color
	}
	if update.Timezone != nil {
		cal.Timezone = update.Timezone
	} else if update.RemoveTimezone {
		cal.Timezone = nil
	}
	b.calendars[path] = cal
	return nil
}

func newMemCalendarClient(t *testing.T) (*Client, *memCalendarBackend) {
	t.Helper()
	backend := newMemCalendarBackend()
	ts := httptest.NewServer(&Handler{Backend: backend})
	t.Cleanup(ts.Close)
	c, err := NewClient(nil, ts.URL)
	if err != nil {
		t.Fatal(err)
	}
	return c, backend
}

func TestClientCalendarLifecycle(t *testing.T) {
	c, backend := newMemCalendarClient(t)
	ctx := context.Background()
	const calPath = "/user/calendars/work/"

	err := c.CreateCalendar(ctx, &Calendar{
		Path:                  calPath,
		Name:                  "Work",
		Description:           "Meetings and such",
		Color:                 "#FF2968",
		Timezone:              decodeICal(t, parisTimezone),
		SupportedComponentSet: []string{ical.CompEvent, ical.CompToDo},
		// Protected, so not sent
		MaxResourceSize: 1024,
	})
	if err != nil {
		t.Fatalf("CreateCalendar() = %v", err)
	}

	cal, err := c.FindCalendar(ctx, calPath)
	if err != nil {
		t.Fatalf("FindCalendar() after CreateCalendar = %v", err)
	}
	if cal.Path != calPath || cal.Name != "Work" || cal.Description != "Meetings and such" || cal.Color != "#FF2968" {
		t.Errorf("FindCalendar() = %+v, want the path, name, description and color it was created with", cal)
	}
	if want := []string{ical.CompEvent, ical.CompToDo}; !reflect.DeepEqual(cal.SupportedComponentSet, want) {
		t.Errorf("FindCalendar() components = %v, want %v", cal.SupportedComponentSet, want)
	}
	if id := timezoneID(cal.Timezone); id != "Europe/Paris" {
		t.Errorf("FindCalendar() timezone = %q, want Europe/Paris", id)
	}
	if cal.MaxResourceSize != 0 {
		t.Errorf("FindCalendar() max resource size = %v, want none", cal.MaxResourceSize)
	}

	// Change the name, remove the description and the timezone, leave the
	// color as it is
	name, empty := "Office", ""
	err = c.UpdateCalendar(ctx, calPath, &CalendarUpdate{
		Name:           &name,
		Description:    &empty,
		RemoveTimezone: true,
	})
	if err != nil {
		t.Fatalf("UpdateCalendar() = %v", err)
	}
	wantUpdates := []CalendarUpdate{{Name: &name, Description: &empty, RemoveTimezone: true}}
	if !reflect.DeepEqual(backend.updates, wantUpdates) {
		t.Errorf("backend updates = %+v, want %+v", backend.updates, wantUpdates)
	}

	cal, err = c.FindCalendar(ctx, calPath)
	if err != nil {
		t.Fatalf("FindCalendar() after UpdateCalendar = %v", err)
	}
	if cal.Name != "Office" || cal.Description != "" || cal.Timezone != nil || cal.Color != "#FF2968" {
		t.Errorf("FindCalendar() after UpdateCalendar = %+v, want the new name, the color and nothing else", cal)
	}

	// Setting the timezone again
	err = c.UpdateCalendar(ctx, calPath, &CalendarUpdate{Timezone: decodeICal(t, parisTimezone)})
	if err != nil {
		t.Fatalf("UpdateCalendar() with a timezone = %v", err)
	}
	if cal, err := c.FindCalendar(ctx, calPath); err != nil {
		t.Fatalf("FindCalendar() = %v", err)
	} else if id := timezoneID(cal.Timezone); id != "Europe/Paris" {
		t.Errorf("FindCalendar() timezone = %q, want Europe/Paris", id)
	}

	// An update without changes sends no request
	if err := c.UpdateCalendar(ctx, calPath, &CalendarUpdate{}); err != nil {
		t.Errorf("UpdateCalendar() without changes = %v", err)
	}
	if len(backend.updates) != 2 {
		t.Errorf("backend got %v updates, want 2", len(backend.updates))
	}

	cals, err := c.FindCalendars(ctx, "/user/calendars/")
	if err != nil {
		t.Fatalf("FindCalendars() = %v", err)
	}
	if len(cals) != 1 || cals[0].Path != calPath {
		t.Errorf("FindCalendars() = %+v, want only %v", cals, calPath)
	}

	if err := c.RemoveAll(ctx, calPath); err != nil {
		t.Fatalf("RemoveAll() = %v", err)
	}
	_, err = c.FindCalendar(ctx, calPath)
	var httpErr *webdav.HTTPError
	if !errors.As(err, &httpErr) || httpErr.Code != http.StatusNotFound {
		t.Errorf("FindCalendar() after RemoveAll = %v, want a 404 HTTPError", err)
	}
}

func TestClientCreateCalendarWithoutProps(t *testing.T) {
	c, backend := newMemCalendarClient(t)
	ctx := context.Background()

	if err := c.CreateCalendar(ctx, &Calendar{Path: "/user/calendars/default/"}); err != nil {
		t.Fatalf("CreateCalendar() = %v", err)
	}
	if _, ok := backend.calendars["/user/calendars/default/"]; !ok {
		t.Errorf("backend calendars = %+v, want one at /user/calendars/default/", backend.calendars)
	}

	if err := c.CreateCalendar(ctx, &Calendar{}); err == nil {
		t.Errorf("CreateCalendar() without a path succeeded")
	}
}

func TestClientCreateCalendarErrors(t *testing.T) {
	c, backend := newMemCalendarClient(t)
	ctx := context.Background()
	const calPath = "/user/calendars/work/"

	if err := c.CreateCalendar(ctx, &Calendar{Path: calPath, Name: "Work"}); err != nil {
		t.Fatalf("CreateCalendar() = %v", err)
	}

	// The backend refuses to replace the calendar
	err := c.CreateCalendar(ctx, &Calendar{Path: calPath, Name: "Other"})
	var httpErr *webdav.HTTPError
	if !errors.As(err, &httpErr) || httpErr.Code != http.StatusMethodNotAllowed {
		t.Errorf("CreateCalendar() on an existing calendar = %v, want a 405 HTTPError", err)
	}
	if cal := backend.calendars[calPath]; cal.Name != "Work" {
		t.Errorf("calendar = %+v, want it unchanged", cal)
	}

	// The server only creates calendars in the calendar home set
	err = c.CreateCalendar(ctx, &Calendar{Path: "/user/calendars/", Name: "Home"})
	if !errors.As(err, &httpErr) || httpErr.Code != http.StatusForbidden {
		t.Errorf("CreateCalendar() on the calendar home set = %v, want a 403 HTTPError", err)
	} else if !strings.Contains(err.Error(), string(PreconditionCalendarCollectionLocationOk)) {
		t.Errorf("CreateCalendar() on the calendar home set = %v, want the %v precondition", err, PreconditionCalendarCollectionLocationOk)
	}

	// The server checks the timezone (RFC 4791 section 5.3.1.1)
	err = c.CreateCalendar(ctx, &Calendar{Path: "/user/calendars/tz/", Timezone: decodeICal(t, twoTimezones)})
	if !errors.As(err, &httpErr) || httpErr.Code != http.StatusConflict {
		t.Errorf("CreateCalendar() with two timezones = %v, want a 409 HTTPError", err)
	} else if !strings.Contains(err.Error(), string(PreconditionValidCalendarData)) {
		t.Errorf("CreateCalendar() with two timezones = %v, want the %v precondition", err, PreconditionValidCalendarData)
	}
	if _, ok := backend.calendars["/user/calendars/tz/"]; ok {
		t.Errorf("calendar with two timezones was created")
	}
}

func TestClientUpdateCalendarErrors(t *testing.T) {
	c, backend := newMemCalendarClient(t)
	ctx := context.Background()
	const calPath = "/user/calendars/work/"

	if err := c.CreateCalendar(ctx, &Calendar{Path: calPath, Name: "Work"}); err != nil {
		t.Fatalf("CreateCalendar() = %v", err)
	}
	name := "Office"

	// The server refuses a property: the error names it with its status,
	// before the other property, which got 424 Failed Dependency
	err := c.UpdateCalendar(ctx, calPath, &CalendarUpdate{Name: &name, Timezone: decodeICal(t, twoTimezones)})
	var httpErr *webdav.HTTPError
	if !errors.As(err, &httpErr) || httpErr.Code != http.StatusConflict {
		t.Errorf("UpdateCalendar() with two timezones = %v, want a 409 HTTPError", err)
	}
	if err != nil {
		msg := err.Error()
		tzIndex := strings.Index(msg, "calendar-timezone")
		nameIndex := strings.Index(msg, "displayname")
		if tzIndex < 0 || nameIndex < tzIndex || !strings.Contains(msg, "424") || !strings.Contains(msg, string(PreconditionValidCalendarData)) {
			t.Errorf("UpdateCalendar() = %q, want the calendar-timezone error, then the displayname 424", msg)
		}
	}

	// The backend refuses the update as a whole
	backend.updateErr = webdav.NewHTTPError(http.StatusInsufficientStorage, errors.New("quota exceeded"))
	err = c.UpdateCalendar(ctx, calPath, &CalendarUpdate{Name: &name})
	if !errors.As(err, &httpErr) || httpErr.Code != http.StatusInsufficientStorage {
		t.Errorf("UpdateCalendar() refused by the backend = %v, want a 507 HTTPError", err)
	}
	backend.updateErr = nil

	if len(backend.updates) != 0 {
		t.Errorf("backend updates = %+v, want none", backend.updates)
	}
	if cal, err := c.FindCalendar(ctx, calPath); err != nil {
		t.Fatalf("FindCalendar() = %v", err)
	} else if cal.Name != "Work" {
		t.Errorf("FindCalendar() = %+v, want it unchanged", cal)
	}

	// No calendar there
	err = c.UpdateCalendar(ctx, "/user/calendars/missing/", &CalendarUpdate{Name: &name})
	if !errors.As(err, &httpErr) || httpErr.Code != http.StatusNotFound {
		t.Errorf("UpdateCalendar() of a missing calendar = %v, want a 404 HTTPError", err)
	}

	// Contradictory fields
	err = c.UpdateCalendar(ctx, calPath, &CalendarUpdate{Timezone: decodeICal(t, parisTimezone), RemoveTimezone: true})
	if err == nil {
		t.Errorf("UpdateCalendar() setting and removing the timezone succeeded")
	}
}

// xmlElements returns the elements of an XML document, each as the path of
// expanded names leading to it, along with the attributes and character data
// of each.
func xmlElements(t *testing.T, body []byte) (paths []string, attrs map[string][]xml.Attr, text map[string]string) {
	t.Helper()
	attrs = make(map[string][]xml.Attr)
	text = make(map[string]string)
	d := xml.NewDecoder(strings.NewReader(string(body)))
	var stack []string
	for {
		tok, err := d.Token()
		if err == io.EOF {
			break
		} else if err != nil {
			t.Fatalf("malformed XML %q: %v", body, err)
		}
		switch tok := tok.(type) {
		case xml.StartElement:
			stack = append(stack, "{"+tok.Name.Space+"}"+tok.Name.Local)
			p := strings.Join(stack, "/")
			paths = append(paths, p)
			for _, attr := range tok.Attr {
				if attr.Name.Space != "xmlns" && attr.Name.Local != "xmlns" {
					attrs[p] = append(attrs[p], attr)
				}
			}
		case xml.CharData:
			if len(stack) > 0 {
				text[strings.Join(stack, "/")] += string(tok)
			}
		case xml.EndElement:
			stack = stack[:len(stack)-1]
		}
	}
	return paths, attrs, text
}

// The request body is a CALDAV:mkcalendar element, as in the example of RFC
// 4791 section 5.3.1.2.
func TestClientCreateCalendarRequest(t *testing.T) {
	var method, path, contentType string
	var body []byte
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		method, path, contentType = r.Method, r.URL.Path, r.Header.Get("Content-Type")
		var err error
		if body, err = io.ReadAll(r.Body); err != nil {
			t.Errorf("failed to read request body: %v", err)
		}
		w.WriteHeader(http.StatusCreated)
	}))
	defer ts.Close()
	c, err := NewClient(nil, ts.URL)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()

	err = c.CreateCalendar(ctx, &Calendar{
		Path:                  "/home/lisa/calendars/events/",
		Name:                  "Lisa's Events",
		Description:           "Calendar restricted to events.",
		SupportedComponentSet: []string{ical.CompEvent, ical.CompToDo},
		Timezone:              decodeICal(t, parisTimezone),
		Color:                 "#FF2968",
		MaxResourceSize:       1024,
		SyncToken:             "sync-1",
		CTag:                  "ctag-1",
		ReadOnly:              true,
	})
	if err != nil {
		t.Fatalf("CreateCalendar() = %v", err)
	}
	if method != "MKCALENDAR" || path != "/home/lisa/calendars/events/" {
		t.Errorf("request = %v %v, want MKCALENDAR /home/lisa/calendars/events/", method, path)
	}
	if !strings.HasPrefix(contentType, "text/xml") && !strings.HasPrefix(contentType, "application/xml") {
		t.Errorf("Content-Type = %q, want an XML media type", contentType)
	}
	if !strings.HasPrefix(string(body), "<?xml") {
		t.Errorf("request body %q doesn't start with an XML declaration", body)
	}

	const (
		mkcalendar = "{urn:ietf:params:xml:ns:caldav}mkcalendar"
		prop       = mkcalendar + "/{DAV:}set/{DAV:}prop"
		compSet    = prop + "/{urn:ietf:params:xml:ns:caldav}supported-calendar-component-set"
		comp       = compSet + "/{urn:ietf:params:xml:ns:caldav}comp"
		tz         = prop + "/{urn:ietf:params:xml:ns:caldav}calendar-timezone"
	)
	paths, attrs, text := xmlElements(t, body)
	wantPaths := []string{
		mkcalendar,
		mkcalendar + "/{DAV:}set",
		prop,
		prop + "/{DAV:}displayname",
		prop + "/{urn:ietf:params:xml:ns:caldav}calendar-description",
		compSet,
		comp,
		comp,
		tz,
		prop + "/{http://apple.com/ns/ical/}calendar-color",
	}
	if !reflect.DeepEqual(paths, wantPaths) {
		t.Errorf("request elements = %q, want %q", paths, wantPaths)
	}
	wantAttrs := []xml.Attr{{Name: xml.Name{Local: "name"}, Value: "VEVENT"}, {Name: xml.Name{Local: "name"}, Value: "VTODO"}}
	if !reflect.DeepEqual(attrs[comp], wantAttrs) {
		t.Errorf("comp attributes = %v, want %v", attrs[comp], wantAttrs)
	}
	for p, want := range map[string]string{
		prop + "/{DAV:}displayname":                                   "Lisa's Events",
		prop + "/{urn:ietf:params:xml:ns:caldav}calendar-description": "Calendar restricted to events.",
		prop + "/{http://apple.com/ns/ical/}calendar-color":           "#FF2968",
	} {
		if text[p] != want {
			t.Errorf("text of %v = %q, want %q", p, text[p], want)
		}
	}
	if id := timezoneID(decodeICal(t, text[tz])); id != "Europe/Paris" || !strings.Contains(text[tz], "\r\n") {
		t.Errorf("calendar-timezone = %q, want the Europe/Paris VTIMEZONE with CRLF line endings", text[tz])
	}

	// Without properties to set, there's no request body
	body = nil
	if err := c.CreateCalendar(ctx, &Calendar{Path: "/home/lisa/calendars/other/"}); err != nil {
		t.Fatalf("CreateCalendar() without properties = %v", err)
	}
	if method != "MKCALENDAR" || path != "/home/lisa/calendars/other/" || len(body) != 0 {
		t.Errorf("request = %v %v with body %q, want MKCALENDAR /home/lisa/calendars/other/ without a body", method, path, body)
	}
}

// The request body of UpdateCalendar sets the properties with a value, and
// removes those with an empty one.
func TestClientUpdateCalendarRequest(t *testing.T) {
	var method string
	var body []byte
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		method = r.Method
		var err error
		if body, err = io.ReadAll(r.Body); err != nil {
			t.Errorf("failed to read request body: %v", err)
		}
		w.Header().Set("Content-Type", "application/xml; charset=utf-8")
		w.WriteHeader(http.StatusMultiStatus)
		io.WriteString(w, `<?xml version="1.0" encoding="utf-8"?>
<D:multistatus xmlns:D="DAV:"><D:response><D:href>/cal/</D:href>
<D:propstat><D:prop><D:displayname/></D:prop><D:status>HTTP/1.1 200 OK</D:status></D:propstat>
</D:response></D:multistatus>`)
	}))
	defer ts.Close()
	c, err := NewClient(nil, ts.URL)
	if err != nil {
		t.Fatal(err)
	}

	name, empty, color := "Office", "", "#00FF00"
	err = c.UpdateCalendar(context.Background(), "/cal/", &CalendarUpdate{
		Name:           &name,
		Description:    &empty,
		Color:          &color,
		RemoveTimezone: true,
	})
	if err != nil {
		t.Fatalf("UpdateCalendar() = %v", err)
	}
	if method != "PROPPATCH" {
		t.Errorf("method = %v, want PROPPATCH", method)
	}

	const (
		update = "{DAV:}propertyupdate"
		set    = update + "/{DAV:}set/{DAV:}prop"
		remove = update + "/{DAV:}remove/{DAV:}prop"
	)
	paths, _, text := xmlElements(t, body)
	wantPaths := []string{
		update,
		update + "/{DAV:}remove",
		remove,
		remove + "/{urn:ietf:params:xml:ns:caldav}calendar-description",
		remove + "/{urn:ietf:params:xml:ns:caldav}calendar-timezone",
		update + "/{DAV:}set",
		set,
		set + "/{DAV:}displayname",
		set + "/{http://apple.com/ns/ical/}calendar-color",
	}
	if !reflect.DeepEqual(paths, wantPaths) {
		t.Errorf("request elements = %q, want %q", paths, wantPaths)
	}
	if got := text[set+"/{DAV:}displayname"]; got != "Office" {
		t.Errorf("displayname = %q, want Office", got)
	}
	if got := text[set+"/{http://apple.com/ns/ical/}calendar-color"]; got != "#00FF00" {
		t.Errorf("calendar-color = %q, want #00FF00", got)
	}
}
