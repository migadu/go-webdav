package caldav

import (
	"strings"
	"testing"

	"github.com/emersion/go-ical"
)

func TestValidateCalendarObject(t *testing.T) {
	for _, tc := range []struct {
		name          string
		data          string
		wantEventType string
		wantUID       string
		wantErr       string // substring of the error, empty if valid
	}{
		{
			// RFC 4791 appendix B, abcd1.ics
			name: "valid event with timezone",
			data: `BEGIN:VCALENDAR
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
END:VCALENDAR`,
			wantEventType: ical.CompEvent,
			wantUID:       "74855313FA803DA593CD579A@example.com",
		},
		{
			// RFC 5545 section 3.6 allows an iCalendar object with only
			// VTIMEZONE components, and VTIMEZONE has no UID
			name:          "VTIMEZONE only",
			data:          calendarTimezoneData,
			wantEventType: ical.CompTimezone,
			wantUID:       "",
		},
		{
			// RFC 4791 appendix B, abcd2.ics without its VTIMEZONE: a
			// recurring event with overridden instances sharing its UID
			name: "valid recurring event",
			data: `BEGIN:VCALENDAR
VERSION:2.0
PRODID:-//Example Corp.//CalDAV Client//EN
BEGIN:VEVENT
DTSTAMP:20060206T001121Z
DTSTART:20060102T170000Z
DURATION:PT1H
RRULE:FREQ=DAILY;COUNT=5
SUMMARY:Event #2
UID:00959BC664CA650E933C892C@example.com
END:VEVENT
BEGIN:VEVENT
DTSTAMP:20060206T001121Z
DTSTART:20060104T190000Z
DURATION:PT1H
RECURRENCE-ID:20060104T170000Z
SUMMARY:Event #2 bis
UID:00959BC664CA650E933C892C@example.com
END:VEVENT
END:VCALENDAR`,
			wantEventType: ical.CompEvent,
			wantUID:       "00959BC664CA650E933C892C@example.com",
		},
		{
			// RFC 4791 appendix B, abcd4.ics
			name: "valid to-do with alarm",
			data: `BEGIN:VCALENDAR
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
END:VCALENDAR`,
			wantEventType: ical.CompToDo,
			wantUID:       "DDDEEB7915FA61233B861457@example.com",
		},
		{
			name: "missing PRODID",
			data: `BEGIN:VCALENDAR
VERSION:2.0
BEGIN:VEVENT
DTSTAMP:20060206T001102Z
DTSTART:20060102T150000Z
UID:74855313FA803DA593CD579A@example.com
END:VEVENT
END:VCALENDAR`,
			wantErr: ical.PropProductID,
		},
		{
			name: "missing VERSION",
			data: `BEGIN:VCALENDAR
PRODID:-//Example Corp.//CalDAV Client//EN
BEGIN:VEVENT
DTSTAMP:20060206T001102Z
DTSTART:20060102T150000Z
UID:74855313FA803DA593CD579A@example.com
END:VEVENT
END:VCALENDAR`,
			wantErr: ical.PropVersion,
		},
		{
			name: "event missing DTSTAMP",
			data: `BEGIN:VCALENDAR
VERSION:2.0
PRODID:-//Example Corp.//CalDAV Client//EN
BEGIN:VEVENT
DTSTART:20060102T150000Z
UID:74855313FA803DA593CD579A@example.com
END:VEVENT
END:VCALENDAR`,
			wantErr: ical.PropDateTimeStamp,
		},
		{
			name: "event missing UID",
			data: `BEGIN:VCALENDAR
VERSION:2.0
PRODID:-//Example Corp.//CalDAV Client//EN
BEGIN:VEVENT
DTSTAMP:20060206T001102Z
DTSTART:20060102T150000Z
END:VEVENT
END:VCALENDAR`,
			wantErr: ical.PropUID,
		},
		{
			name: "METHOD",
			data: `BEGIN:VCALENDAR
VERSION:2.0
PRODID:-//Example Corp.//CalDAV Client//EN
METHOD:REQUEST
BEGIN:VEVENT
DTSTAMP:20060206T001102Z
DTSTART:20060102T150000Z
UID:74855313FA803DA593CD579A@example.com
END:VEVENT
END:VCALENDAR`,
			wantErr: ical.PropMethod,
		},
		{
			name: "conflicting UIDs",
			data: `BEGIN:VCALENDAR
VERSION:2.0
PRODID:-//Example Corp.//CalDAV Client//EN
BEGIN:VEVENT
DTSTAMP:20060206T001102Z
DTSTART:20060102T150000Z
UID:74855313FA803DA593CD579A@example.com
END:VEVENT
BEGIN:VEVENT
DTSTAMP:20060206T001102Z
DTSTART:20060103T150000Z
UID:00959BC664CA650E933C892C@example.com
END:VEVENT
END:VCALENDAR`,
			wantErr: "UID",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cal, err := ical.NewDecoder(strings.NewReader(tc.data)).Decode()
			if err != nil {
				t.Fatalf("failed to decode calendar: %v", err)
			}

			eventType, uid, err := ValidateCalendarObject(cal)
			if tc.wantErr != "" {
				if err == nil {
					t.Fatalf("ValidateCalendarObject() = %q, %q, nil, want an error about %v", eventType, uid, tc.wantErr)
				}
				if !strings.Contains(err.Error(), tc.wantErr) {
					t.Errorf("ValidateCalendarObject() = %v, want an error about %v", err, tc.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("ValidateCalendarObject() = %v", err)
			}
			if eventType != tc.wantEventType {
				t.Errorf("eventType = %q, want %q", eventType, tc.wantEventType)
			}
			if uid != tc.wantUID {
				t.Errorf("uid = %q, want %q", uid, tc.wantUID)
			}
		})
	}
}
