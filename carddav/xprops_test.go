package carddav

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync"
	"testing"

	"github.com/emersion/go-vcard"
	"github.com/emersion/go-webdav"
)

// xPropsVCF is a contact as Apple clients store it, with the properties and
// parameters of upstream issue 115: extended properties (RFC 6350 section
// 6.10) in mixed case, in a group, and with parameters, and extended
// parameters (RFC 6350 section 5) on standard properties, along with a
// lowercase name and a folded line.
const xPropsVCF = "BEGIN:VCARD\r\n" +
	"VERSION:3.0\r\n" +
	"PRODID:-//Apple Inc.//iPhone OS 16.1//EN\r\n" +
	"UID:8D6C2E58-3E8B-4B0D-9C8E-5A6F7B8C9D0E\r\n" +
	"N:Appleseed;John;;;\r\n" +
	"FN:John Appleseed\r\n" +
	"ORG:Apple Inc.;\r\n" +
	"X-ABShowAs:COMPANY\r\n" +
	"X-APPLE-SORT-ORDER:2\r\n" +
	"X-PHONETIC-FIRST-NAME:Jon\r\n" +
	"item1.EMAIL;type=INTERNET;type=pref:john@example.com\r\n" +
	"item1.X-ABLabel:_$!<Other>!$_\r\n" +
	"BDAY;X-APPLE-OMIT-YEAR=1604:1604-03-15\r\n" +
	"IMPP;X-SERVICE-TYPE=Skype;type=HOME;type=pref:skype:johnappleseed\r\n" +
	"X-SOCIALPROFILE;type=twitter;x-user=johnappleseed:http://twitter.com/joh\r\n" +
	" nappleseed\r\n" +
	"x-vendor-flags;x-vendor-param=a:pinned\r\n" +
	"END:VCARD\r\n"

func decodeXPropsVCF(t *testing.T) vcard.Card {
	t.Helper()
	card, err := vcard.NewDecoder(strings.NewReader(xPropsVCF)).Decode()
	if err != nil {
		t.Fatalf("decoding the test vCard: %v", err)
	}
	return card
}

// checkXProps checks that card is xPropsVCF, with its extended properties and
// parameters.
func checkXProps(t *testing.T, card vcard.Card) {
	t.Helper()
	if card == nil {
		t.Fatalf("no vCard")
	}

	for name, want := range map[string]string{
		"X-APPLE-SORT-ORDER":    "2",
		"X-ABSHOWAS":            "COMPANY",
		"X-PHONETIC-FIRST-NAME": "Jon",
		"X-SOCIALPROFILE":       "http://twitter.com/johnappleseed",
		"X-VENDOR-FLAGS":        "pinned",
	} {
		if got := card.Value(name); got != want {
			t.Errorf("%v = %q, want %q", name, got, want)
		}
	}
	if f := card.Get("X-ABLABEL"); f == nil || f.Group != "item1" || f.Value != "_$!<Other>!$_" {
		t.Errorf("X-ABLabel = %+v, want _$!<Other>!$_ in group item1", f)
	}
	if f := card.Get("X-SOCIALPROFILE"); f == nil || f.Params.Get("X-USER") != "johnappleseed" {
		t.Errorf("X-SOCIALPROFILE = %+v, want an X-USER parameter", f)
	}
	if f := card.Get(vcard.FieldBirthday); f == nil || f.Params.Get("X-APPLE-OMIT-YEAR") != "1604" {
		t.Errorf("BDAY = %+v, want an X-APPLE-OMIT-YEAR parameter", f)
	}
	if f := card.Get(vcard.FieldIMPP); f == nil || f.Params.Get("X-SERVICE-TYPE") != "Skype" {
		t.Errorf("IMPP = %+v, want an X-SERVICE-TYPE parameter", f)
	}

	// Nothing else was lost or added on the way
	if want := decodeXPropsVCF(t); !reflect.DeepEqual(card, want) {
		var gotBuf, wantBuf strings.Builder
		vcard.NewEncoder(&gotBuf).Encode(card)
		vcard.NewEncoder(&wantBuf).Encode(want)
		t.Errorf("vCard changed:\ngot:\n%v\nwant:\n%v", gotBuf.String(), wantBuf.String())
	}
}

const (
	xPropsPrincipal   = "/user/"
	xPropsHomeSet     = "/user/contacts/"
	xPropsAddressBook = "/user/contacts/default/"
	xPropsObject      = "/user/contacts/default/john.vcf"
)

// xPropsBackend stores address objects in memory, querying them with Filter
// as backends do.
type xPropsBackend struct {
	mu      sync.Mutex
	objects map[string]vcard.Card
}

func (b *xPropsBackend) CurrentUserPrincipal(ctx context.Context) (string, error) {
	return xPropsPrincipal, nil
}

func (b *xPropsBackend) AddressBookHomeSetPath(ctx context.Context) (string, error) {
	return xPropsHomeSet, nil
}

func (b *xPropsBackend) ListAddressBooks(ctx context.Context) ([]AddressBook, error) {
	return []AddressBook{{Path: xPropsAddressBook}}, nil
}

func (b *xPropsBackend) GetAddressBook(ctx context.Context, path string) (*AddressBook, error) {
	if path != xPropsAddressBook {
		return nil, webdav.NewHTTPError(http.StatusNotFound, nil)
	}
	return &AddressBook{Path: xPropsAddressBook}, nil
}

func (b *xPropsBackend) CreateAddressBook(ctx context.Context, addressBook *AddressBook) error {
	return webdav.NewHTTPError(http.StatusForbidden, nil)
}

func (b *xPropsBackend) DeleteAddressBook(ctx context.Context, path string) error {
	return webdav.NewHTTPError(http.StatusForbidden, nil)
}

func (b *xPropsBackend) object(path string) (*AddressObject, bool) {
	b.mu.Lock()
	defer b.mu.Unlock()
	card, ok := b.objects[path]
	if !ok {
		return nil, false
	}
	return &AddressObject{Path: path, ETag: "1", Card: card}, true
}

func (b *xPropsBackend) GetAddressObject(ctx context.Context, path string, req *AddressDataRequest) (*AddressObject, error) {
	ao, ok := b.object(path)
	if !ok {
		return nil, webdav.NewHTTPError(http.StatusNotFound, nil)
	}
	return ao, nil
}

func (b *xPropsBackend) ListAddressObjects(ctx context.Context, path string, req *AddressDataRequest) ([]AddressObject, error) {
	if path != xPropsAddressBook {
		return nil, webdav.NewHTTPError(http.StatusNotFound, nil)
	}
	ao, ok := b.object(xPropsObject)
	if !ok {
		return nil, nil
	}
	return []AddressObject{*ao}, nil
}

func (b *xPropsBackend) QueryAddressObjects(ctx context.Context, path string, query *AddressBookQuery) ([]AddressObject, error) {
	aos, err := b.ListAddressObjects(ctx, path, &query.DataRequest)
	if err != nil {
		return nil, err
	}
	return Filter(query, aos)
}

func (b *xPropsBackend) PutAddressObject(ctx context.Context, path string, card vcard.Card, opts *PutAddressObjectOptions) (*AddressObject, error) {
	if path != xPropsObject {
		return nil, webdav.NewHTTPError(http.StatusForbidden, nil)
	}
	b.mu.Lock()
	b.objects[path] = card
	b.mu.Unlock()
	ao, _ := b.object(path)
	return ao, nil
}

func (b *xPropsBackend) DeleteAddressObject(ctx context.Context, path string) error {
	return webdav.NewHTTPError(http.StatusForbidden, nil)
}

func newXPropsServer(t *testing.T) (*httptest.Server, *xPropsBackend) {
	b := &xPropsBackend{objects: make(map[string]vcard.Card)}
	ts := httptest.NewServer(&Handler{Backend: b})
	t.Cleanup(ts.Close)
	return ts, b
}

func doXPropsRequest(t *testing.T, ts *httptest.Server, method, path, contentType, body string, header ...string) (*http.Response, string) {
	t.Helper()
	req, err := http.NewRequest(method, ts.URL+path, strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	for i := 0; i+1 < len(header); i += 2 {
		req.Header.Set(header[i], header[i+1])
	}
	resp, err := ts.Client().Do(req)
	if err != nil {
		t.Fatalf("%v %v: %v", method, path, err)
	}
	defer resp.Body.Close()
	b, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	return resp, string(b)
}

// TestServerExtendedProperties sends a vCard with extended properties and
// parameters to the server as a client would, and checks they come back
// unchanged in GET responses and REPORT address data, and that address book
// queries can filter on them and ask for them by the name clients use.
func TestServerExtendedProperties(t *testing.T) {
	ts, b := newXPropsServer(t)

	resp, body := doXPropsRequest(t, ts, http.MethodPut, xPropsObject, vcard.MIMEType, xPropsVCF)
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("PUT: got status %v, want 201 Created: %v", resp.Status, body)
	}
	ao, ok := b.object(xPropsObject)
	if !ok {
		t.Fatalf("PUT: the backend didn't store the object")
	}
	checkXProps(t, ao.Card)

	resp, body = doXPropsRequest(t, ts, http.MethodGet, xPropsObject, "", "")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET: got status %v, want 200 OK: %v", resp.Status, body)
	}
	if !strings.Contains(body, "\r\nX-APPLE-SORT-ORDER:2\r\n") {
		t.Errorf("GET: X-APPLE-SORT-ORDER missing from the response:\n%v", body)
	}
	card, err := vcard.NewDecoder(strings.NewReader(body)).Decode()
	if err != nil {
		t.Fatalf("GET: decoding the response: %v", err)
	}
	checkXProps(t, card)

	for _, tc := range []struct {
		name, body string
		// The extended properties expected in the address data, or nil if
		// the address object isn't expected in the response
		want []string
		// The properties not expected in the address data
		notWant []string
	}{
		{
			name: "addressbook-multiget",
			body: `<?xml version="1.0" encoding="utf-8"?>
<C:addressbook-multiget xmlns:D="DAV:" xmlns:C="urn:ietf:params:xml:ns:carddav">
  <D:prop><D:getetag/><C:address-data/></D:prop>
  <D:href>` + xPropsObject + `</D:href>
</C:addressbook-multiget>`,
			want: []string{"X-APPLE-SORT-ORDER:2", "X-ABSHOWAS:COMPANY", "X-SOCIALPROFILE;TYPE=twitter;X-USER=johnappleseed:"},
		},
		{
			name: "addressbook-query on an extended property",
			body: `<?xml version="1.0" encoding="utf-8"?>
<C:addressbook-query xmlns:D="DAV:" xmlns:C="urn:ietf:params:xml:ns:carddav">
  <D:prop><C:address-data/></D:prop>
  <C:filter test="allof">
    <C:prop-filter name="X-APPLE-SORT-ORDER">
      <C:text-match match-type="equals">2</C:text-match>
    </C:prop-filter>
    <C:prop-filter name="X-ABShowAs">
      <C:text-match match-type="equals">COMPANY</C:text-match>
    </C:prop-filter>
  </C:filter>
</C:addressbook-query>`,
			want: []string{"X-APPLE-SORT-ORDER:2", "X-ABSHOWAS:COMPANY"},
		},
		{
			name: "addressbook-query on another extended property value",
			body: `<?xml version="1.0" encoding="utf-8"?>
<C:addressbook-query xmlns:D="DAV:" xmlns:C="urn:ietf:params:xml:ns:carddav">
  <D:prop><C:address-data/></D:prop>
  <C:filter>
    <C:prop-filter name="X-ABShowAs">
      <C:text-match match-type="equals">PERSON</C:text-match>
    </C:prop-filter>
  </C:filter>
</C:addressbook-query>`,
			want: nil,
		},
		{
			name: "addressbook-query for some extended properties",
			body: `<?xml version="1.0" encoding="utf-8"?>
<C:addressbook-query xmlns:D="DAV:" xmlns:C="urn:ietf:params:xml:ns:carddav">
  <D:prop>
    <C:address-data>
      <C:prop name="FN"/>
      <C:prop name="X-ABShowAs"/>
      <C:prop name="x-apple-sort-order"/>
    </C:address-data>
  </D:prop>
  <C:filter>
    <C:prop-filter name="UID"/>
  </C:filter>
</C:addressbook-query>`,
			want:    []string{"FN:John Appleseed", "X-ABSHOWAS:COMPANY", "X-APPLE-SORT-ORDER:2"},
			notWant: []string{"X-PHONETIC-FIRST-NAME", "X-SOCIALPROFILE"},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			resp, body := doXPropsRequest(t, ts, "REPORT", xPropsAddressBook, "application/xml", tc.body, "Depth", "1")
			if resp.StatusCode != http.StatusMultiStatus {
				t.Fatalf("got status %v, want 207 Multi-Status: %v", resp.Status, body)
			}
			if tc.want == nil {
				if strings.Contains(body, xPropsObject) {
					t.Errorf("the address object is in the response:\n%v", body)
				}
				return
			}
			for _, s := range tc.want {
				if !strings.Contains(body, s) {
					t.Errorf("%q missing from the response:\n%v", s, body)
				}
			}
			for _, s := range tc.notWant {
				if strings.Contains(body, s) {
					t.Errorf("%q in the response:\n%v", s, body)
				}
			}
		})
	}
}

// TestClientExtendedProperties checks that the client sends and receives the
// extended properties and parameters of address objects unchanged.
func TestClientExtendedProperties(t *testing.T) {
	ts, b := newXPropsServer(t)
	ctx := context.Background()

	c, err := NewClient(ts.Client(), ts.URL)
	if err != nil {
		t.Fatal(err)
	}

	if _, err := c.PutAddressObject(ctx, xPropsObject, decodeXPropsVCF(t), nil); err != nil {
		t.Fatalf("PutAddressObject: %v", err)
	}
	ao, ok := b.object(xPropsObject)
	if !ok {
		t.Fatalf("PutAddressObject: the backend didn't store the object")
	}
	checkXProps(t, ao.Card)

	t.Run("GetAddressObject", func(t *testing.T) {
		ao, err := c.GetAddressObject(ctx, xPropsObject)
		if err != nil {
			t.Fatal(err)
		}
		checkXProps(t, ao.Card)
	})

	t.Run("MultiGetAddressBook", func(t *testing.T) {
		aos, err := c.MultiGetAddressBook(ctx, xPropsAddressBook, &AddressBookMultiGet{
			Paths:       []string{xPropsObject},
			DataRequest: AddressDataRequest{AllProp: true},
		})
		if err != nil {
			t.Fatal(err)
		}
		if len(aos) != 1 {
			t.Fatalf("got %v address objects, want 1", len(aos))
		}
		checkXProps(t, aos[0].Card)
	})

	for _, tc := range []struct {
		name  string
		props []PropFilter
		want  int
	}{
		{
			name: "matching",
			props: []PropFilter{
				{Name: "X-APPLE-SORT-ORDER", TextMatches: []TextMatch{{Text: "2", MatchType: MatchEquals}}},
				{Name: "X-ABShowAs", TextMatches: []TextMatch{{Text: "COMPANY", MatchType: MatchEquals}}},
				{Name: "x-vendor-flags", TextMatches: []TextMatch{{Text: "pinned"}}},
			},
			want: 1,
		},
		{
			name:  "not matching",
			props: []PropFilter{{Name: "X-ABShowAs", TextMatches: []TextMatch{{Text: "PERSON", MatchType: MatchEquals}}}},
			want:  0,
		},
	} {
		t.Run("QueryAddressBook "+tc.name, func(t *testing.T) {
			aos, err := c.QueryAddressBook(ctx, xPropsAddressBook, &AddressBookQuery{
				DataRequest: AddressDataRequest{AllProp: true},
				PropFilters: tc.props,
				FilterTest:  FilterAllOf,
			})
			if err != nil {
				t.Fatal(err)
			}
			if len(aos) != tc.want {
				t.Fatalf("got %v address objects, want %v", len(aos), tc.want)
			}
			if tc.want > 0 {
				checkXProps(t, aos[0].Card)
			}
		})
	}

	t.Run("QueryAddressBook for some extended properties", func(t *testing.T) {
		aos, err := c.QueryAddressBook(ctx, xPropsAddressBook, &AddressBookQuery{
			DataRequest: AddressDataRequest{Props: []string{vcard.FieldFormattedName, "X-ABShowAs", "x-apple-sort-order"}},
			PropFilters: []PropFilter{{Name: vcard.FieldUID}},
		})
		if err != nil {
			t.Fatal(err)
		}
		if len(aos) != 1 {
			t.Fatalf("got %v address objects, want 1", len(aos))
		}
		want := vcard.Card{
			vcard.FieldVersion:       {{Value: "3.0"}},
			vcard.FieldFormattedName: {{Value: "John Appleseed"}},
			"X-ABSHOWAS":             {{Value: "COMPANY"}},
			"X-APPLE-SORT-ORDER":     {{Value: "2"}},
		}
		if got := aos[0].Card; !reflect.DeepEqual(got, want) {
			var gotBuf, wantBuf strings.Builder
			vcard.NewEncoder(&gotBuf).Encode(got)
			vcard.NewEncoder(&wantBuf).Encode(want)
			t.Errorf("got vCard:\n%v\nwant:\n%v", gotBuf.String(), wantBuf.String())
		}
	})
}
