package caldav

import "testing"

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
