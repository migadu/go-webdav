package caldav

import (
	"strings"
	"testing"

	"github.com/emersion/go-ical"
)

func TestValidateCalendarObjectExtraConstraints(t *testing.T) {
	for _, tc := range []struct {
		name    string
		data    string
		wantErr string
	}{
		{
			name:    "VTIMEZONE-only calendar without VEVENT/VTODO/VJOURNAL",
			data:    calendarTimezoneData,
			wantErr: "component",
		},
		{
			name: "VEVENT with empty UID value",
			data: `BEGIN:VCALENDAR
VERSION:2.0
PRODID:-//Example Corp.//CalDAV Client//EN
BEGIN:VEVENT
DTSTAMP:20060206T001102Z
DTSTART:20060102T150000Z
UID:
END:VEVENT
END:VCALENDAR`,
			wantErr: ical.PropUID,
		},
		{
			name: "second VEVENT missing empty UID",
			data: `BEGIN:VCALENDAR
VERSION:2.0
PRODID:-//Example Corp.//CalDAV Client//EN
BEGIN:VEVENT
DTSTAMP:20060206T001102Z
DTSTART:20060102T150000Z
UID:first-uid@example.com
END:VEVENT
BEGIN:VEVENT
DTSTAMP:20060206T001102Z
DTSTART:20060103T150000Z
UID:
END:VEVENT
END:VCALENDAR`,
			wantErr: ical.PropUID,
		},
		{
			name: "VEVENT without DTSTART",
			data: `BEGIN:VCALENDAR
VERSION:2.0
PRODID:-//Example Corp.//CalDAV Client//EN
BEGIN:VEVENT
DTSTAMP:20060206T001102Z
UID:74855313FA803DA593CD579A@example.com
SUMMARY:No DTSTART
END:VEVENT
END:VCALENDAR`,
			wantErr: ical.PropDateTimeStart,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cal, err := ical.NewDecoder(strings.NewReader(tc.data)).Decode()
			if err != nil {
				t.Fatalf("Decode: %v", err)
			}
			eventType, uid, err := ValidateCalendarObject(cal)
			if err == nil {
				t.Fatalf("ValidateCalendarObject() = (%q, %q, nil), want error containing %q", eventType, uid, tc.wantErr)
			}
			if !strings.Contains(err.Error(), tc.wantErr) {
				t.Errorf("ValidateCalendarObject() error = %q, want substring %q", err.Error(), tc.wantErr)
			}
		})
	}
}
