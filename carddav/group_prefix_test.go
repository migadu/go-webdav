package carddav

import (
	"bytes"
	"strings"
	"testing"

	"github.com/emersion/go-vcard"
)

const groupedCard = "BEGIN:VCARD\r\n" +
	"VERSION:4.0\r\n" +
	"UID:uid-group\r\n" +
	"FN:Grouped\r\n" +
	"item1.TEL:+10000000001\r\n" +
	"item2.TEL:+10000000002\r\n" +
	"TEL:+10000000003\r\n" +
	"END:VCARD\r\n"

func decodeGroupedCard(t *testing.T) AddressObject {
	t.Helper()
	card, err := vcard.NewDecoder(strings.NewReader(groupedCard)).Decode()
	if err != nil {
		t.Fatal(err)
	}
	return AddressObject{Card: card}
}

// RFC 6352 section 10.5.1: "When the "name" attribute includes a group
// prefix, it MUST match properties that have exactly the same group prefix
// and name."
func TestMatchPropFilterGroupPrefix(t *testing.T) {
	ao := decodeGroupedCard(t)

	for _, tc := range []struct {
		name string
		pf   PropFilter
		want bool
	}{
		{"grouped name is defined", PropFilter{Name: "item1.TEL"}, true},
		{"grouped name, is-not-defined", PropFilter{Name: "item1.TEL", IsNotDefined: true}, false},
		{"group names are case-insensitive", PropFilter{Name: "ITEM1.tel"}, true},
		{"unknown group", PropFilter{Name: "item3.TEL"}, false},
		{"unknown group, is-not-defined", PropFilter{Name: "item3.TEL", IsNotDefined: true}, true},
		{"grouped name matches its own value", PropFilter{
			Name:        "item1.TEL",
			TextMatches: []TextMatch{{Text: "+10000000001"}},
		}, true},
		{"grouped name doesn't match another group", PropFilter{
			Name:        "item1.TEL",
			TextMatches: []TextMatch{{Text: "+10000000002"}},
		}, false},
		{"grouped name doesn't match an ungrouped property", PropFilter{
			Name:        "item1.TEL",
			TextMatches: []TextMatch{{Text: "+10000000003"}},
		}, false},
		{"plain name matches a grouped property", PropFilter{
			Name:        "TEL",
			TextMatches: []TextMatch{{Text: "+10000000002"}},
		}, true},
		{"plain name matches an ungrouped property", PropFilter{
			Name:        "TEL",
			TextMatches: []TextMatch{{Text: "+10000000003"}},
		}, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ok, err := Match(&AddressBookQuery{PropFilters: []PropFilter{tc.pf}}, &ao)
			if err != nil {
				t.Fatalf("Match() error = %v", err)
			}
			if ok != tc.want {
				t.Errorf("Match(%+v) = %v, want %v", tc.pf, ok, tc.want)
			}
		})
	}
}

// RFC 6352 section 10.4.2 gives CARDDAV:prop in address-data the same group
// prefix rules as prop-filter.
func TestFilterPropertiesGroupPrefix(t *testing.T) {
	ao := decodeGroupedCard(t)

	for _, tc := range []struct {
		name  string
		props []string
		want  []string // TEL lines in the filtered vCard
	}{
		{"grouped name", []string{"item1.TEL"}, []string{"item1.TEL:+10000000001"}},
		{"lowercase grouped name", []string{"item2.tel"}, []string{"item2.TEL:+10000000002"}},
		{"plain name", []string{"TEL"}, []string{
			"item1.TEL:+10000000001",
			"item2.TEL:+10000000002",
			"TEL:+10000000003",
		}},
		{"plain and grouped name", []string{"item1.TEL", "TEL"}, []string{
			"item1.TEL:+10000000001",
			"item2.TEL:+10000000002",
			"TEL:+10000000003",
		}},
		{"two groups", []string{"item1.TEL", "item2.TEL"}, []string{
			"item1.TEL:+10000000001",
			"item2.TEL:+10000000002",
		}},
		{"unknown group", []string{"item3.TEL"}, nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := filterProperties(AddressDataRequest{Props: tc.props}, ao)

			var buf bytes.Buffer
			if err := vcard.NewEncoder(&buf).Encode(got.Card); err != nil {
				t.Fatalf("Encode() = %v", err)
			}
			var tels []string
			for _, line := range strings.Split(buf.String(), "\r\n") {
				if strings.Contains(line, "TEL:") {
					tels = append(tels, line)
				}
			}
			if strings.Join(tels, "\n") != strings.Join(tc.want, "\n") {
				t.Errorf("filterProperties(%q) TEL lines = %q, want %q", tc.props, tels, tc.want)
			}
		})
	}
}
