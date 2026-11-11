package caldav

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

type queryRecorderBackend struct {
	testBackend
	got *CalendarQuery
}

func (b *queryRecorderBackend) QueryCalendarObjects(ctx context.Context, path string, query *CalendarQuery) ([]CalendarObject, error) {
	q := *query
	b.got = &q
	return nil, nil
}

func TestDateWithUTCTimeMarshalNonUTC(t *testing.T) {
	plus2 := time.FixedZone("UTC+2", 2*60*60)
	minus5 := time.FixedZone("UTC-5", -5*60*60)

	// 12:00 in UTC+2 is 10:00 UTC; 09:30 in UTC-5 is 14:30 UTC.
	compStart := time.Date(2026, 3, 15, 12, 0, 0, 0, plus2)
	compEnd := time.Date(2026, 3, 15, 9, 30, 0, 0, minus5)
	propStart := time.Date(2026, 3, 15, 13, 15, 0, 0, plus2)
	propEnd := time.Date(2026, 3, 15, 10, 45, 0, 0, minus5)
	expandStart := time.Date(2026, 3, 1, 2, 0, 0, 0, plus2)
	expandEnd := time.Date(2026, 3, 31, 19, 0, 0, 0, minus5)

	rec := &queryRecorderBackend{}
	srv := httptest.NewServer(&Handler{Backend: rec})
	defer srv.Close()

	c, err := NewClient(http.DefaultClient, srv.URL)
	if err != nil {
		t.Fatal(err)
	}

	_, err = c.QueryCalendar(context.Background(), "/user/calendars/work/", &CalendarQuery{
		CompRequest: CalendarCompRequest{
			Name: "VCALENDAR",
			Expand: &CalendarExpandRequest{
				Start: expandStart,
				End:   expandEnd,
			},
		},
		CompFilter: CompFilter{
			Name: "VCALENDAR",
			Comps: []CompFilter{{
				Name:  "VEVENT",
				Start: compStart,
				End:   compEnd,
				Props: []PropFilter{{
					Name:  "DTSTART",
					Start: propStart,
					End:   propEnd,
				}},
			}},
		},
	})
	if err != nil {
		t.Fatalf("QueryCalendar() = %v", err)
	}
	if rec.got == nil {
		t.Fatal("server did not receive query")
	}

	vevent := rec.got.CompFilter.Comps[0]
	if !vevent.Start.Equal(compStart) {
		t.Errorf("CompFilter.Start = %v, want %v (UTC %v)", vevent.Start, compStart, compStart.UTC())
	}
	if !vevent.End.Equal(compEnd) {
		t.Errorf("CompFilter.End = %v, want %v (UTC %v)", vevent.End, compEnd, compEnd.UTC())
	}
	pf := vevent.Props[0]
	if !pf.Start.Equal(propStart) {
		t.Errorf("PropFilter.Start = %v, want %v (UTC %v)", pf.Start, propStart, propStart.UTC())
	}
	if !pf.End.Equal(propEnd) {
		t.Errorf("PropFilter.End = %v, want %v (UTC %v)", pf.End, propEnd, propEnd.UTC())
	}
	if rec.got.CompRequest.Expand == nil {
		t.Fatal("Expand = nil, want non-nil")
	}
	if !rec.got.CompRequest.Expand.Start.Equal(expandStart) {
		t.Errorf("Expand.Start = %v, want %v (UTC %v)", rec.got.CompRequest.Expand.Start, expandStart, expandStart.UTC())
	}
	if !rec.got.CompRequest.Expand.End.Equal(expandEnd) {
		t.Errorf("Expand.End = %v, want %v (UTC %v)", rec.got.CompRequest.Expand.End, expandEnd, expandEnd.UTC())
	}
}
