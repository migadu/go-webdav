package caldav

import (
	"context"
	"encoding/xml"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// queryCalendarRequest runs QueryCalendar against a test server and returns
// the calendar-query the server received.
func queryCalendarRequest(t *testing.T, query *CalendarQuery) *calendarQuery {
	var body []byte
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var err error
		body, err = io.ReadAll(r.Body)
		if err != nil {
			t.Errorf("failed to read request body: %v", err)
		}
		w.Header().Set("Content-Type", "application/xml; charset=\"utf-8\"")
		w.WriteHeader(http.StatusMultiStatus)
		io.WriteString(w, `<?xml version="1.0" encoding="utf-8" ?><d:multistatus xmlns:d="DAV:"/>`)
	}))
	defer ts.Close()

	c, err := NewClient(nil, ts.URL)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := c.QueryCalendar(context.Background(), "/user/calendars/cal/", query); err != nil {
		t.Fatalf("QueryCalendar() = %v", err)
	}

	var cq calendarQuery
	if err := xml.Unmarshal(body, &cq); err != nil {
		t.Fatalf("failed to decode calendar-query %q: %v", body, err)
	}
	return &cq
}

// Adapted from RFC 4791 section 7.8.9: all pending to-dos, i.e. without a
// COMPLETED property and not cancelled. Additionally, require the to-dos to
// have no alarm and an attendee without a PARTSTAT parameter, so that
// is-not-defined is used at each filter level.
func TestQueryCalendarIsNotDefined(t *testing.T) {
	cq := queryCalendarRequest(t, &CalendarQuery{
		CompFilter: CompFilter{
			Name: "VCALENDAR",
			Comps: []CompFilter{{
				Name: "VTODO",
				Props: []PropFilter{{
					Name:         "COMPLETED",
					IsNotDefined: true,
				}, {
					Name:      "STATUS",
					TextMatch: &TextMatch{Text: "CANCELLED", NegateCondition: true},
				}, {
					Name: "ATTENDEE",
					ParamFilter: []ParamFilter{{
						Name:         "PARTSTAT",
						IsNotDefined: true,
					}},
				}},
				Comps: []CompFilter{{
					Name:         "VALARM",
					IsNotDefined: true,
				}},
			}},
		},
	})

	vcal := cq.Filter.CompFilter
	if vcal.Name != "VCALENDAR" || vcal.IsNotDefined != nil || len(vcal.CompFilters) != 1 {
		t.Fatalf("unexpected VCALENDAR comp-filter: %+v", vcal)
	}
	vtodo := vcal.CompFilters[0]
	if vtodo.Name != "VTODO" || vtodo.IsNotDefined != nil || len(vtodo.PropFilters) != 3 || len(vtodo.CompFilters) != 1 {
		t.Fatalf("unexpected VTODO comp-filter: %+v", vtodo)
	}

	if valarm := vtodo.CompFilters[0]; valarm.Name != "VALARM" || valarm.IsNotDefined == nil {
		t.Errorf("VALARM comp-filter is missing is-not-defined: %+v", valarm)
	}

	if completed := vtodo.PropFilters[0]; completed.Name != "COMPLETED" || completed.IsNotDefined == nil {
		t.Errorf("COMPLETED prop-filter is missing is-not-defined: %+v", completed)
	}

	status := vtodo.PropFilters[1]
	if status.Name != "STATUS" || status.IsNotDefined != nil {
		t.Errorf("unexpected STATUS prop-filter: %+v", status)
	}
	if tm := status.TextMatch; tm == nil || tm.Text != "CANCELLED" || !tm.NegateCondition {
		t.Errorf("STATUS prop-filter has text-match %+v, want negated CANCELLED", tm)
	}

	attendee := vtodo.PropFilters[2]
	if attendee.Name != "ATTENDEE" || attendee.IsNotDefined != nil || len(attendee.ParamFilter) != 1 {
		t.Fatalf("unexpected ATTENDEE prop-filter: %+v", attendee)
	}
	if partstat := attendee.ParamFilter[0]; partstat.Name != "PARTSTAT" || partstat.IsNotDefined == nil {
		t.Errorf("PARTSTAT param-filter is missing is-not-defined: %+v", partstat)
	}

	// The server side must read the same filter back.
	if _, err := decodeCompFilter(&cq.Filter.CompFilter); err != nil {
		t.Errorf("decodeCompFilter() = %v", err)
	}
}

// RFC 4791 sections 9.7.1 to 9.7.3: an is-not-defined element can't have
// siblings.
func TestQueryCalendarIsNotDefinedAlone(t *testing.T) {
	start := time.Date(2006, 1, 4, 0, 0, 0, 0, time.UTC)
	end := time.Date(2006, 1, 5, 0, 0, 0, 0, time.UTC)

	cq := queryCalendarRequest(t, &CalendarQuery{
		CompFilter: CompFilter{
			Name: "VCALENDAR",
			Comps: []CompFilter{{
				Name: "VEVENT",
				Props: []PropFilter{{
					Name:         "LOCATION",
					IsNotDefined: true,
					TextMatch:    &TextMatch{Text: "Office"},
					ParamFilter:  []ParamFilter{{Name: "ALTREP"}},
				}, {
					Name:         "DTEND",
					IsNotDefined: true,
					Start:        start,
					End:          end,
				}, {
					Name: "ATTENDEE",
					ParamFilter: []ParamFilter{{
						Name:         "PARTSTAT",
						IsNotDefined: true,
						TextMatch:    &TextMatch{Text: "NEEDS-ACTION"},
					}},
				}},
				Comps: []CompFilter{{
					Name:         "VALARM",
					IsNotDefined: true,
					Start:        start,
					End:          end,
					Props:        []PropFilter{{Name: "ACTION"}},
					Comps:        []CompFilter{{Name: "X-CHILD"}},
				}},
			}},
		},
	})

	vevent := cq.Filter.CompFilter.CompFilters[0]

	valarm := vevent.CompFilters[0]
	if valarm.IsNotDefined == nil || valarm.TimeRange != nil || len(valarm.PropFilters) != 0 || len(valarm.CompFilters) != 0 {
		t.Errorf("VALARM comp-filter = %+v, want only is-not-defined", valarm)
	}

	for _, pf := range vevent.PropFilters[:2] {
		if pf.IsNotDefined == nil || pf.TimeRange != nil || pf.TextMatch != nil || len(pf.ParamFilter) != 0 {
			t.Errorf("%v prop-filter = %+v, want only is-not-defined", pf.Name, pf)
		}
	}

	partstat := vevent.PropFilters[2].ParamFilter[0]
	if partstat.IsNotDefined == nil || partstat.TextMatch != nil {
		t.Errorf("PARTSTAT param-filter = %+v, want only is-not-defined", partstat)
	}

	if _, err := decodeCompFilter(&cq.Filter.CompFilter); err != nil {
		t.Errorf("decodeCompFilter() = %v", err)
	}
}
