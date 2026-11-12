package caldav

import (
	"context"
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"

	"github.com/emersion/go-ical"
)

func TestClientFindCalendarCollectionProps(t *testing.T) {
	tz, err := decodeCalendarTimezone(calendarTimezoneData)
	if err != nil {
		t.Fatal(err)
	}

	b := testBackend{
		calendars: []Calendar{
			{
				Path:        "/user/calendars/work/",
				Name:        "Work",
				Description: "Work meetings",
				Color:       "#112233",
				Timezone:    tz,
				SyncToken:   "urn:sync:work-1",
				CTag:        "ctag-work-1",
				ReadOnly:    true,
			},
			{
				Path:      "/user/calendars/personal/",
				Name:      "Personal",
				SyncToken: "urn:sync:pers-1",
				CTag:      "ctag-pers-1",
				ReadOnly:  false,
			},
		},
	}
	srv := httptest.NewServer(&Handler{Backend: b})
	defer srv.Close()

	c, err := NewClient(srv.Client(), srv.URL)
	if err != nil {
		t.Fatal(err)
	}

	cals, err := c.FindCalendars(context.Background(), "/user/calendars/")
	if err != nil {
		t.Fatalf("FindCalendars: %v", err)
	}
	if len(cals) != 2 {
		t.Fatalf("got %d calendars, want 2", len(cals))
	}

	work := cals[0]
	if work.Color != "#112233" {
		t.Errorf("work.Color = %q, want %q", work.Color, "#112233")
	}
	if work.Timezone == nil {
		t.Errorf("work.Timezone = nil, want US-Eastern VTIMEZONE")
	} else if tzid := work.Timezone.Children[0].Props.Get(ical.PropTimezoneID); tzid == nil || tzid.Value != "US-Eastern" {
		t.Errorf("work.Timezone TZID = %v, want US-Eastern", tzid)
	}
	if work.SyncToken != "urn:sync:work-1" {
		t.Errorf("work.SyncToken = %q, want %q", work.SyncToken, "urn:sync:work-1")
	}
	if work.CTag != "ctag-work-1" {
		t.Errorf("work.CTag = %q, want %q", work.CTag, "ctag-work-1")
	}
	if !work.ReadOnly {
		t.Errorf("work.ReadOnly = false, want true")
	}

	pers := cals[1]
	if pers.ReadOnly {
		t.Errorf("pers.ReadOnly = true, want false")
	}

	single, err := c.FindCalendar(context.Background(), "/user/calendars/work/")
	if err != nil {
		t.Fatalf("FindCalendar: %v", err)
	}
	if single.Color != "#112233" || single.CTag != "ctag-work-1" || single.SyncToken != "urn:sync:work-1" || !single.ReadOnly || single.Timezone == nil {
		t.Errorf("FindCalendar returned incomplete properties: %+v", single)
	}
}

// TestClientFindCalendarsOptionalPropErrors checks that a property which
// only fills in an optional field of Calendar is left out if the server fails
// to return it, or returns a value which can't be decoded, rather than failing
// the request. RFC 4918 section 9.1.2: "clients should be prepared to handle
// other 2/3/4/5xx series status codes as well". RFC 3744 section 3.7 has
// DAV:current-user-privilege-set need its own privilege, so a server can
// refuse it with 403 Forbidden.
func TestClientFindCalendarsOptionalPropErrors(t *testing.T) {
	for _, tc := range []struct {
		name, prop, status string
	}{
		{"current-user-privilege-set 403", `<D:current-user-privilege-set/>`, "403 Forbidden"},
		{"sync-token 403", `<D:sync-token/>`, "403 Forbidden"},
		{"getctag 500", `<CS:getctag/>`, "500 Internal Server Error"},
		{"calendar-color 401", `<A:calendar-color/>`, "401 Unauthorized"},
		{"calendar-timezone 403", `<C:calendar-timezone/>`, "403 Forbidden"},
		{"unparsable calendar-timezone", `<C:calendar-timezone>not an iCalendar object</C:calendar-timezone>`, "200 OK"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/xml; charset=utf-8")
				w.WriteHeader(http.StatusMultiStatus)
				w.Write([]byte(`<?xml version="1.0" encoding="utf-8"?>
<D:multistatus xmlns:D="DAV:" xmlns:C="urn:ietf:params:xml:ns:caldav" xmlns:CS="http://calendarserver.org/ns/" xmlns:A="http://apple.com/ns/ical/">
  <D:response>
    <D:href>/cal/work/</D:href>
    <D:propstat>
      <D:prop>
        <D:resourcetype><D:collection/><C:calendar/></D:resourcetype>
        <D:displayname>Work</D:displayname>
      </D:prop>
      <D:status>HTTP/1.1 200 OK</D:status>
    </D:propstat>
    <D:propstat>
      <D:prop>` + tc.prop + `</D:prop>
      <D:status>HTTP/1.1 ` + tc.status + `</D:status>
    </D:propstat>
  </D:response>
</D:multistatus>`))
			}))
			defer srv.Close()

			c, err := NewClient(srv.Client(), srv.URL)
			if err != nil {
				t.Fatal(err)
			}

			want := Calendar{Path: "/cal/work/", Name: "Work", SupportedComponentSet: []string{}}
			cals, err := c.FindCalendars(context.Background(), "/cal/")
			if err != nil {
				t.Errorf("FindCalendars() = %v", err)
			} else if !reflect.DeepEqual(cals, []Calendar{want}) {
				t.Errorf("FindCalendars() = %+v, want %+v", cals, []Calendar{want})
			}
			cal, err := c.FindCalendar(context.Background(), "/cal/work/")
			if err != nil {
				t.Errorf("FindCalendar() = %v", err)
			} else if !reflect.DeepEqual(*cal, want) {
				t.Errorf("FindCalendar() = %+v, want %+v", *cal, want)
			}
		})
	}
}
