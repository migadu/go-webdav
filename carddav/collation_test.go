package carddav

import (
	"context"
	"encoding/xml"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"

	"github.com/emersion/go-vcard"
	"github.com/migadu/go-webdav"
)

type collationCardBackend struct {
	testBackend
	got *AddressBookQuery
}

func (b *collationCardBackend) QueryAddressObjects(ctx context.Context, path string, query *AddressBookQuery) ([]AddressObject, error) {
	q := *query
	b.got = &q
	return nil, nil
}

func TestTextMatchCollation(t *testing.T) {
	card, err := vcard.NewDecoder(strings.NewReader("BEGIN:VCARD\r\n" +
		"VERSION:4.0\r\n" +
		"UID:uid-collation\r\n" +
		"FN:René Müller\r\n" +
		"EMAIL;TYPE=Work,Home:Rene.Mueller@Example.COM\r\n" +
		"END:VCARD\r\n")).Decode()
	if err != nil {
		t.Fatal(err)
	}
	ao := AddressObject{Card: card}

	queryWith := func(prop string, tm TextMatch) *AddressBookQuery {
		return &AddressBookQuery{
			PropFilters: []PropFilter{{
				Name:        prop,
				TextMatches: []TextMatch{tm},
			}},
		}
	}

	t.Run("default collation is i;unicode-casemap", func(t *testing.T) {
		for _, tc := range []struct {
			name string
			tm   TextMatch
			want bool
		}{
			{"contains Unicode folded", TextMatch{Text: "RENÉ MÜLLER"}, true},
			{"equals Unicode folded", TextMatch{Text: "rené müller", MatchType: MatchEquals}, true},
			{"starts-with Unicode folded", TextMatch{Text: "RENÉ", MatchType: MatchStartsWith}, true},
			{"ends-with Unicode folded", TextMatch{Text: "MÜLLER", MatchType: MatchEndsWith}, true},
		} {
			t.Run(tc.name, func(t *testing.T) {
				ok, err := Match(queryWith("FN", tc.tm), &ao)
				if err != nil {
					t.Fatalf("Match() error = %v", err)
				}
				if ok != tc.want {
					t.Errorf("Match(%+v) = %v, want %v", tc.tm, ok, tc.want)
				}
			})
		}
	})

	t.Run("i;ascii-casemap folds ASCII only", func(t *testing.T) {
		okASCII, err := Match(queryWith("EMAIL", TextMatch{
			Text:      "rene.mueller@example.com",
			Collation: "i;ascii-casemap",
			MatchType: MatchEquals,
		}), &ao)
		if err != nil {
			t.Fatalf("Match() error = %v", err)
		}
		if !okASCII {
			t.Errorf("Match(i;ascii-casemap ASCII) = false, want true")
		}

		okNonASCII, err := Match(queryWith("FN", TextMatch{
			Text:      "RENÉ",
			Collation: "i;ascii-casemap",
			MatchType: MatchStartsWith,
		}), &ao)
		if err != nil {
			t.Fatalf("Match() error = %v", err)
		}
		if okNonASCII {
			t.Errorf("Match(i;ascii-casemap 'RENÉ' vs 'René') = true, want false")
		}
	})

	t.Run("i;octet is case-sensitive", func(t *testing.T) {
		okLower, err := Match(queryWith("EMAIL", TextMatch{
			Text:      "rene.mueller@example.com",
			Collation: "i;octet",
			MatchType: MatchEquals,
		}), &ao)
		if err != nil {
			t.Fatalf("Match() error = %v", err)
		}
		if okLower {
			t.Errorf("Match(i;octet lowercase) = true, want false")
		}

		okExact, err := Match(queryWith("EMAIL", TextMatch{
			Text:      "Rene.Mueller@Example.COM",
			Collation: "i;octet",
			MatchType: MatchEquals,
		}), &ao)
		if err != nil {
			t.Fatalf("Match() error = %v", err)
		}
		if !okExact {
			t.Errorf("Match(i;octet exact) = false, want true")
		}
	})

	t.Run("Match rejects unsupported collation", func(t *testing.T) {
		_, err := Match(queryWith("FN", TextMatch{Text: "René", Collation: "i;unknown"}), &ao)
		if err == nil {
			t.Errorf("Match(unsupported collation) = nil error, want error")
		}
	})

	t.Run("ParamFilter.TextMatch applies collation and rejects unsupported collation", func(t *testing.T) {
		paramQuery := func(tm TextMatch) *AddressBookQuery {
			return &AddressBookQuery{
				PropFilters: []PropFilter{{
					Name:   "EMAIL",
					Params: []ParamFilter{{Name: "TYPE", TextMatch: &tm}},
				}},
			}
		}

		okDefault, err := Match(paramQuery(TextMatch{Text: "HOME", MatchType: MatchEquals}), &ao)
		if err != nil {
			t.Fatalf("Match(param default casemap) error = %v", err)
		}
		if !okDefault {
			t.Errorf("Match(param default casemap 'HOME' vs 'Home') = false, want true")
		}

		okOctet, err := Match(paramQuery(TextMatch{Text: "HOME", Collation: "i;octet", MatchType: MatchEquals}), &ao)
		if err != nil {
			t.Fatalf("Match(param i;octet) error = %v", err)
		}
		if okOctet {
			t.Errorf("Match(param i;octet 'HOME' vs 'Home') = true, want false")
		}

		if _, err := Match(paramQuery(TextMatch{Text: "Home", Collation: "i;unknown"}), &ao); err == nil {
			t.Errorf("Match(param unsupported collation) = nil error, want error")
		}
	})

	t.Run("client and server round-trip collation and reject unsupported collation with supported-collation", func(t *testing.T) {
		rec := &collationCardBackend{}
		srv := httptest.NewServer(&Handler{Backend: rec})
		defer srv.Close()

		c, err := NewClient(http.DefaultClient, srv.URL)
		if err != nil {
			t.Fatal(err)
		}

		_, err = c.QueryAddressBook(context.Background(), "/user/addressbooks/ab/", queryWith("FN", TextMatch{
			Text:      "René",
			Collation: "i;octet",
		}))
		if err != nil {
			t.Fatalf("QueryAddressBook(i;octet) = %v", err)
		}
		if rec.got == nil || len(rec.got.PropFilters) == 0 || len(rec.got.PropFilters[0].TextMatches) == 0 {
			t.Fatal("backend did not receive TextMatch")
		}
		gotTM := rec.got.PropFilters[0].TextMatches[0]
		if gotTM.Collation != "i;octet" {
			t.Errorf("decoded TextMatch.Collation = %q, want %q", gotTM.Collation, "i;octet")
		}

		_, err = c.QueryAddressBook(context.Background(), "/user/addressbooks/ab/", queryWith("FN", TextMatch{
			Text:      "René",
			Collation: "i;unsupported",
		}))
		var httpErr *webdav.HTTPError
		if !errors.As(err, &httpErr) || (httpErr.Code != http.StatusForbidden && httpErr.Code != http.StatusConflict) {
			t.Errorf("QueryAddressBook(unsupported) = %v, want 403/409 HTTPError", err)
		}

		rawReq := `<?xml version="1.0" encoding="utf-8"?>
<C:addressbook-query xmlns:D="DAV:" xmlns:C="urn:ietf:params:xml:ns:carddav">
  <D:prop><D:getetag/></D:prop>
  <C:filter>
    <C:prop-filter name="FN">
      <C:text-match collation="i;unsupported">René</C:text-match>
    </C:prop-filter>
  </C:filter>
</C:addressbook-query>`
		httpReq, err := http.NewRequest("REPORT", srv.URL+"/user/addressbooks/ab/", strings.NewReader(rawReq))
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

// RFC 6352 section 8.3: "if the client specifies the "default" collation
// identifier (as defined in [RFC4790], Section 3.1), the server MUST default
// to using "i;unicode-casemap" as the collation."
func TestTextMatchDefaultCollation(t *testing.T) {
	card, err := vcard.NewDecoder(strings.NewReader("BEGIN:VCARD\r\n" +
		"VERSION:4.0\r\n" +
		"UID:uid-default\r\n" +
		"FN:René Müller\r\n" +
		"END:VCARD\r\n")).Decode()
	if err != nil {
		t.Fatal(err)
	}
	ao := AddressObject{Card: card}

	queryWith := func(text string) *AddressBookQuery {
		return &AddressBookQuery{PropFilters: []PropFilter{{
			Name:        "FN",
			TextMatches: []TextMatch{{Text: text, Collation: "default", MatchType: MatchStartsWith}},
		}}}
	}

	t.Run("Match", func(t *testing.T) {
		// i;unicode-casemap folds non-ASCII letters too
		ok, err := Match(queryWith("RENÉ"), &ao)
		if err != nil {
			t.Fatalf("Match(collation=default) error = %v", err)
		}
		if !ok {
			t.Errorf("Match(collation=default, 'RENÉ' vs 'René') = false, want true")
		}
	})

	t.Run("REPORT", func(t *testing.T) {
		rec := &collationCardBackend{}
		srv := httptest.NewServer(&Handler{Backend: rec})
		defer srv.Close()

		c, err := NewClient(http.DefaultClient, srv.URL)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := c.QueryAddressBook(context.Background(), "/user/addressbooks/ab/", queryWith("René")); err != nil {
			t.Fatalf("QueryAddressBook(collation=default) = %v", err)
		}
		// Backends get the default collation as an empty Collation
		if rec.got == nil || len(rec.got.PropFilters) == 0 || len(rec.got.PropFilters[0].TextMatches) == 0 {
			t.Fatal("backend did not receive TextMatch")
		}
		if gotTM := rec.got.PropFilters[0].TextMatches[0]; gotTM.Collation != "" {
			t.Errorf("decoded TextMatch.Collation = %q, want \"\"", gotTM.Collation)
		}
	})
}

// RFC 5051 section 2 decomposes every codepoint, so a precomposed and a
// decomposed "é" are equal under i;unicode-casemap, the default collation of
// CardDAV (RFC 6352 section 8.3).
func TestTextMatchUnicodeCasemapDecomposition(t *testing.T) {
	card, err := vcard.NewDecoder(strings.NewReader("BEGIN:VCARD\r\n" +
		"VERSION:4.0\r\n" +
		"UID:uid-nfkd\r\n" +
		"FN:José\r\n" +
		"END:VCARD\r\n")).Decode()
	if err != nil {
		t.Fatal(err)
	}
	ao := AddressObject{Card: card}

	for _, text := range []string{"josé", "ＪＯＳÉ"} {
		ok, err := Match(&AddressBookQuery{PropFilters: []PropFilter{{
			Name:        "FN",
			TextMatches: []TextMatch{{Text: text, MatchType: MatchEquals}},
		}}}, &ao)
		if err != nil {
			t.Fatalf("Match() error = %v", err)
		}
		if !ok {
			t.Errorf("Match(FN equals %+q) against %+q = false, want true", text, "José")
		}
	}
}

const propFindSupportedCollationSet = `<?xml version="1.0" encoding="utf-8"?>
<D:propfind xmlns:D="DAV:" xmlns:C="urn:ietf:params:xml:ns:carddav">
  <D:prop><C:supported-collation-set/></D:prop>
</D:propfind>`

// testSupportedCollationSet decodes a CARDDAV:supported-collation-set,
// independently of the server's own types.
type testSupportedCollationSet struct {
	XMLName    xml.Name `xml:"urn:ietf:params:xml:ns:carddav supported-collation-set"`
	Collations []string `xml:"urn:ietf:params:xml:ns:carddav supported-collation"`
}

// RFC 6352 section 8.3: "Servers MUST advertise the set of collations that
// they support via the CARDDAV:supported-collation-set property defined on
// any resource that supports reports that use collations." Section 8.3.1:
// it's protected, and SHOULD NOT be returned by allprop.
func TestSupportedCollationSet(t *testing.T) {
	name := xml.Name{Space: namespace, Local: "supported-collation-set"}

	_, resp := serveACLPropFind(t, &testBackend{}, aclAddressBookPath, propFindSupportedCollationSet)
	var set testSupportedCollationSet
	if err := resp.DecodeProp(&set); err != nil {
		t.Fatalf("supported-collation-set: %v", err)
	}
	want := []string{"i;ascii-casemap", "i;octet", "i;unicode-casemap"}
	if !reflect.DeepEqual(set.Collations, want) {
		t.Errorf("supported-collation-set = %q, want %q", set.Collations, want)
	}

	_, resp = serveACLPropFind(t, &testBackend{}, aclAddressBookPath, `<propfind xmlns="DAV:"><propname/></propfind>`)
	if code := propStatus(resp, name); code != http.StatusOK {
		t.Errorf("propname: supported-collation-set status = %v, want 200", code)
	}

	_, resp = serveACLPropFind(t, &testBackend{}, aclAddressBookPath, `<propfind xmlns="DAV:"><allprop/></propfind>`)
	if code := propStatus(resp, name); code != 0 {
		t.Errorf("allprop returned supported-collation-set with status %v", code)
	}

	codes := propPatchAddressBook(t, &Handler{Backend: &updateBackend{}}, `<?xml version="1.0" encoding="UTF-8"?>
<D:propertyupdate xmlns:D="DAV:" xmlns:C="urn:ietf:params:xml:ns:carddav">
  <D:set><D:prop>
    <C:supported-collation-set><C:supported-collation>i;octet</C:supported-collation></C:supported-collation-set>
  </D:prop></D:set>
</D:propertyupdate>`)
	if codes[name] != http.StatusForbidden {
		t.Errorf("PROPPATCH supported-collation-set status = %v, want 403", codes[name])
	}
}
