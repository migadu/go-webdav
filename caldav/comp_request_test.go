package caldav

import (
	"encoding/xml"
	"strings"
	"testing"
)

func encodeCompRequestString(t *testing.T, req *CalendarCompRequest) (string, error) {
	t.Helper()
	prop, err := encodeCalendarReq(req)
	if err != nil {
		return "", err
	}
	var buf strings.Builder
	for _, raw := range prop.Raw {
		b, err := xml.Marshal(&raw)
		if err != nil {
			t.Fatal(err)
		}
		buf.Write(b)
	}
	return buf.String(), nil
}

// An empty child comp request has nothing to ask for: leave it out.
func TestEncodeCalendarCompRequestEmptyChild(t *testing.T) {
	s, err := encodeCompRequestString(t, &CalendarCompRequest{
		Name:  "VCALENDAR",
		Comps: []CalendarCompRequest{{}, {Name: "VEVENT", AllProps: true}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Count(s, "<comp ") != 2 {
		t.Errorf("want the VCALENDAR and VEVENT comp elements only: %s", s)
	}
}

// RFC 4791 section 9.6.1: the name attribute of comp is required, and the
// outermost comp of calendar-data is VCALENDAR.
func TestEncodeCalendarCompRequestName(t *testing.T) {
	s, err := encodeCompRequestString(t, &CalendarCompRequest{AllProps: true, AllComps: true})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(s, `<comp xmlns="urn:ietf:params:xml:ns:caldav" name="VCALENDAR">`) {
		t.Errorf("want a VCALENDAR comp element: %s", s)
	}

	if _, err := encodeCompRequestString(t, &CalendarCompRequest{
		Name:  "VCALENDAR",
		Comps: []CalendarCompRequest{{Props: []string{"SUMMARY"}}},
	}); err == nil {
		t.Errorf("a child comp request without a name was accepted")
	}
}

// An empty request asks for the whole calendar data: send no comp element,
// which would restrict it to the named component alone.
func TestEncodeCalendarCompRequestEmpty(t *testing.T) {
	s, err := encodeCompRequestString(t, &CalendarCompRequest{})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(s, "<comp") {
		t.Errorf("want no comp element: %s", s)
	}
}
