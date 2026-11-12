package caldav

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/emersion/go-ical"
)

// putBackend records the paths of the calendar objects it stores.
type putBackend struct {
	testBackend
	inbox *Inbox
	puts  []string
}

func (b *putBackend) GetInbox(ctx context.Context) (*Inbox, error) {
	return b.inbox, nil
}

func (b *putBackend) PutCalendarObject(ctx context.Context, path string, calendar *ical.Calendar, opts *PutCalendarObjectOptions) (*CalendarObject, error) {
	b.puts = append(b.puts, path)
	return &CalendarObject{Path: path}, nil
}

const putTimezoneEventData = `BEGIN:VCALENDAR
VERSION:2.0
PRODID:-//Example Corp.//CalDAV Client//EN
BEGIN:VTIMEZONE
TZID:US/Eastern
BEGIN:STANDARD
DTSTART:20001026T020000
RRULE:FREQ=YEARLY;BYDAY=-1SU;BYMONTH=10
TZOFFSETFROM:-0400
TZOFFSETTO:-0500
END:STANDARD
END:VTIMEZONE
BEGIN:VEVENT
DTSTAMP:20060206T001102Z
DTSTART;TZID=US/Eastern:20060102T100000
UID:74855313FA803DA593CD579A@example.com
END:VEVENT
END:VCALENDAR
`

// RFC 4791 section 5.2.3: a calendar only holds calendar objects with only
// VTIMEZONE components if its supported-calendar-component-set lists
// VTIMEZONE, and storing a component type it doesn't list fails with
// CALDAV:supported-calendar-component. Without SupportedComponentSet, the
// server advertises VEVENT only.
func TestPutTimezoneOnlyCalendarObject(t *testing.T) {
	const (
		calPath = "/user/calendars/work/"
		objPath = calPath + "tz.ics"
	)
	for _, tc := range []struct {
		name       string
		components []string
		data       string
		wantStored bool
	}{
		{"default component set", nil, calendarTimezoneData, false},
		{"empty component set", []string{}, calendarTimezoneData, false},
		{"without VTIMEZONE", []string{ical.CompEvent, ical.CompToDo}, calendarTimezoneData, false},
		{"with VTIMEZONE", []string{ical.CompEvent, ical.CompTimezone}, calendarTimezoneData, true},
		{"with lowercase vtimezone", []string{"vtimezone"}, calendarTimezoneData, true},
		// VTIMEZONE components along with others are always supported
		{"event with VTIMEZONE, default component set", nil, putTimezoneEventData, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			b := &putBackend{testBackend: testBackend{calendars: []Calendar{{Path: calPath, SupportedComponentSet: tc.components}}}}
			req := httptest.NewRequest(http.MethodPut, objPath, strings.NewReader(tc.data))
			req.Header.Set("Content-Type", ical.MIMEType)
			w := httptest.NewRecorder()
			(&Handler{Backend: b}).ServeHTTP(w, req)

			if tc.wantStored {
				if w.Code != http.StatusCreated {
					t.Errorf("status = %d, want 201; body:\n%s", w.Code, w.Body.String())
				}
				if len(b.puts) != 1 || b.puts[0] != objPath {
					t.Errorf("PutCalendarObject called for %q, want %q", b.puts, objPath)
				}
				return
			}
			if w.Code != http.StatusForbidden {
				t.Errorf("status = %d, want 403; body:\n%s", w.Code, w.Body.String())
			}
			if !hasCalDAVPrecondition(t, w, PreconditionSupportedCalendarComponent) {
				t.Errorf("body isn't a DAV:error with CALDAV:supported-calendar-component:\n%s", w.Body.String())
			}
			if len(b.puts) != 0 {
				t.Errorf("PutCalendarObject called for %q", b.puts)
			}
		})
	}
}

// The members of the scheduling Inbox are scheduling messages, which the
// backend checks: the Inbox isn't a calendar.
func TestPutTimezoneOnlyInboxMember(t *testing.T) {
	const inboxPath = "/user/calendars/inbox/"
	b := &putBackend{inbox: &Inbox{Path: inboxPath}}
	req := httptest.NewRequest(http.MethodPut, inboxPath+"tz.ics", strings.NewReader(calendarTimezoneData))
	req.Header.Set("Content-Type", ical.MIMEType)
	w := httptest.NewRecorder()
	(&Handler{Backend: b}).ServeHTTP(w, req)
	if w.Code != http.StatusCreated {
		t.Errorf("status = %d, want 201; body:\n%s", w.Code, w.Body.String())
	}
	if len(b.puts) != 1 {
		t.Errorf("PutCalendarObject called for %q, want %q", b.puts, inboxPath+"tz.ics")
	}
}
