package carddav

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/emersion/go-webdav"
)

// TestClientFindAddressBookHomeSetHref checks that FindAddressBookHomeSet
// reports a CARDDAV:addressbook-home-set without any href, which RFC 6352
// section 7.1.1 allows, as a missing home set, as when the property isn't
// defined. An empty href is a reference to the principal itself (RFC 3986
// section 4.4).
func TestClientFindAddressBookHomeSetHref(t *testing.T) {
	const principal = "/principals/me/"
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
    `+tc.propstats+`
  </D:response>
</D:multistatus>`)
			}))
			defer ts.Close()

			c, err := NewClient(ts.Client(), ts.URL)
			if err != nil {
				t.Fatal(err)
			}
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
