package caldav

import (
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/emersion/go-ical"
)

var dateFormat = "20060102T150405Z"

func toDate(t *testing.T, date string) time.Time {
	res, err := time.ParseInLocation(dateFormat, date, time.UTC)
	if err != nil {
		t.Fatal(err)
	}
	return res
}

// Test data taken from https://datatracker.ietf.org/doc/html/rfc4791#appendix-B
// TODO add missing data
func TestFilter(t *testing.T) {
	newCO := func(str string) CalendarObject {
		cal, err := ical.NewDecoder(strings.NewReader(str)).Decode()
		if err != nil {
			t.Fatal(err)
		}
		return CalendarObject{
			Data: cal,
		}
	}

	event1 := newCO(`BEGIN:VCALENDAR
VERSION:2.0
PRODID:-//Example Corp.//CalDAV Client//EN
BEGIN:VTIMEZONE
LAST-MODIFIED:20040110T032845Z
TZID:US/Eastern
BEGIN:DAYLIGHT
DTSTART:20000404T020000
RRULE:FREQ=YEARLY;BYDAY=1SU;BYMONTH=4
TZNAME:EDT
TZOFFSETFROM:-0500
TZOFFSETTO:-0400
END:DAYLIGHT
BEGIN:STANDARD
DTSTART:20001026T020000
RRULE:FREQ=YEARLY;BYDAY=-1SU;BYMONTH=10
TZNAME:EST
TZOFFSETFROM:-0400
TZOFFSETTO:-0500
END:STANDARD
END:VTIMEZONE
BEGIN:VEVENT
DTSTAMP:20060206T001102Z
DTSTART;TZID=US/Eastern:20060102T100000
DURATION:PT1H
SUMMARY:Event #1
Description:Go Steelers!
UID:74855313FA803DA593CD579A@example.com
END:VEVENT
END:VCALENDAR`)

	event2 := newCO(`BEGIN:VCALENDAR
VERSION:2.0
PRODID:-//Example Corp.//CalDAV Client//EN
BEGIN:VTIMEZONE
LAST-MODIFIED:20040110T032845Z
TZID:US/Eastern
BEGIN:DAYLIGHT
DTSTART:20000404T020000
RRULE:FREQ=YEARLY;BYDAY=1SU;BYMONTH=4
TZNAME:EDT
TZOFFSETFROM:-0500
TZOFFSETTO:-0400
END:DAYLIGHT
BEGIN:STANDARD
DTSTART:20001026T020000
RRULE:FREQ=YEARLY;BYDAY=-1SU;BYMONTH=10
TZNAME:EST
TZOFFSETFROM:-0400
TZOFFSETTO:-0500
END:STANDARD
END:VTIMEZONE
BEGIN:VEVENT
DTSTAMP:20060206T001121Z
DTSTART;TZID=US/Eastern:20060102T120000
DURATION:PT1H
RRULE:FREQ=DAILY;COUNT=5
SUMMARY:Event #2
UID:00959BC664CA650E933C892C@example.com
END:VEVENT
BEGIN:VEVENT
DTSTAMP:20060206T001121Z
DTSTART;TZID=US/Eastern:20060104T140000
DURATION:PT1H
RECURRENCE-ID;TZID=US/Eastern:20060104T120000
SUMMARY:Event #2 bis
UID:00959BC664CA650E933C892C@example.com
END:VEVENT
BEGIN:VEVENT
DTSTAMP:20060206T001121Z
DTSTART;TZID=US/Eastern:20060106T140000
DURATION:PT1H
RECURRENCE-ID;TZID=US/Eastern:20060106T120000
SUMMARY:Event #2 bis bis
UID:00959BC664CA650E933C892C@example.com
END:VEVENT
END:VCALENDAR`)

	event3 := newCO(`BEGIN:VCALENDAR
VERSION:2.0
PRODID:-//Example Corp.//CalDAV Client//EN
BEGIN:VTIMEZONE
LAST-MODIFIED:20040110T032845Z
TZID:US/Eastern
BEGIN:DAYLIGHT
DTSTART:20000404T020000
RRULE:FREQ=YEARLY;BYDAY=1SU;BYMONTH=4
TZNAME:EDT
TZOFFSETFROM:-0500
TZOFFSETTO:-0400
END:DAYLIGHT
BEGIN:STANDARD
DTSTART:20001026T020000
RRULE:FREQ=YEARLY;BYDAY=-1SU;BYMONTH=10
TZNAME:EST
TZOFFSETFROM:-0400
TZOFFSETTO:-0500
END:STANDARD
END:VTIMEZONE
BEGIN:VEVENT
ATTENDEE;PARTSTAT=ACCEPTED;ROLE=CHAIR:mailto:cyrus@example.com
ATTENDEE;PARTSTAT=NEEDS-ACTION:mailto:lisa@example.com
DTSTAMP:20060206T001220Z
DTSTART;TZID=US/Eastern:20060104T100000
DURATION:PT1H
LAST-MODIFIED:20060206T001330Z
ORGANIZER:mailto:cyrus@example.com
SEQUENCE:1
STATUS:TENTATIVE
SUMMARY:Event #3
UID:DC6C50A017428C5216A2F1CD@example.com
END:VEVENT
END:VCALENDAR`)

	event4 := newCO(`
BEGIN:VCALENDAR
PRODID:DAVx5/4.4.5-ose ical4j/3.2.19 (org.fossify.calendar)
VERSION:2.0
BEGIN:VEVENT
CREATED:20250111T232306Z
DTEND;TZID=Europe/Paris:20250114T200000
DTSTAMP:20250111T235047Z
DTSTART;TZID=Europe/Paris:20250114T190000
RRULE:FREQ=DAILY;COUNT=2;INTERVAL=1
SEQUENCE:5
STATUS:TENTATIVE
SUMMARY:event
UID:FA4733E2-EDE6-454A-BAE1-0AC82E6384AB
X-APPLE-CREATOR-IDENTITY:com.apple.mobilecal
X-APPLE-CREATOR-TEAM-IDENTITY:0000000000
BEGIN:VALARM
ACTION:DISPLAY
DESCRIPTION: description
TRIGGER:-PT30M
END:VALARM
END:VEVENT
BEGIN:VTIMEZONE
TZID:Europe/Paris
BEGIN:STANDARD
DTSTART:19961027T030000
RRULE:FREQ=YEARLY;BYMONTH=10;BYDAY=-1SU
TZNAME:CET
TZOFFSETFROM:+0200
TZOFFSETTO:+0100
END:STANDARD
BEGIN:DAYLIGHT
DTSTART:19810329T020000
RRULE:FREQ=YEARLY;BYMONTH=3;BYDAY=-1SU
TZNAME:CEST
TZOFFSETFROM:+0100
TZOFFSETTO:+0200
END:DAYLIGHT
END:VTIMEZONE
END:VCALENDAR
`)

	event5 := newCO(`
BEGIN:VCALENDAR
PRODID:DAVx5/4.4.5-ose ical4j/3.2.19 (org.fossify.calendar)
VERSION:2.0
BEGIN:VEVENT
CREATED:20250111T232306Z
DTSTAMP:20250111T235047Z
DTSTART;TZID=Europe/Paris:20250101T120000
STATUS:CONFIRMED
SUMMARY:event
UID:FA4733E2-EDE6-454A-BAE1-0AC82E6384AB
X-APPLE-CREATOR-IDENTITY:com.apple.mobilecal
X-APPLE-CREATOR-TEAM-IDENTITY:0000000000
BEGIN:VALARM
ACTION:DISPLAY
DESCRIPTION: description
TRIGGER:-PT30M
END:VALARM
END:VEVENT
BEGIN:VTIMEZONE
TZID:Europe/Paris
BEGIN:STANDARD
DTSTART:19961027T030000
RRULE:FREQ=YEARLY;BYMONTH=10;BYDAY=-1SU
TZNAME:CET
TZOFFSETFROM:+0200
TZOFFSETTO:+0100
END:STANDARD
BEGIN:DAYLIGHT
DTSTART:19810329T020000
RRULE:FREQ=YEARLY;BYMONTH=3;BYDAY=-1SU
TZNAME:CEST
TZOFFSETFROM:+0100
TZOFFSETTO:+0200
END:DAYLIGHT
END:VTIMEZONE
END:VCALENDAR
`)

	todo1 := newCO(`BEGIN:VCALENDAR
VERSION:2.0
PRODID:-//Example Corp.//CalDAV Client//EN
BEGIN:VTODO
DTSTAMP:20060205T235335Z
DUE;VALUE=DATE:20060104
STATUS:NEEDS-ACTION
SUMMARY:Task #1
UID:DDDEEB7915FA61233B861457@example.com
BEGIN:VALARM
ACTION:AUDIO
TRIGGER;RELATED=START:-PT10M
END:VALARM
END:VTODO
END:VCALENDAR`)

	for _, tc := range []struct {
		name  string
		query *CalendarQuery
		addrs []CalendarObject
		want  []CalendarObject
		err   error
	}{
		{
			name:  "nil-query",
			query: nil,
			addrs: []CalendarObject{event1, event2, event3, event4, todo1},
			want:  []CalendarObject{event1, event2, event3, event4, todo1},
		},
		{
			// https://datatracker.ietf.org/doc/html/rfc4791#section-7.8.8
			name: "events only",
			query: &CalendarQuery{
				CompFilter: CompFilter{
					Name: "VCALENDAR",
					Comps: []CompFilter{
						CompFilter{
							Name: "VEVENT",
						},
					},
				},
			},
			addrs: []CalendarObject{event1, event2, event3, event4, todo1},
			want:  []CalendarObject{event1, event2, event3, event4},
		},
		{
			// https://datatracker.ietf.org/doc/html/rfc4791#section-7.8.1
			name: "events in time range",
			query: &CalendarQuery{
				CompFilter: CompFilter{
					Name: "VCALENDAR",
					Comps: []CompFilter{
						CompFilter{
							Name:  "VEVENT",
							Start: toDate(t, "20060104T000000Z"),
							End:   toDate(t, "20060105T000000Z"),
						},
					},
				},
			},
			addrs: []CalendarObject{event1, event2, event3, event4, todo1},
			want:  []CalendarObject{event2, event3},
		},
		{
			// https://datatracker.ietf.org/doc/html/rfc4791#section-7.8.1
			name: "events in open time range (no end date)",
			query: &CalendarQuery{
				CompFilter: CompFilter{
					Name: "VCALENDAR",
					Comps: []CompFilter{
						CompFilter{
							Name:  "VEVENT",
							Start: toDate(t, "20060104T000000Z"),
						},
					},
				},
			},
			addrs: []CalendarObject{event1, event2, event3, todo1},
			want:  []CalendarObject{event2, event3},
		},
		{
			// https://datatracker.ietf.org/doc/html/rfc4791#section-7.8.6
			name: "events by UID",
			query: &CalendarQuery{
				CompFilter: CompFilter{
					Name: "VCALENDAR",
					Comps: []CompFilter{
						CompFilter{
							Name: "VEVENT",
							Props: []PropFilter{{
								Name: "UID",
								TextMatch: &TextMatch{
									Text: "DC6C50A017428C5216A2F1CD@example.com",
								},
							}},
						},
					},
				},
			},
			addrs: []CalendarObject{event1, event2, event3, event4, todo1},
			want:  []CalendarObject{event3},
		},
		{
			// https://datatracker.ietf.org/doc/html/rfc4791#section-7.8.6
			name: "events by description substring",
			query: &CalendarQuery{
				CompFilter: CompFilter{
					Name: "VCALENDAR",
					Comps: []CompFilter{
						CompFilter{
							Name: "VEVENT",
							Props: []PropFilter{{
								Name: "Description",
								TextMatch: &TextMatch{
									Text: "Steelers",
								},
							}},
						},
					},
				},
			},
			addrs: []CalendarObject{event1, event2, event3, event4, todo1},
			want:  []CalendarObject{event1},
		},
		{
			// Query a time range that only returns a result if recurrence is properly evaluated.
			name: "recurring events in time range",
			query: &CalendarQuery{
				CompFilter: CompFilter{
					Name: "VCALENDAR",
					Comps: []CompFilter{
						CompFilter{
							Name:  "VEVENT",
							Start: toDate(t, "20060103T000000Z"),
							End:   toDate(t, "20060104T000000Z"),
						},
					},
				},
			},
			addrs: []CalendarObject{event1, event2, event3, event4, todo1},
			want:  []CalendarObject{event2},
		},
		{
			// only an end attribute: the range is open at the start
			name: "time range without start",
			query: &CalendarQuery{
				CompFilter: CompFilter{
					Name: "VCALENDAR",
					Comps: []CompFilter{
						CompFilter{
							Name: "VEVENT",
							End:  toDate(t, "20250114T180000Z"),
						},
					},
				},
			},
			addrs: []CalendarObject{event1, event2, event3, event4, todo1},
			want:  []CalendarObject{event1, event2, event3},
		},
		{
			// event with no DTEND or DURATION
			name: "No DTEND or DURATION",
			query: &CalendarQuery{
				CompFilter: CompFilter{
					Name: "VCALENDAR",
					Comps: []CompFilter{
						CompFilter{
							Name:  "VEVENT",
							Start: toDate(t, "20250101T110000Z"),
							End:   toDate(t, "20250114T110001Z"),
						},
					},
				},
			},
			addrs: []CalendarObject{event4, event5},
			want:  []CalendarObject{event5},
		},

		// TODO add more examples
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := Filter(tc.query, tc.addrs)
			switch {
			case err != nil && tc.err == nil:
				t.Fatalf("unexpected error: %+v", err)
			case err != nil && tc.err != nil:
				if got, want := err.Error(), tc.err.Error(); got != want {
					t.Fatalf("invalid error:\ngot= %q\nwant=%q", got, want)
				}
			case err == nil && tc.err != nil:
				t.Fatalf("expected an error:\ngot= %+v\nwant=%+v", err, tc.err)
			case err == nil && tc.err == nil:
				if got, want := got, tc.want; !reflect.DeepEqual(got, want) {
					t.Fatalf("invalid filter values:\ngot= %+v\nwant=%+v", got, want)
				}
			}
		})
	}
}

// https://datatracker.ietf.org/doc/html/rfc4791#section-9.9 defines the range as
// [start, end): a VEVENT with a DTEND overlaps it when (start < DTEND AND end > DTSTART).
func TestMatchCompTimeRangeBoundary(t *testing.T) {
	newCO := func(dtstart, dtend string) CalendarObject {
		s := "BEGIN:VCALENDAR\r\nVERSION:2.0\r\nPRODID:-//Example Corp.//CalDAV Client//EN\r\n" +
			"BEGIN:VEVENT\r\nDTSTAMP:20060206T001102Z\r\nUID:boundary\r\nSUMMARY:Boundary\r\n" +
			"DTSTART:" + dtstart + "\r\n"
		if dtend != "" {
			s += "DTEND:" + dtend + "\r\n"
		}
		s += "END:VEVENT\r\nEND:VCALENDAR\r\n"
		cal, err := ical.NewDecoder(strings.NewReader(s)).Decode()
		if err != nil {
			t.Fatal(err)
		}
		return CalendarObject{Data: cal}
	}

	// The query range is the whole of 2006-01-03.
	query := &CalendarQuery{
		CompFilter: CompFilter{
			Name: "VCALENDAR",
			Comps: []CompFilter{
				{
					Name:  "VEVENT",
					Start: toDate(t, "20060103T000000Z"),
					End:   toDate(t, "20060104T000000Z"),
				},
			},
		},
	}

	for _, tc := range []struct {
		name    string
		dtstart string
		dtend   string
		want    bool
	}{
		{"event covers the range exactly", "20060103T000000Z", "20060104T000000Z", true},
		{"event ends at the range end", "20060102T000000Z", "20060104T000000Z", true},
		{"zero-length event at the range start", "20060103T000000Z", "", true},
		{"event inside the range", "20060103T060000Z", "20060103T120000Z", true},
		{"event spans the range", "20060102T000000Z", "20060105T000000Z", true},
		{"event ends at the range start", "20060102T000000Z", "20060103T000000Z", false},
		{"event starts at the range end", "20060104T000000Z", "20060105T000000Z", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			co := newCO(tc.dtstart, tc.dtend)
			got, err := Filter(query, []CalendarObject{co})
			if err != nil {
				t.Fatalf("unexpected error: %+v", err)
			}
			if matched := len(got) == 1; matched != tc.want {
				t.Fatalf("matched=%v, want %v", matched, tc.want)
			}
		})
	}
}

// An occurrence of a recurring event matches when it overlaps the range, not
// only when it starts inside it.
func TestMatchRecurringTimeRange(t *testing.T) {
	newCO := func(rrule string) CalendarObject {
		s := "BEGIN:VCALENDAR\r\nVERSION:2.0\r\nPRODID:-//Example Corp.//CalDAV Client//EN\r\n" +
			"BEGIN:VEVENT\r\nDTSTAMP:20060206T001102Z\r\nUID:recurring\r\nSUMMARY:Daily\r\n" +
			"DTSTART:20060101T090000Z\r\nDTEND:20060101T110000Z\r\nRRULE:" + rrule + "\r\n" +
			"END:VEVENT\r\nEND:VCALENDAR\r\n"
		cal, err := ical.NewDecoder(strings.NewReader(s)).Decode()
		if err != nil {
			t.Fatal(err)
		}
		return CalendarObject{Data: cal}
	}

	for _, tc := range []struct {
		name       string
		rrule      string
		start, end string
		want       bool
	}{
		{"occurrence started before the range", "FREQ=DAILY;COUNT=5", "20060103T100000Z", "20060103T103000Z", true},
		{"occurrence ends at the range start", "FREQ=DAILY;COUNT=5", "20060103T110000Z", "20060103T120000Z", false},
		{"occurrence starts at the range end", "FREQ=DAILY;COUNT=5", "20060103T080000Z", "20060103T090000Z", false},
		{"range without end, last occurrence overlaps", "FREQ=DAILY;COUNT=3", "20060103T100000Z", "", true},
		{"range without end, after the last occurrence", "FREQ=DAILY;COUNT=2", "20060103T100000Z", "", false},
		{"range without start, before the first occurrence", "FREQ=DAILY;COUNT=2", "", "20060101T090000Z", false},
		{"range without start, first occurrence overlaps", "FREQ=DAILY;COUNT=2", "", "20060101T090001Z", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			filter := CompFilter{Name: "VEVENT"}
			if tc.start != "" {
				filter.Start = toDate(t, tc.start)
			}
			if tc.end != "" {
				filter.End = toDate(t, tc.end)
			}
			query := &CalendarQuery{CompFilter: CompFilter{Name: "VCALENDAR", Comps: []CompFilter{filter}}}

			got, err := Filter(query, []CalendarObject{newCO(tc.rrule)})
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if matched := len(got) == 1; matched != tc.want {
				t.Fatalf("matched=%v, want %v", matched, tc.want)
			}
		})
	}
}

// A time-range on a DATE-TIME property is [start, end) as well.
func TestMatchPropTimeRangeBoundary(t *testing.T) {
	cal, err := ical.NewDecoder(strings.NewReader("BEGIN:VCALENDAR\r\nVERSION:2.0\r\nPRODID:-//Example Corp.//CalDAV Client//EN\r\n" +
		"BEGIN:VTODO\r\nDTSTAMP:20060103T000000Z\r\nUID:todo\r\nCOMPLETED:20060103T000000Z\r\nSUMMARY:Done\r\n" +
		"END:VTODO\r\nEND:VCALENDAR\r\n")).Decode()
	if err != nil {
		t.Fatal(err)
	}
	co := CalendarObject{Data: cal}

	for _, tc := range []struct {
		name       string
		start, end string
		want       bool
	}{
		{"value at the range start", "20060103T000000Z", "20060104T000000Z", true},
		{"value at the range end", "20060102T000000Z", "20060103T000000Z", false},
		{"range without start", "", "20060103T000001Z", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			pf := PropFilter{Name: "COMPLETED"}
			if tc.start != "" {
				pf.Start = toDate(t, tc.start)
			}
			if tc.end != "" {
				pf.End = toDate(t, tc.end)
			}
			query := &CalendarQuery{CompFilter: CompFilter{Name: "VCALENDAR", Comps: []CompFilter{{Name: "VTODO", Props: []PropFilter{pf}}}}}

			got, err := Filter(query, []CalendarObject{co})
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if matched := len(got) == 1; matched != tc.want {
				t.Fatalf("matched=%v, want %v", matched, tc.want)
			}
		})
	}
}

// Whether a recurring event matches a range open at the start depends on its
// first occurrence only, and a range that an early occurrence overlaps needs no
// later ones. Neither may visit every occurrence up to the end of the range.
func TestMatchRecurringTimeRangeCost(t *testing.T) {
	// An occurrence every second, with no COUNT or UNTIL: a day has 86400.
	cal, err := ical.NewDecoder(strings.NewReader("BEGIN:VCALENDAR\r\nVERSION:2.0\r\nPRODID:-//Example Corp.//CalDAV Client//EN\r\n" +
		"BEGIN:VEVENT\r\nDTSTAMP:20060206T001102Z\r\nUID:secondly\r\nSUMMARY:Secondly\r\n" +
		"DTSTART:20060101T000000Z\r\nDTEND:20060101T000001Z\r\nRRULE:FREQ=SECONDLY\r\n" +
		"END:VEVENT\r\nEND:VCALENDAR\r\n")).Decode()
	if err != nil {
		t.Fatal(err)
	}
	co := CalendarObject{Data: cal}

	for _, tc := range []struct {
		name       string
		start, end string
		want       bool
	}{
		{"range without start", "", "20060102T000000Z", true},
		{"range without start, before the first occurrence", "", "20060101T000000Z", false},
		{"first occurrence overlaps", "20060101T000000Z", "20060102T000000Z", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			filter := CompFilter{Name: "VEVENT"}
			if tc.start != "" {
				filter.Start = toDate(t, tc.start)
			}
			if tc.end != "" {
				filter.End = toDate(t, tc.end)
			}
			query := &CalendarQuery{CompFilter: CompFilter{Name: "VCALENDAR", Comps: []CompFilter{filter}}}

			var got []CalendarObject
			var err error
			allocs := testing.AllocsPerRun(1, func() {
				got, err = Filter(query, []CalendarObject{co})
			})
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if matched := len(got) == 1; matched != tc.want {
				t.Fatalf("matched=%v, want %v", matched, tc.want)
			}
			if allocs > 1000 {
				t.Fatalf("%v allocations, want at most 1000", allocs)
			}
		})
	}
}

// https://datatracker.ietf.org/doc/html/rfc4791#section-9.9 defines when a
// VTODO overlaps a time range from its DTSTART, DURATION, DUE, COMPLETED and
// CREATED properties.
func TestMatchTodoTimeRange(t *testing.T) {
	newCO := func(props string) CalendarObject {
		s := "BEGIN:VCALENDAR\r\nVERSION:2.0\r\nPRODID:-//Example Corp.//CalDAV Client//EN\r\n" +
			"BEGIN:VTODO\r\nDTSTAMP:20060205T235335Z\r\nUID:todo\r\nSUMMARY:Task\r\n" +
			props + "END:VTODO\r\nEND:VCALENDAR\r\n"
		cal, err := ical.NewDecoder(strings.NewReader(s)).Decode()
		if err != nil {
			t.Fatal(err)
		}
		return CalendarObject{Data: cal}
	}

	const (
		startDue      = "DTSTART:20060101T000000Z\r\nDUE:20060104T000000Z\r\n"
		startDuration = "DTSTART:20060101T000000Z\r\nDURATION:P3D\r\n"
		startOnly     = "DTSTART:20060101T000000Z\r\n"
		dueOnly       = "DUE:20060104T000000Z\r\n"
		dueDate       = "DUE;VALUE=DATE:20060104\r\n"
		completedOnly = "COMPLETED:20060105T000000Z\r\n"
		createdOnly   = "CREATED:20060101T000000Z\r\n"
		completed     = "CREATED:20060101T000000Z\r\nCOMPLETED:20060105T000000Z\r\n"
		recurring     = "DTSTART:20060101T090000Z\r\nDUE:20060101T110000Z\r\nRRULE:FREQ=DAILY;COUNT=3\r\n"
	)

	for _, tc := range []struct {
		name       string
		props      string
		start, end string
		want       bool
	}{
		{"DTSTART and DUE, range without start", startDue, "", "20060201T000000Z", true},
		{"DTSTART and DUE, range without start, ends at DTSTART", startDue, "", "20060101T000000Z", false},
		{"DTSTART and DUE, range without end", startDue, "20060103T000000Z", "", true},
		{"DTSTART and DUE, range before DUE", startDue, "20060103T000000Z", "20060105T000000Z", true},
		{"DTSTART and DUE, range starts at DUE", startDue, "20060104T000000Z", "20060105T000000Z", false},
		{"DTSTART and DURATION, range starts at the end", startDuration, "20060104T000000Z", "20060105T000000Z", true},
		{"DTSTART and DURATION, range after the end", startDuration, "20060104T000001Z", "20060105T000000Z", false},
		{"DTSTART and DURATION, range without start", startDuration, "", "20060101T000001Z", true},
		{"DTSTART only, range starts at DTSTART", startOnly, "20060101T000000Z", "20060102T000000Z", true},
		{"DTSTART only, range ends at DTSTART", startOnly, "20051231T000000Z", "20060101T000000Z", false},
		{"DUE only, range ends at DUE", dueOnly, "20060103T000000Z", "20060104T000000Z", true},
		{"DUE only, range starts at DUE", dueOnly, "20060104T000000Z", "20060105T000000Z", false},
		{"DUE date only, range without start", dueDate, "", "20060104T000000Z", true},
		{"COMPLETED and CREATED, range in between", completed, "20060102T000000Z", "20060103T000000Z", true},
		{"COMPLETED and CREATED, range after COMPLETED", completed, "20060106T000000Z", "20060107T000000Z", false},
		{"COMPLETED only, range ends at COMPLETED", completedOnly, "20060104T000000Z", "20060105T000000Z", true},
		{"COMPLETED only, range after COMPLETED", completedOnly, "20060105T000001Z", "20060106T000000Z", false},
		{"CREATED only, range ends at CREATED", createdOnly, "20051201T000000Z", "20060101T000000Z", false},
		{"CREATED only, range without end", createdOnly, "20070101T000000Z", "", true},
		{"no dates", "", "20060101T000000Z", "20060102T000000Z", true},
		{"recurring, an occurrence overlaps", recurring, "20060103T100000Z", "20060103T103000Z", true},
		{"recurring, range after the last occurrence", recurring, "20060104T100000Z", "20060104T103000Z", false},
		{"recurring, range without start", recurring, "", "20060101T090001Z", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			filter := CompFilter{Name: "VTODO"}
			if tc.start != "" {
				filter.Start = toDate(t, tc.start)
			}
			if tc.end != "" {
				filter.End = toDate(t, tc.end)
			}
			query := &CalendarQuery{CompFilter: CompFilter{Name: "VCALENDAR", Comps: []CompFilter{filter}}}

			got, err := Filter(query, []CalendarObject{newCO(tc.props)})
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if matched := len(got) == 1; matched != tc.want {
				t.Fatalf("matched=%v, want %v", matched, tc.want)
			}
		})
	}
}

// A range with only an end matches a VALARM which triggers before it and a
// VJOURNAL which starts before it.
func TestMatchOtherCompTimeRangeWithoutStart(t *testing.T) {
	cal, err := ical.NewDecoder(strings.NewReader("BEGIN:VCALENDAR\r\nVERSION:2.0\r\nPRODID:-//Example Corp.//CalDAV Client//EN\r\n" +
		"BEGIN:VEVENT\r\nDTSTAMP:20060206T001102Z\r\nUID:alarm\r\nSUMMARY:Alarm\r\n" +
		"DTSTART:20060102T100000Z\r\nDURATION:PT1H\r\n" +
		"BEGIN:VALARM\r\nACTION:AUDIO\r\nTRIGGER:-PT10M\r\nEND:VALARM\r\n" +
		"END:VEVENT\r\n" +
		"BEGIN:VJOURNAL\r\nDTSTAMP:20060206T001102Z\r\nUID:journal\r\nSUMMARY:Journal\r\n" +
		"DTSTART;VALUE=DATE:20060102\r\n" +
		"END:VJOURNAL\r\n" +
		"END:VCALENDAR\r\n")).Decode()
	if err != nil {
		t.Fatal(err)
	}
	co := CalendarObject{Data: cal}
	end := toDate(t, "20060201T000000Z")

	for _, tc := range []struct {
		name   string
		filter CompFilter
	}{
		{"VALARM", CompFilter{Name: "VEVENT", Comps: []CompFilter{{Name: "VALARM", End: end}}}},
		{"VJOURNAL", CompFilter{Name: "VJOURNAL", End: end}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			query := &CalendarQuery{CompFilter: CompFilter{Name: "VCALENDAR", Comps: []CompFilter{tc.filter}}}
			got, err := Filter(query, []CalendarObject{co})
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if len(got) != 1 {
				t.Fatalf("matched=false, want true")
			}
		})
	}
}
