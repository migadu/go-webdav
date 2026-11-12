package caldav

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/emersion/go-ical"
)

// mkcalendarRequest is what the iOS 18 Calendar app sends.
const mkcalendarRequest = `<?xml version="1.0" encoding="UTF-8"?>
<B:mkcalendar xmlns:B="urn:ietf:params:xml:ns:caldav">
  <A:set xmlns:A="DAV:">
    <A:prop>
      <B:calendar-timezone>BEGIN:VCALENDAR&#13;
VERSION:2.0&#13;
PRODID:-//Apple Inc.//iPhone OS 18.1.1//EN&#13;
CALSCALE:GREGORIAN&#13;
BEGIN:VTIMEZONE&#13;
TZID:Europe/Paris&#13;
BEGIN:DAYLIGHT&#13;
TZOFFSETFROM:+0100&#13;
RRULE:FREQ=YEARLY;BYMONTH=3;BYDAY=-1SU&#13;
DTSTART:19810329T020000&#13;
TZNAME:UTC+2&#13;
TZOFFSETTO:+0200&#13;
END:DAYLIGHT&#13;
BEGIN:STANDARD&#13;
TZOFFSETFROM:+0200&#13;
RRULE:FREQ=YEARLY;BYMONTH=10;BYDAY=-1SU&#13;
DTSTART:19961027T030000&#13;
TZNAME:UTC+1&#13;
TZOFFSETTO:+0100&#13;
END:STANDARD&#13;
END:VTIMEZONE&#13;
END:VCALENDAR&#13;
</B:calendar-timezone>
      <D:calendar-order xmlns:D="http://apple.com/ns/ical/">2</D:calendar-order>
      <B:supported-calendar-component-set>
        <B:comp name="VEVENT"/>
      </B:supported-calendar-component-set>
      <D:calendar-color xmlns:D="http://apple.com/ns/ical/" symbolic-color="red">#FF2968</D:calendar-color>
      <A:displayname>test calendar</A:displayname>
      <B:calendar-free-busy-set>
        <NO/>
      </B:calendar-free-busy-set>
    </A:prop>
  </A:set>
</B:mkcalendar>
`

func TestMkcalendar(t *testing.T) {
	backend := &createBackend{}
	handler := Handler{Backend: backend}

	req := httptest.NewRequest("MKCALENDAR", "/user/calendars/default/", strings.NewReader(mkcalendarRequest))
	req.Header.Set("Content-Type", "text/xml")
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	if w.Code != http.StatusCreated {
		t.Fatalf("MKCALENDAR returned %v: %s", w.Code, w.Body.String())
	}
	if len(backend.created) != 1 {
		t.Fatalf("CreateCalendar called %d times, want 1", len(backend.created))
	}
	cal := backend.created[0]
	if cal.Path != "/user/calendars/default/" || cal.Name != "test calendar" || cal.Color != "#FF2968" {
		t.Errorf("created calendar %+v, want path, name and color from the request", cal)
	}
	if len(cal.SupportedComponentSet) != 1 || cal.SupportedComponentSet[0] != ical.CompEvent {
		t.Errorf("created calendar with components %v, want VEVENT", cal.SupportedComponentSet)
	}
	if cal.Timezone == nil || cal.Timezone.Children[0].Props.Get(ical.PropTimezoneID).Value != "Europe/Paris" {
		t.Errorf("created calendar with timezone %v, want Europe/Paris", cal.Timezone)
	}
}

func TestMkcalendarEmptyBody(t *testing.T) {
	backend := &createBackend{}
	handler := Handler{Backend: backend}

	req := httptest.NewRequest("MKCALENDAR", "/user/calendars/default/", nil)
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	if w.Code != http.StatusCreated {
		t.Fatalf("MKCALENDAR returned %v: %s", w.Code, w.Body.String())
	}
	if len(backend.created) != 1 || backend.created[0].Path != "/user/calendars/default/" {
		t.Fatalf("created calendars %+v, want one at /user/calendars/default/", backend.created)
	}
}

// mkcalendarBody returns the body of a MKCALENDAR request (RFC 4791 section
// 5.3.1) setting props.
func mkcalendarBody(props string) string {
	return `<?xml version="1.0" encoding="utf-8"?>
<C:mkcalendar xmlns:D="DAV:" xmlns:C="urn:ietf:params:xml:ns:caldav" xmlns:A="http://apple.com/ns/ical/">
  <D:set><D:prop>` + props + `</D:prop></D:set>
</C:mkcalendar>`
}

// The errors of a MKCALENDAR request are those of RFC 4791 section 5.3.1.1.
func TestMkcalendarErrors(t *testing.T) {
	for _, tc := range []struct {
		name        string
		contentType string
		body        string
		// maxBytes, if set, limits the size of the request body
		maxBytes int64
		wantCode int
		wantBody string
	}{
		{
			// "415 (Unsupported Media Type) - The server does not support
			// the request type of the body"
			name:        "body which isn't XML",
			contentType: "text/plain",
			body:        "work",
			wantCode:    http.StatusUnsupportedMediaType,
		},
		{
			name:     "body larger than allowed",
			body:     mkcalendarBody(`<D:displayname>Work</D:displayname>`),
			maxBytes: 64,
			wantCode: http.StatusRequestEntityTooLarge,
		},
		{
			name:     "malformed body",
			body:     `<C:mkcalendar xmlns:C="urn:ietf:params:xml:ns:caldav"><D:set>`,
			wantCode: http.StatusBadRequest,
		},
		{
			name:     "body of an extended MKCOL",
			body:     mkcolBody(mkcolCalendarType),
			wantCode: http.StatusBadRequest,
		},
		{
			// "207 (Multi-Status) - The calendar collection resource was
			// not created since one or more DAV:set instructions specified
			// in the request body could not be processed successfully",
			// with 424 for the properties which would have been set
			name: "invalid timezone",
			body: mkcalendarBody(`<D:displayname>Work</D:displayname>
<C:calendar-timezone>Europe/Berlin</C:calendar-timezone>
<A:calendar-order>2</A:calendar-order>`),
			wantCode: http.StatusMultiStatus,
			wantBody: "multistatus /user/calendars/work/ 409 {urn:ietf:params:xml:ns:caldav}calendar-timezone error {urn:ietf:params:xml:ns:caldav}valid-calendar-data; 424 {DAV:}displayname,{http://apple.com/ns/ical/}calendar-order",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			backend := &createBackend{}
			handler := Handler{Backend: backend}

			req := httptest.NewRequest("MKCALENDAR", "/user/calendars/work/", strings.NewReader(tc.body))
			if tc.contentType != "" {
				req.Header.Set("Content-Type", tc.contentType)
			} else {
				req.Header.Set("Content-Type", "application/xml")
			}
			w := httptest.NewRecorder()
			if tc.maxBytes > 0 {
				req.Body = http.MaxBytesReader(w, req.Body, tc.maxBytes)
			}
			handler.ServeHTTP(w, req)

			if w.Code != tc.wantCode {
				t.Errorf("MKCALENDAR returned %v, want %v: %s", w.Code, tc.wantCode, w.Body.String())
			}
			if tc.wantBody != "" {
				if body := mkcolErrorBody(t, w); body != tc.wantBody {
					t.Errorf("MKCALENDAR body is %q, want %q\n%s", body, tc.wantBody, w.Body.String())
				}
			}
			if len(backend.created) > 0 {
				t.Errorf("MKCALENDAR created %+v", backend.created)
			}
		})
	}
}

// RFC 4791 section 5.3.1.1: calendar collections can't be created anywhere.
func TestMkcalendarLocation(t *testing.T) {
	backend := &createBackend{}
	handler := Handler{Backend: backend, Prefix: "/dav"}

	for _, p := range []string{"/dav/user/calendars/", "/dav/user/calendars/default/event.ics"} {
		req := httptest.NewRequest("MKCALENDAR", p, nil)
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, req)
		if w.Code != http.StatusForbidden {
			t.Errorf("MKCALENDAR %s returned %v, want 403", p, w.Code)
		}
	}
	if len(backend.created) != 0 {
		t.Errorf("created calendars %+v, want none", backend.created)
	}

	req := httptest.NewRequest("MKCALENDAR", "/dav/user/calendars/default/", nil)
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)
	if w.Code != http.StatusCreated {
		t.Errorf("MKCALENDAR below the prefix returned %v, want 201", w.Code)
	}
}

// Clients look for MKCALENDAR in the Allow header before offering to create
// calendars.
func TestOptionsAllowsMkcalendar(t *testing.T) {
	handler := Handler{Backend: &createBackend{}}
	req := httptest.NewRequest(http.MethodOptions, "/user/calendars/", nil)
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)
	if allow := w.Header().Get("Allow"); !strings.Contains(allow, "MKCALENDAR") {
		t.Errorf("Allow: %s, want MKCALENDAR", allow)
	}
}
