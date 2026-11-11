package caldav

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/emersion/go-ical"
	"github.com/emersion/go-webdav"
)

type collationQueryBackend struct {
	testBackend
	got *CalendarQuery
}

func (b *collationQueryBackend) QueryCalendarObjects(ctx context.Context, path string, query *CalendarQuery) ([]CalendarObject, error) {
	q := *query
	b.got = &q
	return nil, nil
}

func TestTextMatchCollation(t *testing.T) {
	cal, err := ical.NewDecoder(strings.NewReader("BEGIN:VCALENDAR\r\n" +
		"VERSION:2.0\r\n" +
		"PRODID:-//Test//EN\r\n" +
		"BEGIN:VEVENT\r\n" +
		"UID:ev-collation\r\n" +
		"DTSTAMP:20260101T000000Z\r\n" +
		"DTSTART:20260102T100000Z\r\n" +
		"SUMMARY:Café Meeting\r\n" +
		"ATTENDEE;CN=\"René Müller\";ROLE=OPT-PARTICIPANT:mailto:rene@example.com\r\n" +
		"END:VEVENT\r\n" +
		"END:VCALENDAR\r\n")).Decode()
	if err != nil {
		t.Fatal(err)
	}
	co := CalendarObject{Data: cal}

	queryWith := func(tm TextMatch) CompFilter {
		return CompFilter{
			Name: "VCALENDAR",
			Comps: []CompFilter{{
				Name:  "VEVENT",
				Props: []PropFilter{{Name: "SUMMARY", TextMatch: &tm}},
			}},
		}
	}

	t.Run("default collation is i;ascii-casemap", func(t *testing.T) {
		ok, err := Match(queryWith(TextMatch{Text: "meeting"}), &co)
		if err != nil {
			t.Fatalf("Match() error = %v", err)
		}
		if !ok {
			t.Errorf("Match(default ascii-casemap 'meeting') = false, want true")
		}

		// Non-ASCII É should NOT match é under i;ascii-casemap (RFC 4790 §9.2)
		okAccent, err := Match(queryWith(TextMatch{Text: "CAFÉ"}), &co)
		if err != nil {
			t.Fatalf("Match() error = %v", err)
		}
		if okAccent {
			t.Errorf("Match(default ascii-casemap 'CAFÉ' vs 'Café') = true, want false")
		}
	})

	t.Run("i;octet collation is case-sensitive", func(t *testing.T) {
		okLower, err := Match(queryWith(TextMatch{Text: "meeting", Collation: "i;octet"}), &co)
		if err != nil {
			t.Fatalf("Match() error = %v", err)
		}
		if okLower {
			t.Errorf("Match(i;octet 'meeting' vs 'Meeting') = true, want false")
		}

		okExact, err := Match(queryWith(TextMatch{Text: "Meeting", Collation: "i;octet"}), &co)
		if err != nil {
			t.Fatalf("Match() error = %v", err)
		}
		if !okExact {
			t.Errorf("Match(i;octet 'Meeting') = false, want true")
		}
	})

	t.Run("i;unicode-casemap folds Unicode case", func(t *testing.T) {
		ok, err := Match(queryWith(TextMatch{Text: "CAFÉ", Collation: "i;unicode-casemap"}), &co)
		if err != nil {
			t.Fatalf("Match() error = %v", err)
		}
		if !ok {
			t.Errorf("Match(i;unicode-casemap 'CAFÉ' vs 'Café') = false, want true")
		}
	})

	t.Run("Match rejects unsupported collation", func(t *testing.T) {
		_, err := Match(queryWith(TextMatch{Text: "Meeting", Collation: "i;unknown"}), &co)
		if err == nil {
			t.Errorf("Match(unsupported collation) = nil error, want error")
		}
	})

	t.Run("ParamFilter.TextMatch applies collation and rejects unsupported collation", func(t *testing.T) {
		paramQuery := func(tm TextMatch) CompFilter {
			return CompFilter{
				Name: "VCALENDAR",
				Comps: []CompFilter{{
					Name: "VEVENT",
					Props: []PropFilter{{
						Name:        "ATTENDEE",
						ParamFilter: []ParamFilter{{Name: "CN", TextMatch: &tm}},
					}},
				}},
			}
		}

		okUnicode, err := Match(paramQuery(TextMatch{Text: "RENÉ", Collation: "i;unicode-casemap"}), &co)
		if err != nil {
			t.Fatalf("Match(param unicode-casemap) error = %v", err)
		}
		if !okUnicode {
			t.Errorf("Match(param unicode-casemap 'RENÉ') = false, want true")
		}

		okOctet, err := Match(paramQuery(TextMatch{Text: "rené", Collation: "i;octet"}), &co)
		if err != nil {
			t.Fatalf("Match(param i;octet) error = %v", err)
		}
		if okOctet {
			t.Errorf("Match(param i;octet 'rené') = true, want false")
		}

		if _, err := Match(paramQuery(TextMatch{Text: "René", Collation: "i;unknown"}), &co); err == nil {
			t.Errorf("Match(param unsupported collation) = nil error, want error")
		}
	})

	t.Run("client and server round-trip collation and reject unsupported collation with supported-collation", func(t *testing.T) {
		rec := &collationQueryBackend{}
		srv := httptest.NewServer(&Handler{Backend: rec})
		defer srv.Close()

		c, err := NewClient(http.DefaultClient, srv.URL)
		if err != nil {
			t.Fatal(err)
		}

		_, err = c.QueryCalendar(context.Background(), "/user/calendars/cal/", &CalendarQuery{
			CompFilter: queryWith(TextMatch{Text: "Meeting", Collation: "i;octet"}),
		})
		if err != nil {
			t.Fatalf("QueryCalendar(i;octet) = %v", err)
		}
		if rec.got == nil {
			t.Fatal("backend did not receive query")
		}
		gotTM := rec.got.CompFilter.Comps[0].Props[0].TextMatch
		if gotTM == nil || gotTM.Collation != "i;octet" {
			t.Errorf("decoded TextMatch = %+v, want Collation=\"i;octet\"", gotTM)
		}

		// Unsupported collation must fail with CALDAV:supported-collation precondition
		_, err = c.QueryCalendar(context.Background(), "/user/calendars/cal/", &CalendarQuery{
			CompFilter: queryWith(TextMatch{Text: "Meeting", Collation: "i;unsupported"}),
		})
		var httpErr *webdav.HTTPError
		if !errors.As(err, &httpErr) || (httpErr.Code != http.StatusForbidden && httpErr.Code != http.StatusConflict) {
			t.Errorf("QueryCalendar(unsupported) = %v, want 403/409 HTTPError", err)
		}

		// Also inspect raw XML response body for <supported-collation/>
		rawReq := `<?xml version="1.0" encoding="utf-8"?>
<C:calendar-query xmlns:D="DAV:" xmlns:C="urn:ietf:params:xml:ns:caldav">
  <D:prop><D:getetag/></D:prop>
  <C:filter>
    <C:comp-filter name="VCALENDAR">
      <C:comp-filter name="VEVENT">
        <C:prop-filter name="SUMMARY">
          <C:text-match collation="i;unsupported">Meeting</C:text-match>
        </C:prop-filter>
      </C:comp-filter>
    </C:comp-filter>
  </C:filter>
</C:calendar-query>`
		httpReq, err := http.NewRequest("REPORT", srv.URL+"/user/calendars/cal/", strings.NewReader(rawReq))
		if err != nil {
			t.Fatal(err)
		}
		httpReq.Header.Set("Content-Type", "application/xml")
		resp, err := http.DefaultClient.Do(httpReq)
		if err != nil {
			t.Fatal(err)
		}
		body, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		if resp.StatusCode != http.StatusForbidden && resp.StatusCode != http.StatusConflict {
			t.Errorf("raw REPORT status = %d, want 403 or 409", resp.StatusCode)
		}
		if !strings.Contains(string(body), string(PreconditionSupportedCollation)) {
			t.Errorf("raw REPORT body = %s, want %q precondition element", body, PreconditionSupportedCollation)
		}
	})
}
