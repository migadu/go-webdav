package caldav

import (
	"strings"
	"testing"

	"github.com/emersion/go-ical"
)

func TestMatchIsNotDefinedAndMultiValued(t *testing.T) {
	mustDecode := func(s string) CalendarObject {
		t.Helper()
		cal, err := ical.NewDecoder(strings.NewReader(s)).Decode()
		if err != nil {
			t.Fatal(err)
		}
		return CalendarObject{Data: cal}
	}

	withAlarmAndAttendees := mustDecode("BEGIN:VCALENDAR\r\n" +
		"VERSION:2.0\r\n" +
		"PRODID:-//Test//EN\r\n" +
		"BEGIN:VTIMEZONE\r\n" +
		"TZID:UTC\r\n" +
		"BEGIN:STANDARD\r\n" +
		"DTSTART:19700101T000000\r\n" +
		"TZOFFSETFROM:+0000\r\n" +
		"TZOFFSETTO:+0000\r\n" +
		"END:STANDARD\r\n" +
		"END:VTIMEZONE\r\n" +
		"BEGIN:VEVENT\r\n" +
		"UID:ev-1\r\n" +
		"DTSTAMP:20260101T000000Z\r\n" +
		"DTSTART:20260102T100000Z\r\n" +
		"SUMMARY:Team Sync\r\n" +
		"ATTENDEE;PARTSTAT=ACCEPTED;DELEGATED-TO=\"mailto:a@example.com\",\"mailto:b@example.com\":mailto:cyrus@example.com\r\n" +
		"ATTENDEE;PARTSTAT=NEEDS-ACTION:mailto:lisa@example.com\r\n" +
		"CATEGORIES:WORK\r\n" +
		"CATEGORIES:URGENT\r\n" +
		"BEGIN:VALARM\r\n" +
		"ACTION:DISPLAY\r\n" +
		"DESCRIPTION:Reminder\r\n" +
		"TRIGGER:-PT15M\r\n" +
		"END:VALARM\r\n" +
		"END:VEVENT\r\n" +
		"END:VCALENDAR\r\n")

	withoutSummaryOrAlarm := mustDecode("BEGIN:VCALENDAR\r\n" +
		"VERSION:2.0\r\n" +
		"PRODID:-//Test//EN\r\n" +
		"BEGIN:VEVENT\r\n" +
		"UID:ev-2\r\n" +
		"DTSTAMP:20260101T000000Z\r\n" +
		"DTSTART:20260102T120000Z\r\n" +
		"ATTENDEE:mailto: plain@example.com\r\n" +
		"END:VEVENT\r\n" +
		"END:VCALENDAR\r\n")

	tests := []struct {
		name   string
		obj    CalendarObject
		filter CompFilter
		want   bool
	}{
		{
			name: "CompFilter IsNotDefined false when VEVENT present alongside VTIMEZONE",
			obj:  withAlarmAndAttendees,
			filter: CompFilter{
				Name:  "VCALENDAR",
				Comps: []CompFilter{{Name: "VEVENT", IsNotDefined: true}},
			},
			want: false,
		},
		{
			name: "CompFilter IsNotDefined false when VEVENT present alone",
			obj:  withoutSummaryOrAlarm,
			filter: CompFilter{
				Name:  "VCALENDAR",
				Comps: []CompFilter{{Name: "VEVENT", IsNotDefined: true}},
			},
			want: false,
		},
		{
			name: "CompFilter IsNotDefined true when VTODO absent",
			obj:  withAlarmAndAttendees,
			filter: CompFilter{
				Name:  "VCALENDAR",
				Comps: []CompFilter{{Name: "VTODO", IsNotDefined: true}},
			},
			want: true,
		},
		{
			name: "nested CompFilter IsNotDefined false when VALARM present",
			obj:  withAlarmAndAttendees,
			filter: CompFilter{
				Name: "VCALENDAR",
				Comps: []CompFilter{{
					Name:  "VEVENT",
					Comps: []CompFilter{{Name: "VALARM", IsNotDefined: true}},
				}},
			},
			want: false,
		},
		{
			name: "nested CompFilter IsNotDefined true when VALARM absent",
			obj:  withoutSummaryOrAlarm,
			filter: CompFilter{
				Name: "VCALENDAR",
				Comps: []CompFilter{{
					Name:  "VEVENT",
					Comps: []CompFilter{{Name: "VALARM", IsNotDefined: true}},
				}},
			},
			want: true,
		},
		{
			name:   "top-level CompFilter IsNotDefined false on VCALENDAR",
			obj:    withAlarmAndAttendees,
			filter: CompFilter{Name: "VCALENDAR", IsNotDefined: true},
			want:   false,
		},
		{
			name: "PropFilter IsNotDefined false when SUMMARY present",
			obj:  withAlarmAndAttendees,
			filter: CompFilter{
				Name: "VCALENDAR",
				Comps: []CompFilter{{
					Name:  "VEVENT",
					Props: []PropFilter{{Name: "SUMMARY", IsNotDefined: true}},
				}},
			},
			want: false,
		},
		{
			name: "PropFilter IsNotDefined true when SUMMARY absent",
			obj:  withoutSummaryOrAlarm,
			filter: CompFilter{
				Name: "VCALENDAR",
				Comps: []CompFilter{{
					Name:  "VEVENT",
					Props: []PropFilter{{Name: "SUMMARY", IsNotDefined: true}},
				}},
			},
			want: true,
		},
		{
			name: "multi-valued property matches second CATEGORIES occurrence",
			obj:  withAlarmAndAttendees,
			filter: CompFilter{
				Name: "VCALENDAR",
				Comps: []CompFilter{{
					Name: "VEVENT",
					Props: []PropFilter{{
						Name:      "CATEGORIES",
						TextMatch: &TextMatch{Text: "URGENT"},
					}},
				}},
			},
			want: true,
		},
		{
			name: "multi-valued ATTENDEE matches second occurrence with ParamFilter and TextMatch",
			obj:  withAlarmAndAttendees,
			filter: CompFilter{
				Name: "VCALENDAR",
				Comps: []CompFilter{{
					Name: "VEVENT",
					Props: []PropFilter{{
						Name:      "ATTENDEE",
						TextMatch: &TextMatch{Text: "lisa@example.com"},
						ParamFilter: []ParamFilter{{
							Name:      "PARTSTAT",
							TextMatch: &TextMatch{Text: "NEEDS-ACTION"},
						}},
					}},
				}},
			},
			want: true,
		},
		{
			name: "multi-valued ATTENDEE does not cross-match ParamFilter from first and TextMatch from second",
			obj:  withAlarmAndAttendees,
			filter: CompFilter{
				Name: "VCALENDAR",
				Comps: []CompFilter{{
					Name: "VEVENT",
					Props: []PropFilter{{
						Name:      "ATTENDEE",
						TextMatch: &TextMatch{Text: "lisa@example.com"},
						ParamFilter: []ParamFilter{{
							Name:      "PARTSTAT",
							TextMatch: &TextMatch{Text: "ACCEPTED"},
						}},
					}},
				}},
			},
			want: false,
		},
		{
			name: "multi-valued parameter matches second parameter value",
			obj:  withAlarmAndAttendees,
			filter: CompFilter{
				Name: "VCALENDAR",
				Comps: []CompFilter{{
					Name: "VEVENT",
					Props: []PropFilter{{
						Name: "ATTENDEE",
						ParamFilter: []ParamFilter{{
							Name:      "DELEGATED-TO",
							TextMatch: &TextMatch{Text: "b@example.com"},
						}},
					}},
				}},
			},
			want: true,
		},
		{
			name: "multi-valued parameter negated TextMatch fails when second value matches",
			obj:  withAlarmAndAttendees,
			filter: CompFilter{
				Name: "VCALENDAR",
				Comps: []CompFilter{{
					Name: "VEVENT",
					Props: []PropFilter{{
						Name:      "ATTENDEE",
						TextMatch: &TextMatch{Text: "cyrus@example.com"},
						ParamFilter: []ParamFilter{{
							Name:      "DELEGATED-TO",
							TextMatch: &TextMatch{Text: "b@example.com", NegateCondition: true},
						}},
					}},
				}},
			},
			want: false,
		},
		{
			name: "ParamFilter IsNotDefined true when parameter absent on ATTENDEE",
			obj:  withoutSummaryOrAlarm,
			filter: CompFilter{
				Name: "VCALENDAR",
				Comps: []CompFilter{{
					Name: "VEVENT",
					Props: []PropFilter{{
						Name:        "ATTENDEE",
						ParamFilter: []ParamFilter{{Name: "PARTSTAT", IsNotDefined: true}},
					}},
				}},
			},
			want: true,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := Match(tc.filter, &tc.obj)
			if err != nil {
				t.Fatalf("Match() unexpected error: %v", err)
			}
			if got != tc.want {
				t.Errorf("Match() = %v, want %v", got, tc.want)
			}
		})
	}
}
