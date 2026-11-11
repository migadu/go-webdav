package caldav

import (
	"encoding/xml"
	"strings"
	"testing"
	"time"
)

// The negate-condition attribute of a text-match must reach the backend, or
// it answers a negated query with the opposite set of objects.
func TestDecodeFilterNegateCondition(t *testing.T) {
	el := compFilter{
		Name: "VCALENDAR",
		CompFilters: []compFilter{{
			Name: "VEVENT",
			PropFilters: []propFilter{{
				Name:      "SUMMARY",
				TextMatch: &textMatch{Text: "meeting", NegateCondition: true},
				ParamFilter: []paramFilter{{
					Name:      "LANGUAGE",
					TextMatch: &textMatch{Text: "en", NegateCondition: true},
				}},
			}},
		}},
	}

	cf, err := decodeCompFilter(&el)
	if err != nil {
		t.Fatalf("decodeCompFilter() = %v", err)
	}
	pf := cf.Comps[0].Props[0]
	if pf.TextMatch == nil || !pf.TextMatch.NegateCondition {
		t.Errorf("prop-filter text-match lost negate-condition: %+v", pf.TextMatch)
	}
	if tm := pf.ParamFilter[0].TextMatch; tm == nil || !tm.NegateCondition {
		t.Errorf("param-filter text-match lost negate-condition: %+v", tm)
	}
}

// RFC 4791 section 9.9: a time range open on one side omits that attribute.
func TestEncodeOpenTimeRange(t *testing.T) {
	start := time.Date(2006, 1, 4, 0, 0, 0, 0, time.UTC)
	for _, tc := range []struct {
		name       string
		start, end time.Time
		want, not  string
	}{
		{"start only", start, time.Time{}, `start="20060104T000000Z"`, `end=`},
		{"end only", time.Time{}, start, `end="20060104T000000Z"`, `start=`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cf := encodeCompFilter(&CompFilter{
				Name: "VCALENDAR",
				Comps: []CompFilter{{
					Name:  "VEVENT",
					Start: tc.start,
					End:   tc.end,
					Props: []PropFilter{{Name: "DTSTAMP", Start: tc.start, End: tc.end}},
				}},
			})
			b, err := xml.Marshal(cf)
			if err != nil {
				t.Fatal(err)
			}
			s := string(b)
			if strings.Count(s, tc.want) != 2 {
				t.Errorf("want %s in both time ranges: %s", tc.want, s)
			}
			if strings.Contains(s, tc.not) {
				t.Errorf("unexpected %s: %s", tc.not, s)
			}

			// And the server reads it back as open on that side
			var decoded compFilter
			if err := xml.Unmarshal(b, &decoded); err != nil {
				t.Fatal(err)
			}
			got, err := decodeCompFilter(&decoded)
			if err != nil {
				t.Fatal(err)
			}
			if c := got.Comps[0]; !c.Start.Equal(tc.start) || !c.End.Equal(tc.end) {
				t.Errorf("decoded range [%v, %v), want [%v, %v)", c.Start, c.End, tc.start, tc.end)
			}
		})
	}
}
