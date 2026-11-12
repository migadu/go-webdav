package caldav

import (
	"context"
	"encoding/xml"
	"net/http"
	"net/http/httptest"
	"reflect"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/emersion/go-ical"
)

// calendarDataObject is a calendar object with a timezone, and an event with
// an alarm.
const calendarDataObject = "BEGIN:VCALENDAR\r\n" +
	"VERSION:2.0\r\n" +
	"PRODID:-//Example Corp.//CalDAV Client//EN\r\n" +
	"BEGIN:VTIMEZONE\r\n" +
	"TZID:Europe/Paris\r\n" +
	"BEGIN:STANDARD\r\n" +
	"DTSTART:19961027T030000\r\n" +
	"TZOFFSETFROM:+0200\r\n" +
	"TZOFFSETTO:+0100\r\n" +
	"END:STANDARD\r\n" +
	"END:VTIMEZONE\r\n" +
	"BEGIN:VEVENT\r\n" +
	"UID:uid-calendar-data\r\n" +
	"DTSTAMP:20060206T001102Z\r\n" +
	"DTSTART;TZID=Europe/Paris:20060102T120000\r\n" +
	"DURATION:PT1H\r\n" +
	"SUMMARY:Calendar data\r\n" +
	"DESCRIPTION:not requested\r\n" +
	"ATTENDEE;CN=Lisa;PARTSTAT=ACCEPTED:mailto:lisa@example.com\r\n" +
	"BEGIN:VALARM\r\n" +
	"ACTION:DISPLAY\r\n" +
	"TRIGGER:-PT15M\r\n" +
	"DESCRIPTION:Reminder\r\n" +
	"END:VALARM\r\n" +
	"END:VEVENT\r\n" +
	"END:VCALENDAR\r\n"

// calendarDataBackend serves a single calendar object, made of
// calendarDataObject, in a calendar supporting sync-collection. Like many
// backends, it ignores the calendar-data requested and returns whole objects.
type calendarDataBackend struct {
	*syncTestBackend
}

func (b calendarDataBackend) QueryCalendarObjects(ctx context.Context, path string, query *CalendarQuery) ([]CalendarObject, error) {
	return b.objectMap[path], nil
}

func newCalendarDataBackend(t *testing.T) (calendarDataBackend, string) {
	t.Helper()
	b := newSyncTestBackend()
	co := CalendarObject{
		Path: syncCalendarPath + "/calendar-data.ics",
		ETag: "1",
		Data: decodeICal(t, calendarDataObject),
	}
	b.objectMap = map[string][]CalendarObject{syncCalendarPath: {co}}
	return calendarDataBackend{b}, co.Path
}

// RFC 4791 section 9.6: "When used in a calendaring REPORT request, the
// CALDAV:calendar-data XML element specifies which parts of calendar object
// resources need to be returned in the response." Section 9.6.4: "The
// "novalue" attribute can be used by clients to request that the actual
// value of the property not be returned (if the "novalue" attribute is set to
// "yes"). In that case, the server will return just the iCalendar property
// name and any iCalendar parameters and a trailing ":" without the
// subsequent value data."
//
// The Handler applies it to every REPORT, whatever the backend returns. The
// request is the one of the example of section 7.8.1, with ATTENDEE: the
// properties it doesn't ask for are left out, including those an iCalendar
// object requires (PRODID, DTSTAMP), which section 9.6 allows. The VTIMEZONE
// component is asked for with an empty comp element, and returned whole, as
// in the example.
func TestHandlerCalendarDataComps(t *testing.T) {
	const calendarData = `<C:calendar-data>
      <C:comp name="VCALENDAR">
        <C:prop name="VERSION"/>
        <C:comp name="VEVENT">
          <C:prop name="SUMMARY"/>
          <C:prop name="UID"/>
          <C:prop name="DTSTART"/>
          <C:prop name="DTEND"/>
          <C:prop name="DURATION"/>
          <C:prop name="RRULE"/>
          <C:prop name="RECURRENCE-ID"/>
          <C:prop name="ATTENDEE" novalue="yes"/>
        </C:comp>
        <C:comp name="VTIMEZONE"/>
      </C:comp>
    </C:calendar-data>`
	want := []string{
		"BEGIN:VCALENDAR",
		"VERSION:2.0",
		"BEGIN:VTIMEZONE",
		"TZID:Europe/Paris",
		"BEGIN:STANDARD",
		"DTSTART:19961027T030000",
		"TZOFFSETFROM:+0200",
		"TZOFFSETTO:+0100",
		"END:STANDARD",
		"END:VTIMEZONE",
		"BEGIN:VEVENT",
		"ATTENDEE;CN=Lisa;PARTSTAT=ACCEPTED:",
		"DTSTART;TZID=Europe/Paris:20060102T120000",
		"DURATION:PT1H",
		"SUMMARY:Calendar data",
		"UID:uid-calendar-data",
		"END:VEVENT",
		"END:VCALENDAR",
	}

	b, objPath := newCalendarDataBackend(t)
	h := &Handler{Backend: b}

	for _, tc := range []struct {
		name, body string
	}{
		{"calendar-query", `<?xml version="1.0" encoding="utf-8"?>
<C:calendar-query xmlns:D="DAV:" xmlns:C="urn:ietf:params:xml:ns:caldav">
  <D:prop>` + calendarData + `</D:prop>
  <C:filter><C:comp-filter name="VCALENDAR"/></C:filter>
</C:calendar-query>`},
		{"calendar-multiget", `<?xml version="1.0" encoding="utf-8"?>
<C:calendar-multiget xmlns:D="DAV:" xmlns:C="urn:ietf:params:xml:ns:caldav">
  <D:prop>` + calendarData + `</D:prop>
  <D:href>` + objPath + `</D:href>
</C:calendar-multiget>`},
		{"sync-collection", `<?xml version="1.0" encoding="utf-8"?>
<D:sync-collection xmlns:D="DAV:" xmlns:C="urn:ietf:params:xml:ns:caldav">
  <D:sync-token/>
  <D:sync-level>1</D:sync-level>
  <D:prop>` + calendarData + `</D:prop>
</D:sync-collection>`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			res := serveTestRequest(t, h, "REPORT", syncCalendarPath, tc.body)
			resp := responsesByPath(t, decodeTestMultiStatus(t, res))[objPath]
			if resp == nil {
				t.Fatalf("no response for %v", objPath)
			}
			var data calendarDataResp
			if err := resp.DecodeProp(&data); err != nil {
				t.Fatalf("calendar-data: %v", err)
			}
			got := strings.Split(strings.TrimRight(strings.ReplaceAll(string(data.Data), "\r\n", "\n"), "\n"), "\n")
			if strings.Join(got, "\n") != strings.Join(want, "\n") {
				t.Errorf("calendar-data =\n%v\nwant:\n%v", strings.Join(got, "\n"), strings.Join(want, "\n"))
			}
		})
	}

	t.Run("invalid novalue", func(t *testing.T) {
		res := serveTestRequest(t, h, "REPORT", syncCalendarPath, `<?xml version="1.0" encoding="utf-8"?>
<C:calendar-multiget xmlns:D="DAV:" xmlns:C="urn:ietf:params:xml:ns:caldav">
  <D:prop><C:calendar-data><C:comp name="VCALENDAR"><C:comp name="VEVENT">
    <C:prop name="ATTENDEE" novalue="maybe"/>
  </C:comp></C:comp></C:calendar-data></D:prop>
  <D:href>`+objPath+`</D:href>
</C:calendar-multiget>`)
		res.Body.Close()
		if res.StatusCode != http.StatusBadRequest {
			t.Errorf("got status %v, want 400 Bad Request", res.Status)
		}
	})

	// The backend's object is left as it is
	if co := b.objectMap[syncCalendarPath][0]; !reflect.DeepEqual(co.Data, decodeICal(t, calendarDataObject)) {
		t.Errorf("the backend's calendar object was changed")
	}
}

// componentSummary summarizes comp: its name, the names of its properties in
// sorted order, followed by "=" for those with an empty value, then its
// children in parentheses.
func componentSummary(comp *ical.Component) string {
	var names []string
	for name, props := range comp.Props {
		for _, prop := range props {
			if prop.Value == "" {
				name += "="
			}
			names = append(names, name)
		}
	}
	sort.Strings(names)
	s := comp.Name + "{" + strings.Join(names, " ") + "}"
	if len(comp.Children) > 0 {
		var children []string
		for _, child := range comp.Children {
			children = append(children, componentSummary(child))
		}
		s += "(" + strings.Join(children, " ") + ")"
	}
	return s
}

const (
	wholeCalendarDataObject = "VCALENDAR{PRODID VERSION}(VTIMEZONE{TZID}(STANDARD{DTSTART TZOFFSETFROM TZOFFSETTO}) " +
		"VEVENT{ATTENDEE DESCRIPTION DTSTAMP DTSTART DURATION SUMMARY UID}(VALARM{ACTION DESCRIPTION TRIGGER}))"
	wholeTimezone = "VTIMEZONE{TZID}(STANDARD{DTSTART TZOFFSETFROM TZOFFSETTO})"
)

// Filter returns the parts of the calendar objects the query asks for (RFC
// 4791 section 9.6), without changing the objects it's given.
func TestFilterCalendarData(t *testing.T) {
	expand := &CalendarExpandRequest{Start: time.Date(2006, 1, 3, 0, 0, 0, 0, time.UTC), End: time.Date(2006, 1, 5, 0, 0, 0, 0, time.UTC)}
	example := CalendarCompRequest{
		Name:  "VCALENDAR",
		Props: []string{"VERSION"},
		Comps: []CalendarCompRequest{
			{Name: "VEVENT", Props: []string{"SUMMARY", "UID", "DTSTART", "DTEND", "DURATION", "RRULE", "RECURRENCE-ID"}},
			{Name: "VTIMEZONE"},
		},
	}
	withExpand := example
	withExpand.Expand = expand

	for _, tc := range []struct {
		name string
		req  CalendarCompRequest
		want string
	}{
		{
			// No calendar-data, or calendar-data without comp
			name: "no request",
			want: wholeCalendarDataObject,
		},
		{
			name: "allprop and allcomp",
			req:  CalendarCompRequest{Name: "VCALENDAR", AllProps: true, AllComps: true},
			want: wholeCalendarDataObject,
		},
		{
			// As VTIMEZONE in the example of RFC 4791 section 7.8.1
			name: "empty comp",
			req:  CalendarCompRequest{Name: "VCALENDAR"},
			want: wholeCalendarDataObject,
		},
		{
			// (allprop | prop*), (allcomp | comp*): no comp element is no
			// component
			name: "allprop without comp",
			req:  CalendarCompRequest{Name: "VCALENDAR", AllProps: true},
			want: "VCALENDAR{PRODID VERSION}",
		},
		{
			// and no prop element no property
			name: "comp without prop",
			req:  CalendarCompRequest{Name: "VCALENDAR", Comps: []CalendarCompRequest{{Name: "VEVENT", AllProps: true}}},
			want: "VCALENDAR{}(VEVENT{ATTENDEE DESCRIPTION DTSTAMP DTSTART DURATION SUMMARY UID})",
		},
		{
			name: "allcomp",
			req:  CalendarCompRequest{Name: "VCALENDAR", Props: []string{"VERSION"}, AllComps: true},
			want: "VCALENDAR{VERSION}(" + wholeTimezone + " VEVENT{ATTENDEE DESCRIPTION DTSTAMP DTSTART DURATION SUMMARY UID}(VALARM{ACTION DESCRIPTION TRIGGER}))",
		},
		{
			name: "nested comp",
			req: CalendarCompRequest{Name: "VCALENDAR", Comps: []CalendarCompRequest{{
				Name:  "VEVENT",
				Props: []string{"UID"},
				Comps: []CalendarCompRequest{{Name: "VALARM", Props: []string{"TRIGGER"}}},
			}}},
			want: "VCALENDAR{}(VEVENT{UID}(VALARM{TRIGGER}))",
		},
		{
			name: "RFC 4791 section 7.8.1",
			req:  example,
			want: "VCALENDAR{VERSION}(" + wholeTimezone + " VEVENT{DTSTART DURATION SUMMARY UID})",
		},
		{
			// Component and property names are case-insensitive (RFC 5545
			// section 2)
			name: "lowercase names",
			req: CalendarCompRequest{Name: "vcalendar", Props: []string{"version"}, Comps: []CalendarCompRequest{
				{Name: "vevent", Props: []string{"summary", "Summary"}},
			}},
			want: "VCALENDAR{VERSION}(VEVENT{SUMMARY})",
		},
		{
			// The backend expands the objects Filter returns: it needs
			// their RRULE, which the client didn't ask for. The Handler
			// returns only the parts asked for.
			name: "with expand",
			req:  withExpand,
			want: wholeCalendarDataObject,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			co := CalendarObject{Path: "/user/calendars/a/1.ics", Data: decodeICal(t, calendarDataObject)}
			out, err := Filter(&CalendarQuery{
				CompRequest: tc.req,
				CompFilter:  CompFilter{Name: "VCALENDAR"},
			}, []CalendarObject{co})
			if err != nil {
				t.Fatal(err)
			}
			if len(out) != 1 {
				t.Fatalf("Filter() returned %v objects, want 1", len(out))
			}
			if got := componentSummary(out[0].Data.Component); got != tc.want {
				t.Errorf("Filter() =\n%v\nwant:\n%v", got, tc.want)
			}
			if out[0].Path != co.Path {
				t.Errorf("Filter() path = %q, want %q", out[0].Path, co.Path)
			}
			if !reflect.DeepEqual(co.Data, decodeICal(t, calendarDataObject)) {
				t.Errorf("Filter() changed its calendar object")
			}
		})
	}
}

// RFC 4791 section 9.6.4: <!ATTLIST prop name CDATA #REQUIRED novalue (yes |
// no) "no">
func TestDecodeCalendarDataNoValue(t *testing.T) {
	for _, tc := range []struct {
		name, props string
		want        CalendarCompRequest
	}{
		{"novalue", `<C:prop name="SUMMARY" novalue="no"/><C:prop name="ATTENDEE" novalue="yes"/>`,
			CalendarCompRequest{Name: "VEVENT", Props: []string{"SUMMARY", "ATTENDEE"}, NoValue: []string{"ATTENDEE"}}},
		// A property requested both ways is returned with its value
		{"also requested with its value", `<C:prop name="ATTENDEE" novalue="yes"/><C:prop name="attendee"/>`,
			CalendarCompRequest{Name: "VEVENT", Props: []string{"ATTENDEE", "attendee"}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var el calendarDataReq
			s := `<C:calendar-data xmlns:C="urn:ietf:params:xml:ns:caldav"><C:comp name="VCALENDAR"><C:comp name="VEVENT">` +
				tc.props + `</C:comp></C:comp></C:calendar-data>`
			if err := xml.Unmarshal([]byte(s), &el); err != nil {
				t.Fatal(err)
			}
			req, err := decodeCalendarDataReq(&el)
			if err != nil {
				t.Fatal(err)
			}
			if len(req.Comps) != 1 || !reflect.DeepEqual(req.Comps[0], tc.want) {
				t.Errorf("decodeCalendarDataReq() components = %+v, want %+v", req.Comps, tc.want)
			}
		})
	}
}

// The properties requested with novalue keep their parameters, and get an
// empty value on copies.
func TestFilterCalendarDataNoValue(t *testing.T) {
	query := &CalendarQuery{
		CompRequest: CalendarCompRequest{Name: "VCALENDAR", Comps: []CalendarCompRequest{{
			Name:    "VEVENT",
			Props:   []string{"UID", "attendee", "SUMMARY"},
			NoValue: []string{"attendee", "SUMMARY"},
		}}},
		CompFilter: CompFilter{Name: "VCALENDAR"},
	}
	co := CalendarObject{Path: "/user/calendars/a/1.ics", Data: decodeICal(t, calendarDataObject)}
	out, err := Filter(query, []CalendarObject{co})
	if err != nil {
		t.Fatal(err)
	}
	const want = "VCALENDAR{}(VEVENT{ATTENDEE= SUMMARY= UID})"
	if len(out) != 1 {
		t.Fatalf("Filter() returned %v objects, want 1", len(out))
	} else if got := componentSummary(out[0].Data.Component); got != want {
		t.Errorf("Filter() = %v, want %v", got, want)
	}
	attendee := out[0].Data.Children[0].Props.Get(ical.PropAttendee)
	if attendee == nil || attendee.Params.Get(ical.ParamCommonName) != "Lisa" || attendee.Params.Get(ical.ParamParticipationStatus) != "ACCEPTED" {
		t.Errorf("Filter() ATTENDEE = %+v, want its parameters", attendee)
	}

	// Filtering again changes nothing
	again, err := Filter(query, out)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(again, out) {
		t.Errorf("Filter() of its own result = %v, want %v", componentSummary(again[0].Data.Component), want)
	}
	if !reflect.DeepEqual(co.Data, decodeICal(t, calendarDataObject)) {
		t.Errorf("Filter() changed its calendar object")
	}
}

// requestRecordingBackend records the calendar data requests of
// calendar-multiget REPORTs.
type requestRecordingBackend struct {
	calendarDataBackend
	reqs []CalendarCompRequest
}

func (b *requestRecordingBackend) GetCalendarObject(ctx context.Context, path string, req *CalendarCompRequest) (*CalendarObject, error) {
	b.reqs = append(b.reqs, *req)
	return b.calendarDataBackend.GetCalendarObject(ctx, path, req)
}

// The client sends NoValue, the backend gets the request the client was
// given, and the client decodes the properties without a value.
func TestClientCalendarDataNoValue(t *testing.T) {
	db, objPath := newCalendarDataBackend(t)
	b := &requestRecordingBackend{calendarDataBackend: db}
	ts := httptest.NewServer(&Handler{Backend: b})
	defer ts.Close()
	c, err := NewClient(nil, ts.URL)
	if err != nil {
		t.Fatal(err)
	}

	req := CalendarCompRequest{Name: "VCALENDAR", Props: []string{"VERSION"}, Comps: []CalendarCompRequest{{
		Name:    "VEVENT",
		Props:   []string{"UID", "ATTENDEE"},
		NoValue: []string{"ATTENDEE"},
	}}}
	cos, err := c.MultiGetCalendar(context.Background(), syncCalendarPath, &CalendarMultiGet{
		Paths:       []string{objPath},
		CompRequest: req,
	})
	if err != nil {
		t.Fatalf("MultiGetCalendar() = %v", err)
	}
	if len(b.reqs) != 1 || !reflect.DeepEqual(b.reqs[0], req) {
		t.Errorf("backend got requests %+v, want %+v", b.reqs, req)
	}
	if len(cos) != 1 {
		t.Fatalf("MultiGetCalendar() returned %v objects, want 1", len(cos))
	}
	const want = "VCALENDAR{VERSION}(VEVENT{ATTENDEE= UID})"
	if got := componentSummary(cos[0].Data.Component); got != want {
		t.Errorf("MultiGetCalendar() data = %v, want %v", got, want)
	}
	if attendee := cos[0].Data.Children[0].Props.Get(ical.PropAttendee); attendee == nil || attendee.Params.Get(ical.ParamCommonName) != "Lisa" {
		t.Errorf("MultiGetCalendar() ATTENDEE = %+v, want its parameters", attendee)
	}
}
