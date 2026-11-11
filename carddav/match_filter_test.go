package carddav

import (
	"strings"
	"testing"
	"time"

	"github.com/emersion/go-vcard"
)

func TestMatchAndFilterEdgeCases(t *testing.T) {
	mustDecode := func(s string) AddressObject {
		t.Helper()
		card, err := vcard.NewDecoder(strings.NewReader(s)).Decode()
		if err != nil {
			t.Fatal(err)
		}
		return AddressObject{
			Path:          "/ab/multi.vcf",
			ModTime:       time.Date(2026, 3, 15, 10, 0, 0, 0, time.UTC),
			ContentLength: int64(len(s)),
			ETag:          "etag-1",
			Card:          card,
			ReadOnly:      true,
		}
	}

	multi := mustDecode("BEGIN:VCARD\r\n" +
		"VERSION:4.0\r\n" +
		"UID:uid-multi\r\n" +
		"FN:Multi User\r\n" +
		"EMAIL;TYPE=HOME:multi@home.example\r\n" +
		"EMAIL;TYPE=INTERNET,WORK:multi@work.example\r\n" +
		"TEL;TYPE=CELL:+10000000001\r\n" +
		"TEL:+10000000002\r\n" +
		"END:VCARD\r\n")

	t.Run("empty PropFilters matches all with default FilterTest", func(t *testing.T) {
		ok, err := Match(&AddressBookQuery{}, &multi)
		if err != nil {
			t.Fatalf("Match() error = %v", err)
		}
		if !ok {
			t.Errorf("Match(empty filter) = false, want true")
		}
	})

	t.Run("empty PropFilters matches all with explicit FilterAnyOf", func(t *testing.T) {
		ok, err := Match(&AddressBookQuery{FilterTest: FilterAnyOf}, &multi)
		if err != nil {
			t.Fatalf("Match() error = %v", err)
		}
		if !ok {
			t.Errorf("Match(FilterAnyOf empty filter) = false, want true")
		}
	})

	t.Run("multi-valued EMAIL matches second value and case-insensitive name", func(t *testing.T) {
		ok, err := Match(&AddressBookQuery{
			PropFilters: []PropFilter{{
				Name:        "email",
				TextMatches: []TextMatch{{Text: "work.example"}},
			}},
		}, &multi)
		if err != nil {
			t.Fatalf("Match() error = %v", err)
		}
		if !ok {
			t.Errorf("Match(second EMAIL) = false, want true")
		}
	})

	t.Run("ParamFilter TextMatch matches multi-valued TYPE parameter on second EMAIL", func(t *testing.T) {
		ok, err := Match(&AddressBookQuery{
			PropFilters: []PropFilter{{
				Name: "EMAIL",
				Params: []ParamFilter{{
					Name:      "type",
					TextMatch: &TextMatch{Text: "WORK", MatchType: MatchEquals},
				}},
			}},
		}, &multi)
		if err != nil {
			t.Fatalf("Match() error = %v", err)
		}
		if !ok {
			t.Errorf("Match(EMAIL TYPE=WORK) = false, want true")
		}
	})

	t.Run("ParamFilter TextMatch fails when parameter value absent", func(t *testing.T) {
		ok, err := Match(&AddressBookQuery{
			PropFilters: []PropFilter{{
				Name: "EMAIL",
				Params: []ParamFilter{{
					Name:      "TYPE",
					TextMatch: &TextMatch{Text: "FAX", MatchType: MatchEquals},
				}},
			}},
		}, &multi)
		if err != nil {
			t.Fatalf("Match() error = %v", err)
		}
		if ok {
			t.Errorf("Match(EMAIL TYPE=FAX) = true, want false")
		}
	})

	t.Run("PropFilter FilterAllOf requires ParamFilter and TextMatch on same field instance", func(t *testing.T) {
		okSame, err := Match(&AddressBookQuery{
			PropFilters: []PropFilter{{
				Name:        "EMAIL",
				Test:        FilterAllOf,
				TextMatches: []TextMatch{{Text: "work.example"}},
				Params: []ParamFilter{{
					Name:      "TYPE",
					TextMatch: &TextMatch{Text: "WORK", MatchType: MatchEquals},
				}},
			}},
		}, &multi)
		if err != nil {
			t.Fatalf("Match() error = %v", err)
		}
		if !okSame {
			t.Errorf("Match(same EMAIL instance) = false, want true")
		}

		okCross, err := Match(&AddressBookQuery{
			PropFilters: []PropFilter{{
				Name:        "EMAIL",
				Test:        FilterAllOf,
				TextMatches: []TextMatch{{Text: "home.example"}},
				Params: []ParamFilter{{
					Name:      "TYPE",
					TextMatch: &TextMatch{Text: "WORK", MatchType: MatchEquals},
				}},
			}},
		}, &multi)
		if err != nil {
			t.Fatalf("Match() error = %v", err)
		}
		if okCross {
			t.Errorf("Match(cross EMAIL instances) = true, want false")
		}
	})

	t.Run("ParamFilter IsNotDefined matches second TEL without TYPE", func(t *testing.T) {
		ok, err := Match(&AddressBookQuery{
			PropFilters: []PropFilter{{
				Name:   "TEL",
				Params: []ParamFilter{{Name: "TYPE", IsNotDefined: true}},
			}},
		}, &multi)
		if err != nil {
			t.Fatalf("Match() error = %v", err)
		}
		if !ok {
			t.Errorf("Match(TEL without TYPE) = false, want true")
		}

		okEmail, err := Match(&AddressBookQuery{
			PropFilters: []PropFilter{{
				Name:   "EMAIL",
				Params: []ParamFilter{{Name: "TYPE", IsNotDefined: true}},
			}},
		}, &multi)
		if err != nil {
			t.Fatalf("Match() error = %v", err)
		}
		if okEmail {
			t.Errorf("Match(EMAIL without TYPE) = true, want false")
		}
	})

	t.Run("Filter does not panic on empty vCard and preserves metadata when filtering props", func(t *testing.T) {
		defer func() {
			if r := recover(); r != nil {
				t.Fatalf("Filter() panicked on empty vCard: %v", r)
			}
		}()
		emptyAO := AddressObject{Path: "/ab/empty.vcf", ETag: "etag-empty", Card: vcard.Card{}}
		got, err := Filter(&AddressBookQuery{
			FilterTest: FilterAllOf,
			DataRequest: AddressDataRequest{
				Props: []string{vcard.FieldFormattedName},
			},
		}, []AddressObject{emptyAO, multi})
		if err != nil {
			t.Fatalf("Filter() error = %v", err)
		}
		if len(got) != 2 {
			t.Fatalf("len(Filter()) = %d, want 2", len(got))
		}
		if got[1].ContentLength != multi.ContentLength || !got[1].ReadOnly {
			t.Errorf("filtered object metadata lost: ContentLength=%d ReadOnly=%v, want %d true",
				got[1].ContentLength, got[1].ReadOnly, multi.ContentLength)
		}
	})
}
