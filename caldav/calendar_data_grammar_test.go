package caldav

import (
	"encoding/xml"
	"net/http"
	"reflect"
	"strings"
	"testing"
)

// The calendar-data element of a REPORT follows the grammar of RFC 4791
// section 9.6: a malformed one is a 400 Bad Request.
func TestReportCalendarDataGrammar(t *testing.T) {
	b, objPath := newCalendarDataBackend(t)
	h := &Handler{Backend: b}

	for _, tc := range []struct {
		name, calendarData string
		wantCode           int
	}{
		{
			// <!ELEMENT calendar-data (comp?, (expand |
			// limit-recurrence-set)?, limit-freebusy-set?)>
			name: "two comp elements",
			calendarData: `<C:comp name="VCALENDAR"><C:prop name="VERSION"/></C:comp>
<C:comp name="VCALENDAR"><C:comp name="VEVENT"/></C:comp>`,
			wantCode: http.StatusBadRequest,
		},
		{
			// The outer comp is for the calendar object, a VCALENDAR
			// component (RFC 4791 section 4.1)
			name:         "outer VEVENT",
			calendarData: `<C:comp name="VEVENT"><C:prop name="SUMMARY"/></C:comp>`,
			wantCode:     http.StatusBadRequest,
		},
		{
			// <!ATTLIST comp name CDATA #REQUIRED> (section 9.6.1)
			name:         "outer comp without a name",
			calendarData: `<C:comp><C:allprop/><C:allcomp/></C:comp>`,
			wantCode:     http.StatusBadRequest,
		},
		{
			name:         "comp without a name",
			calendarData: `<C:comp name="VCALENDAR"><C:comp name=""><C:allprop/></C:comp></C:comp>`,
			wantCode:     http.StatusBadRequest,
		},
		{
			// <!ATTLIST prop name CDATA #REQUIRED novalue (yes | no)
			// "no"> (section 9.6.4)
			name:         "prop without a name",
			calendarData: `<C:comp name="VCALENDAR"><C:prop/><C:comp name="VEVENT"/></C:comp>`,
			wantCode:     http.StatusBadRequest,
		},
		{
			name:         "prop with an empty name",
			calendarData: `<C:comp name="VCALENDAR"><C:comp name="VEVENT"><C:prop name="" novalue="yes"/></C:comp></C:comp>`,
			wantCode:     http.StatusBadRequest,
		},
		{
			// Component names are case-insensitive (RFC 5545 section 2)
			name:         "outer vcalendar",
			calendarData: `<C:comp name="vcalendar"><C:allprop/><C:allcomp/></C:comp>`,
			wantCode:     http.StatusMultiStatus,
		},
		{
			// comp* allows several comp elements of the same name
			name: "two VEVENT comp elements",
			calendarData: `<C:comp name="VCALENDAR"><C:comp name="VEVENT"><C:prop name="SUMMARY"/></C:comp>
<C:comp name="VEVENT"><C:prop name="UID"/></C:comp></C:comp>`,
			wantCode: http.StatusMultiStatus,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			res := serveTestRequest(t, h, "REPORT", syncCalendarPath, `<?xml version="1.0" encoding="utf-8"?>
<C:calendar-multiget xmlns:D="DAV:" xmlns:C="urn:ietf:params:xml:ns:caldav">
  <D:prop><C:calendar-data>`+tc.calendarData+`</C:calendar-data></D:prop>
  <D:href>`+objPath+`</D:href>
</C:calendar-multiget>`)
			res.Body.Close()
			if res.StatusCode != tc.wantCode {
				t.Errorf("got status %v, want %v", res.Status, tc.wantCode)
			}
		})
	}
}

// Sibling comp elements with the same name ask for the components of that
// type, each for some of their parts: the server merges them, so that a
// component is returned with all that any of them asks for. A property asked
// for with its value by one of them keeps its value.
func TestDecodeCalendarDataSiblingComps(t *testing.T) {
	for _, tc := range []struct {
		name, comps string
		want        []CalendarCompRequest
	}{
		{
			name: "properties",
			comps: `<C:comp name="VEVENT"><C:prop name="SUMMARY"/><C:prop name="ATTENDEE" novalue="yes"/><C:prop name="ORGANIZER" novalue="yes"/></C:comp>
<C:comp name="VTODO"><C:prop name="UID"/></C:comp>
<C:comp name="vevent"><C:prop name="UID"/><C:prop name="summary"/><C:prop name="attendee"/><C:prop name="ORGANIZER" novalue="yes"/></C:comp>`,
			want: []CalendarCompRequest{
				{Name: "VEVENT", Props: []string{"SUMMARY", "ATTENDEE", "ORGANIZER", "UID"}, NoValue: []string{"ORGANIZER"}},
				{Name: "VTODO", Props: []string{"UID"}},
			},
		},
		{
			name: "components",
			comps: `<C:comp name="VEVENT"><C:prop name="UID"/><C:comp name="VALARM"><C:prop name="TRIGGER"/></C:comp></C:comp>
<C:comp name="VEVENT"><C:comp name="VALARM"><C:prop name="ACTION"/></C:comp></C:comp>`,
			want: []CalendarCompRequest{
				{Name: "VEVENT", Props: []string{"UID"}, Comps: []CalendarCompRequest{{Name: "VALARM", Props: []string{"TRIGGER", "ACTION"}}}},
			},
		},
		{
			name:  "allprop and allcomp",
			comps: `<C:comp name="VEVENT"><C:prop name="UID"/><C:allcomp/></C:comp><C:comp name="VEVENT"><C:allprop/></C:comp>`,
			want:  []CalendarCompRequest{{Name: "VEVENT", AllProps: true, AllComps: true}},
		},
		{
			// An empty comp element asks for the whole component
			name:  "whole component",
			comps: `<C:comp name="VEVENT"><C:prop name="UID"/></C:comp><C:comp name="VEVENT"/>`,
			want:  []CalendarCompRequest{{Name: "VEVENT", AllProps: true, AllComps: true}},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var el calendarDataReq
			s := `<C:calendar-data xmlns:C="urn:ietf:params:xml:ns:caldav"><C:comp name="VCALENDAR">` + tc.comps + `</C:comp></C:calendar-data>`
			if err := xml.Unmarshal([]byte(s), &el); err != nil {
				t.Fatal(err)
			}
			req, err := decodeCalendarDataReq(&el)
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(req.Comps, tc.want) {
				t.Errorf("decodeCalendarDataReq() components = %+v, want %+v", req.Comps, tc.want)
			}
		})
	}
}

// Filter merges the requests of a CalendarCompRequest built by a backend for
// components of the same type, as the Handler does for those it decodes.
func TestFilterCalendarDataSiblingComps(t *testing.T) {
	co := CalendarObject{Path: "/user/calendars/a/1.ics", Data: decodeICal(t, calendarDataObject)}
	out, err := Filter(&CalendarQuery{
		CompRequest: CalendarCompRequest{Name: "VCALENDAR", Comps: []CalendarCompRequest{
			{Name: "VEVENT", Props: []string{"UID"}},
			{Name: "VEVENT", Props: []string{"SUMMARY"}},
		}},
		CompFilter: CompFilter{Name: "VCALENDAR"},
	}, []CalendarObject{co})
	if err != nil {
		t.Fatal(err)
	}
	const want = "VCALENDAR{}(VEVENT{SUMMARY UID})"
	if len(out) != 1 {
		t.Fatalf("Filter() returned %v objects, want 1", len(out))
	} else if got := componentSummary(out[0].Data.Component); got != want {
		t.Errorf("Filter() = %v, want %v", got, want)
	}
}

// The client never sends a calendar-data element the server would refuse.
func TestEncodeCalendarCompRequestInvalid(t *testing.T) {
	for _, tc := range []struct {
		name string
		req  CalendarCompRequest
	}{
		{"outer VEVENT", CalendarCompRequest{Name: "VEVENT", Props: []string{"SUMMARY"}}},
		{"outer VEVENT without anything else", CalendarCompRequest{Name: "VEVENT"}},
		{"empty property name", CalendarCompRequest{Name: "VCALENDAR", Props: []string{"VERSION", ""}}},
		{"empty child property name", CalendarCompRequest{Comps: []CalendarCompRequest{{Name: "VEVENT", Props: []string{""}}}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if s, err := encodeCompRequestString(t, &tc.req); err == nil {
				t.Errorf("encodeCalendarReq() = %s, want an error", s)
			}
		})
	}

	// An outer request without a name is for VCALENDAR, whatever its case
	s, err := encodeCompRequestString(t, &CalendarCompRequest{Name: "vcalendar", Props: []string{"VERSION"}})
	if err != nil {
		t.Fatalf("encodeCalendarReq() with an outer vcalendar = %v", err)
	}
	if !strings.Contains(s, `name="vcalendar"`) {
		t.Errorf("encodeCalendarReq() = %s, want the outer comp as given", s)
	}
}
