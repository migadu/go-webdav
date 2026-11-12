package caldav

import (
	"strings"
	"testing"
	"time"

	"github.com/emersion/go-ical"
)

// calendarObject returns the calendar object made of the components in body.
func calendarObject(t *testing.T, body string) CalendarObject {
	t.Helper()
	s := "BEGIN:VCALENDAR\r\nVERSION:2.0\r\nPRODID:-//Example Corp.//CalDAV Client//EN\r\n" +
		body + "END:VCALENDAR\r\n"
	cal, err := ical.NewDecoder(strings.NewReader(s)).Decode()
	if err != nil {
		t.Fatal(err)
	}
	return CalendarObject{Data: cal}
}

// timeRangeCase is a time range, open on the side of an empty bound, and
// whether the filter on it must match.
type timeRangeCase struct {
	name       string
	start, end string
	want       bool
}

// testTimeRange checks that co matches the comp-filter made by filter, nested
// in the VCALENDAR one, for each time range in cases as they want.
func testTimeRange(t *testing.T, co CalendarObject, filter func(start, end time.Time) CompFilter, cases []timeRangeCase) {
	t.Helper()
	for _, tc := range cases {
		var start, end time.Time
		if tc.start != "" {
			start = toDate(t, tc.start)
		}
		if tc.end != "" {
			end = toDate(t, tc.end)
		}
		query := CompFilter{Name: "VCALENDAR", Comps: []CompFilter{filter(start, end)}}
		got, err := Match(query, &co)
		if err != nil {
			t.Errorf("%v: Match() = %v", tc.name, err)
		} else if got != tc.want {
			t.Errorf("%v: Match() = %v, want %v", tc.name, got, tc.want)
		}
	}
}

// compTimeRange returns a filter on the component called name, with a time
// range.
func compTimeRange(name string) func(start, end time.Time) CompFilter {
	return func(start, end time.Time) CompFilter {
		return CompFilter{Name: name, Start: start, End: end}
	}
}

// RFC 4791 section 9.9: a VJOURNAL with a DATE-TIME DTSTART lasts no time, one
// with a DATE DTSTART a day, and one without DTSTART overlaps no time range.
func TestMatchJournalTimeRange(t *testing.T) {
	journal := func(props string) CalendarObject {
		return calendarObject(t, "BEGIN:VJOURNAL\r\nDTSTAMP:20060206T001102Z\r\nUID:journal\r\nSUMMARY:Journal\r\n"+
			props+"END:VJOURNAL\r\n")
	}

	testTimeRange(t, journal("DTSTART:20060102T100000Z\r\n"), compTimeRange("VJOURNAL"), []timeRangeCase{
		{"DATE-TIME, range starts at DTSTART", "20060102T100000Z", "20060102T110000Z", true},
		{"DATE-TIME, range ends at DTSTART", "20060102T090000Z", "20060102T100000Z", false},
		{"DATE-TIME, range starts after DTSTART", "20060102T100001Z", "20060102T110000Z", false},
		{"DATE-TIME, range without start", "", "20060102T100001Z", true},
		{"DATE-TIME, range without end", "20060102T100000Z", "", true},
	})
	testTimeRange(t, journal("DTSTART;VALUE=DATE:20060102\r\n"), compTimeRange("VJOURNAL"), []timeRangeCase{
		{"DATE, range within the day", "20060102T120000Z", "20060102T130000Z", true},
		{"DATE, range ends at the start of the day", "20060101T000000Z", "20060102T000000Z", false},
		{"DATE, range starts at the end of the day", "20060103T000000Z", "20060104T000000Z", false},
		{"DATE, range without start", "", "20060102T000001Z", true},
	})
	testTimeRange(t, journal(""), compTimeRange("VJOURNAL"), []timeRangeCase{
		{"no DTSTART", "20060101T000000Z", "20060201T000000Z", false},
		{"no DTSTART, range without start", "", "20060201T000000Z", false},
	})
	testTimeRange(t, journal("DTSTART:20060102T100000Z\r\nRRULE:FREQ=WEEKLY;COUNT=3\r\n"), compTimeRange("VJOURNAL"), []timeRangeCase{
		{"recurring, the third occurrence", "20060116T100000Z", "20060116T100001Z", true},
		{"recurring, after the last occurrence", "20060123T100000Z", "20060124T000000Z", false},
	})
}

// RFC 4791 section 9.9: a VFREEBUSY overlaps a time range by its DTSTART and
// DTEND if it has both, otherwise by its FREEBUSY periods.
func TestMatchFreeBusyTimeRange(t *testing.T) {
	freeBusy := func(props string) CalendarObject {
		return calendarObject(t, "BEGIN:VFREEBUSY\r\nDTSTAMP:20060206T001102Z\r\nUID:freebusy\r\n"+
			props+"END:VFREEBUSY\r\n")
	}

	testTimeRange(t, freeBusy("DTSTART:20060102T000000Z\r\nDTEND:20060103T000000Z\r\n"+
		"FREEBUSY:20060110T000000Z/20060111T000000Z\r\n"), compTimeRange("VFREEBUSY"), []timeRangeCase{
		{"DTSTART and DTEND, range inside", "20060102T100000Z", "20060102T110000Z", true},
		// (start <= DTEND)
		{"DTSTART and DTEND, range starts at DTEND", "20060103T000000Z", "20060104T000000Z", true},
		{"DTSTART and DTEND, range ends at DTSTART", "20060101T000000Z", "20060102T000000Z", false},
		{"DTSTART and DTEND, FREEBUSY is ignored", "20060110T100000Z", "20060110T110000Z", false},
	})
	periods := freeBusy("FREEBUSY:20060102T100000Z/20060102T110000Z,20060104T100000Z/PT1H\r\n" +
		"FREEBUSY;FBTYPE=FREE:20060106T100000Z/20060106T110000Z\r\n")
	testTimeRange(t, periods, compTimeRange("VFREEBUSY"), []timeRangeCase{
		{"period with an end", "20060102T103000Z", "20060102T104000Z", true},
		{"period with a duration", "20060104T105900Z", "20060104T120000Z", true},
		{"free period of another property", "20060106T100000Z", "20060106T100001Z", true},
		{"range ends at a period start", "20060102T090000Z", "20060102T100000Z", false},
		{"range starts at a period end", "20060104T110000Z", "20060104T120000Z", false},
		{"range between periods", "20060103T000000Z", "20060104T000000Z", false},
		{"range without start", "", "20060102T100001Z", true},
	})
	testTimeRange(t, freeBusy(""), compTimeRange("VFREEBUSY"), []timeRangeCase{
		{"no dates", "", "20070101T000000Z", false},
	})
}

// RFC 4791 section 9.9: a VALARM overlaps a time range if one of its triggers
// is in it. A relative trigger is relative to the start or end of the event or
// to-do the alarm is in, so to each of its occurrences if it recurs.
func TestMatchAlarmTimeRange(t *testing.T) {
	alarm := func(parent, parentProps, alarmProps string) CalendarObject {
		return calendarObject(t, "BEGIN:"+parent+"\r\nDTSTAMP:20060206T001102Z\r\nUID:alarm\r\nSUMMARY:Alarm\r\n"+
			parentProps+"BEGIN:VALARM\r\nACTION:DISPLAY\r\nDESCRIPTION:Reminder\r\n"+alarmProps+"END:VALARM\r\n"+
			"END:"+parent+"\r\n")
	}
	inParent := func(parent string) func(start, end time.Time) CompFilter {
		return func(start, end time.Time) CompFilter {
			return CompFilter{Name: parent, Comps: []CompFilter{{Name: "VALARM", Start: start, End: end}}}
		}
	}
	inEvent := inParent("VEVENT")
	const event = "DTSTART:20060102T100000Z\r\nDTEND:20060102T110000Z\r\n"

	testTimeRange(t, alarm("VEVENT", event, "TRIGGER:-PT15M\r\n"), inEvent, []timeRangeCase{
		{"before the start, range starts at the trigger", "20060102T094500Z", "20060102T094501Z", true},
		{"before the start, range ends at the trigger", "20060102T090000Z", "20060102T094500Z", false},
		{"before the start, range is the event", "20060102T100000Z", "20060102T110000Z", false},
		{"before the start, range without start", "", "20060102T094501Z", true},
		{"before the start, range without end", "20060102T094500Z", "", true},
	})
	testTimeRange(t, alarm("VEVENT", event, "TRIGGER;RELATED=END:PT5M\r\n"), inEvent, []timeRangeCase{
		{"after the end", "20060102T110500Z", "20060102T110501Z", true},
		{"after the end, range is the event", "20060102T100000Z", "20060102T110500Z", false},
	})
	testTimeRange(t, alarm("VEVENT", "DTSTART:20060102T100000Z\r\nDURATION:PT2H\r\n", "TRIGGER;RELATED=END:PT0S\r\n"), inEvent, []timeRangeCase{
		{"at the end of an event with a duration", "20060102T120000Z", "20060102T120001Z", true},
	})
	testTimeRange(t, alarm("VEVENT", event, "TRIGGER;VALUE=DATE-TIME:20060101T080000Z\r\n"), inEvent, []timeRangeCase{
		{"absolute", "20060101T080000Z", "20060101T080001Z", true},
		{"absolute, range is the event", "20060102T100000Z", "20060102T110000Z", false},
	})
	repeated := alarm("VEVENT", event, "TRIGGER:-PT30M\r\nREPEAT:2\r\nDURATION:PT10M\r\n")
	testTimeRange(t, repeated, inEvent, []timeRangeCase{
		{"second repetition", "20060102T095000Z", "20060102T095001Z", true},
		{"between repetitions", "20060102T093100Z", "20060102T093900Z", false},
		{"after the last repetition", "20060102T095001Z", "20060102T100000Z", false},
	})
	recurring := alarm("VEVENT", event+"RRULE:FREQ=DAILY;COUNT=3\r\n", "TRIGGER:-PT15M\r\n")
	testTimeRange(t, recurring, inEvent, []timeRangeCase{
		{"recurring, third occurrence", "20060104T094500Z", "20060104T094501Z", true},
		// The occurrence starts after the range, its alarm in it
		{"recurring, range ends before the occurrence", "20060103T094000Z", "20060103T095000Z", true},
		{"recurring, after the last occurrence", "20060105T094500Z", "20060105T094501Z", false},
	})
	forever := alarm("VEVENT", event+"RRULE:FREQ=DAILY\r\n", "TRIGGER:-PT15M\r\n")
	testTimeRange(t, forever, inEvent, []timeRangeCase{
		{"recurring forever, range before the first trigger", "20060101T000000Z", "20060102T094500Z", false},
		{"recurring forever, a year later", "20070102T094500Z", "20070102T094501Z", true},
	})
	testTimeRange(t, alarm("VTODO", "DTSTART:20060102T100000Z\r\nDUE:20060103T100000Z\r\n", "TRIGGER;RELATED=END:-PT1H\r\n"), inParent("VTODO"), []timeRangeCase{
		{"before the due date of a to-do", "20060103T090000Z", "20060103T090001Z", true},
		{"before the due date of a to-do, range at its start", "20060102T090000Z", "20060102T090001Z", false},
	})
	testTimeRange(t, alarm("VEVENT", event, ""), inEvent, []timeRangeCase{
		{"no trigger", "", "20070101T000000Z", false},
	})
}

// RFC 4791 section 7.4 compares the start and end of a property to the time
// range. A DATE value is a whole day, as for the DTSTART of a VEVENT or a
// VJOURNAL in section 9.9.
func TestMatchPropTimeRangeDate(t *testing.T) {
	co := calendarObject(t, "BEGIN:VEVENT\r\nDTSTAMP:20060206T001102Z\r\nUID:date\r\nSUMMARY:Date\r\n"+
		"DTSTART;VALUE=DATE:20060105\r\nEND:VEVENT\r\n")
	dtstart := func(start, end time.Time) CompFilter {
		return CompFilter{Name: "VEVENT", Props: []PropFilter{{Name: "DTSTART", Start: start, End: end}}}
	}
	testTimeRange(t, co, dtstart, []timeRangeCase{
		{"range within the day", "20060105T120000Z", "20060105T130000Z", true},
		{"range ends at the start of the day", "20060104T000000Z", "20060105T000000Z", false},
		{"range starts at the end of the day", "20060106T000000Z", "20060107T000000Z", false},
		{"range without start, ends in the day", "", "20060105T000001Z", true},
		{"range without end, starts in the day", "20060105T235959Z", "", true},
		{"range without end, starts after the day", "20060106T000000Z", "", false},
	})
}

// RFC 4791 section 9.9 defines no time range for VCALENDAR, VTIMEZONE or
// SUMMARY. The server refuses such a filter, but Match, which a backend can
// call with a filter of its own, reports no match rather than an error.
func TestMatchUndefinedTimeRange(t *testing.T) {
	co := calendarObject(t, "BEGIN:VTIMEZONE\r\nTZID:Test\r\nBEGIN:STANDARD\r\nDTSTART:19700101T000000\r\n"+
		"TZOFFSETFROM:+0000\r\nTZOFFSETTO:+0000\r\nEND:STANDARD\r\nEND:VTIMEZONE\r\n"+
		"BEGIN:VEVENT\r\nDTSTAMP:20060206T001102Z\r\nUID:event\r\nSUMMARY:Meeting\r\n"+
		"DTSTART:20060102T100000Z\r\nEND:VEVENT\r\n")
	start, end := toDate(t, "20060101T000000Z"), toDate(t, "20070101T000000Z")
	for _, tc := range []struct {
		name   string
		filter CompFilter
	}{
		{"VCALENDAR", CompFilter{Name: "VCALENDAR", Start: start, End: end}},
		{"VTIMEZONE", CompFilter{Name: "VCALENDAR", Comps: []CompFilter{{Name: "VTIMEZONE", Start: start, End: end}}}},
		{"SUMMARY", CompFilter{Name: "VCALENDAR", Comps: []CompFilter{{
			Name:  "VEVENT",
			Props: []PropFilter{{Name: "SUMMARY", Start: start, End: end}},
		}}}},
	} {
		got, err := Match(tc.filter, &co)
		if err != nil || got {
			t.Errorf("%v: Match() = %v, %v, want false, nil", tc.name, got, err)
		}
	}
}
