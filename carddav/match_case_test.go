package carddav

import (
	"bytes"
	"strings"
	"testing"

	"github.com/emersion/go-vcard"
)

// mixedCaseCard returns a card built in Go, with keys differing only by case:
// vcard.Decoder uppercases property and parameter names, but nothing keeps a
// backend from building such a map.
func mixedCaseCard() vcard.Card {
	return vcard.Card{
		vcard.FieldVersion: {{Value: "4.0"}},
		"FN":               {{Value: "Mixed Case"}},
		"TEL":              {{Value: "+10000000001"}},
		"tel":              {{Value: "+10000000002", Group: "item1"}},
		"Tel": {{
			Value:  "+10000000003",
			Params: vcard.Params{"TYPE": {"home"}, "type": {"cell"}},
		}},
	}
}

// Property and parameter names are case-insensitive (RFC 6350 section 3.3):
// every key matching a name counts, not only the first one found.
func TestMatchMixedCaseKeys(t *testing.T) {
	ao := AddressObject{Card: mixedCaseCard()}

	for _, tc := range []struct {
		name string
		pf   PropFilter
	}{
		{"uppercase name, lowercase key", PropFilter{
			Name:        "TEL",
			TextMatches: []TextMatch{{Text: "+10000000002"}},
		}},
		{"lowercase name, uppercase key", PropFilter{
			Name:        "tel",
			TextMatches: []TextMatch{{Text: "+10000000001"}},
		}},
		{"grouped name, lowercase key", PropFilter{
			Name:        "item1.TEL",
			TextMatches: []TextMatch{{Text: "+10000000002"}},
		}},
		{"uppercase parameter name, lowercase key", PropFilter{
			Name:   "TEL",
			Params: []ParamFilter{{Name: "TYPE", TextMatch: &TextMatch{Text: "cell"}}},
		}},
		{"lowercase parameter name, uppercase key", PropFilter{
			Name:   "TEL",
			Params: []ParamFilter{{Name: "type", TextMatch: &TextMatch{Text: "home"}}},
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ok, err := Match(&AddressBookQuery{PropFilters: []PropFilter{tc.pf}}, &ao)
			if err != nil {
				t.Fatalf("Match() error = %v", err)
			}
			if !ok {
				t.Errorf("Match(%+v) = false, want true", tc.pf)
			}
		})
	}
}

// The fields of keys differing only by case are all returned, and always in
// the same order, whatever the order the map is iterated in.
func TestFilterPropertiesMixedCaseKeys(t *testing.T) {
	ao := AddressObject{Card: mixedCaseCard()}
	want := "BEGIN:VCARD\r\n" +
		"VERSION:4.0\r\n" +
		"TEL:+10000000001\r\n" +
		"TEL;TYPE=home;type=cell:+10000000003\r\n" +
		"item1.TEL:+10000000002\r\n" +
		"END:VCARD\r\n"

	for _, name := range []string{"TEL", "tel"} {
		for i := 0; i < 20; i++ {
			got := filterProperties(AddressDataRequest{Props: []string{name}}, ao)
			var buf bytes.Buffer
			if err := vcard.NewEncoder(&buf).Encode(got.Card); err != nil {
				t.Fatalf("Encode() = %v", err)
			}
			if buf.String() != want {
				t.Fatalf("filterProperties(%q) =\n%v\nwant:\n%v", name, buf.String(), want)
			}
		}
	}
}

// filterProperties always keeps VERSION, without which the vCard can't be
// encoded, under whatever case its key has.
func TestFilterPropertiesVersion(t *testing.T) {
	t.Run("lowercase key", func(t *testing.T) {
		card := mixedCaseCard()
		card["version"] = card[vcard.FieldVersion]
		delete(card, vcard.FieldVersion)

		got := filterProperties(AddressDataRequest{Props: []string{"FN"}}, AddressObject{Card: card})
		var buf bytes.Buffer
		if err := vcard.NewEncoder(&buf).Encode(got.Card); err != nil {
			t.Fatalf("Encode() = %v", err)
		}
		if want := "BEGIN:VCARD\r\nVERSION:4.0\r\nFN:Mixed Case\r\nEND:VCARD\r\n"; buf.String() != want {
			t.Errorf("filterProperties() =\n%v\nwant:\n%v", buf.String(), want)
		}
	})

	t.Run("not aliased", func(t *testing.T) {
		card, err := vcard.NewDecoder(strings.NewReader(groupedCard)).Decode()
		if err != nil {
			t.Fatal(err)
		}
		// Room for another field, which appending to an alias would write
		// into card's array
		card[vcard.FieldVersion] = append(make([]*vcard.Field, 0, 2), card[vcard.FieldVersion]...)

		got := filterProperties(AddressDataRequest{Props: []string{"FN"}}, AddressObject{Card: card})
		got.Card[vcard.FieldVersion] = append(got.Card[vcard.FieldVersion], &vcard.Field{Value: "3.0"})
		if extra := card[vcard.FieldVersion][:2][1]; extra != nil {
			t.Errorf("appending to the filtered VERSION changed the original card: %+v", extra)
		}
	})
}
