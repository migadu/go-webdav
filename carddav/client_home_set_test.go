package carddav

import (
	"context"
	"encoding/xml"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"

	"github.com/emersion/go-webdav"
)

const homeSetTestPrincipal = "/principals/me/"

// newHomeSetTestClient returns a client of a server answering a PROPFIND
// request on principal with a response holding propstats.
func newHomeSetTestClient(t *testing.T, principal, propstats string) *Client {
	t.Helper()
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "PROPFIND" || r.URL.Path != principal {
			t.Errorf("got %v %v, want PROPFIND %v", r.Method, r.URL.Path, principal)
		}
		w.Header().Set("Content-Type", "application/xml; charset=utf-8")
		w.WriteHeader(http.StatusMultiStatus)
		io.WriteString(w, `<?xml version="1.0" encoding="utf-8"?>
<D:multistatus xmlns:D="DAV:" xmlns:C="urn:ietf:params:xml:ns:carddav">
  <D:response>
    <D:href>`+principal+`</D:href>
    `+propstats+`
  </D:response>
</D:multistatus>`)
	}))
	t.Cleanup(ts.Close)

	c, err := NewClient(ts.Client(), ts.URL)
	if err != nil {
		t.Fatal(err)
	}
	return c
}

// TestClientFindAddressBookHomeSetHref checks that FindAddressBookHomeSet
// reports a CARDDAV:addressbook-home-set without any href, which RFC 6352
// section 7.1.1 allows, as a missing home set, as when the property isn't
// defined. An empty href is a reference to the principal itself (RFC 3986
// section 4.4).
func TestClientFindAddressBookHomeSetHref(t *testing.T) {
	const principal = homeSetTestPrincipal
	for _, tc := range []struct {
		name      string
		propstats string
		want      string // empty if a 404 error is expected
	}{
		{
			name:      "href",
			propstats: `<D:propstat><D:prop><C:addressbook-home-set><D:href>/contacts/me/</D:href></C:addressbook-home-set></D:prop><D:status>HTTP/1.1 200 OK</D:status></D:propstat>`,
			want:      "/contacts/me/",
		},
		{
			name:      "empty href",
			propstats: `<D:propstat><D:prop><C:addressbook-home-set><D:href></D:href></C:addressbook-home-set></D:prop><D:status>HTTP/1.1 200 OK</D:status></D:propstat>`,
			want:      principal,
		},
		{
			// RFC 6352 section 7.1.1: DAV:href*
			name:      "two hrefs",
			propstats: `<D:propstat><D:prop><C:addressbook-home-set><D:href>/contacts/me/</D:href><D:href>/contacts/shared/</D:href></C:addressbook-home-set></D:prop><D:status>HTTP/1.1 200 OK</D:status></D:propstat>`,
			want:      "/contacts/me/",
		},
		{
			name:      "no href",
			propstats: `<D:propstat><D:prop><C:addressbook-home-set/></D:prop><D:status>HTTP/1.1 200 OK</D:status></D:propstat>`,
		},
		{
			name:      "not found",
			propstats: `<D:propstat><D:prop><C:addressbook-home-set/></D:prop><D:status>HTTP/1.1 404 Not Found</D:status></D:propstat>`,
		},
		{
			name:      "missing",
			propstats: `<D:propstat><D:prop><D:resourcetype><D:principal/></D:resourcetype></D:prop><D:status>HTTP/1.1 200 OK</D:status></D:propstat>`,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c := newHomeSetTestClient(t, principal, tc.propstats)
			got, err := c.FindAddressBookHomeSet(context.Background(), principal)
			if tc.want == "" {
				var httpErr *webdav.HTTPError
				if !errors.As(err, &httpErr) || httpErr.Code != http.StatusNotFound {
					t.Errorf("got %q, %v, want a 404 HTTPError", got, err)
				}
				return
			}
			if err != nil {
				t.Fatalf("FindAddressBookHomeSet: %v", err)
			}
			if got != tc.want {
				t.Errorf("got %q, want %q", got, tc.want)
			}
		})
	}
}

// TestClientFindAddressBookHomeSets checks that FindAddressBookHomeSets
// returns every href of CARDDAV:addressbook-home-set, in order, resolved as
// FindAddressBookHomeSet resolves its href.
func TestClientFindAddressBookHomeSets(t *testing.T) {
	const principal = homeSetTestPrincipal
	for _, tc := range []struct {
		name  string
		hrefs string
		want  []string // nil if a 404 error is expected
	}{
		{"href", `<D:href>/contacts/me/</D:href>`, []string{"/contacts/me/"}},
		{
			"several hrefs",
			`<D:href>/contacts/me/</D:href><D:href></D:href><D:href>shared/</D:href><D:href>https://other.example/contacts/me/</D:href>`,
			[]string{"/contacts/me/", principal, principal + "shared/", "https://other.example/contacts/me/"},
		},
		{"no href", ``, nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c := newHomeSetTestClient(t, principal, `<D:propstat><D:prop><C:addressbook-home-set>`+tc.hrefs+`</C:addressbook-home-set></D:prop><D:status>HTTP/1.1 200 OK</D:status></D:propstat>`)
			got, err := c.FindAddressBookHomeSets(context.Background(), principal)
			if tc.want == nil {
				var httpErr *webdav.HTTPError
				if !errors.As(err, &httpErr) || httpErr.Code != http.StatusNotFound {
					t.Errorf("got %q, %v, want a 404 HTTPError", got, err)
				}
				return
			}
			if err != nil {
				t.Fatalf("FindAddressBookHomeSets: %v", err)
			}
			if !reflect.DeepEqual(got, tc.want) {
				t.Errorf("got %q, want %q", got, tc.want)
			}
		})
	}
}

// TestNewAddressBookHomeSet pins the encoding of the addressbook-home-set
// servers report, empty path included.
func TestNewAddressBookHomeSet(t *testing.T) {
	for _, tc := range []struct {
		path, want string
	}{
		{"/contacts/me/", `<addressbook-home-set xmlns="urn:ietf:params:xml:ns:carddav"><href xmlns="DAV:">/contacts/me/</href></addressbook-home-set>`},
		{"", `<addressbook-home-set xmlns="urn:ietf:params:xml:ns:carddav"><href xmlns="DAV:"></href></addressbook-home-set>`},
	} {
		b, err := xml.Marshal(NewAddressBookHomeSet(tc.path))
		if err != nil {
			t.Fatalf("NewAddressBookHomeSet(%q): %v", tc.path, err)
		}
		if string(b) != tc.want {
			t.Errorf("NewAddressBookHomeSet(%q) = %s, want %s", tc.path, b, tc.want)
		}
	}
}
