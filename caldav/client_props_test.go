package caldav

import (
	"context"
	"net/http/httptest"
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
