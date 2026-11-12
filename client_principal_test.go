package webdav_test

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/emersion/go-webdav"
)

// TestClientFindCurrentUserPrincipalHref checks that FindCurrentUserPrincipal
// tells an empty href, which refers to the request URL (RFC 3986 section
// 4.4), from a DAV:current-user-principal without any href or
// DAV:unauthenticated, with both, or with several hrefs, which RFC 5397
// section 3 doesn't allow: <!ELEMENT current-user-principal (unauthenticated
// | href)>.
func TestClientFindCurrentUserPrincipalHref(t *testing.T) {
	const (
		endpoint        = "/dav/"
		malformed       = "webdav: malformed current-user-principal property"
		unauthenticated = "webdav: unauthenticated"
	)
	for _, tc := range []struct {
		name string
		prop string
		want string // empty if an error is expected
		// The start of the expected error
		wantErr string
	}{
		{"href", `<D:current-user-principal><D:href>/principals/me/</D:href></D:current-user-principal>`, "/principals/me/", ""},
		{"empty href", `<D:current-user-principal><D:href></D:href></D:current-user-principal>`, endpoint, ""},
		// An empty path with a host is "/" (RFC 3986 section 6.2.3)
		{"origin URL", `<D:current-user-principal><D:href>http://{host}</D:href></D:current-user-principal>`, "/", ""},
		{"network-path reference without a path", `<D:current-user-principal><D:href>//{host}</D:href></D:current-user-principal>`, "/", ""},
		{"no href", `<D:current-user-principal/>`, "", malformed},
		{"no href, other element", `<D:current-user-principal><D:foo/></D:current-user-principal>`, "", malformed},
		{"unauthenticated", `<D:current-user-principal><D:unauthenticated/></D:current-user-principal>`, "", unauthenticated},
		{"unauthenticated and href", `<D:current-user-principal><D:unauthenticated/><D:href>/principals/me/</D:href></D:current-user-principal>`, "", malformed},
		{"href and unauthenticated", `<D:current-user-principal><D:href>/principals/me/</D:href><D:unauthenticated/></D:current-user-principal>`, "", malformed},
		{"two hrefs", `<D:current-user-principal><D:href>/principals/alice/</D:href><D:href>/principals/bob/</D:href></D:current-user-principal>`, "", malformed},
		{"two hrefs, one empty", `<D:current-user-principal><D:href>/principals/alice/</D:href><D:href></D:href></D:current-user-principal>`, "", malformed},
		{"two hrefs and unauthenticated", `<D:current-user-principal><D:href>/principals/alice/</D:href><D:href>/principals/bob/</D:href><D:unauthenticated/></D:current-user-principal>`, "", malformed},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method != "PROPFIND" || r.URL.Path != endpoint {
					t.Errorf("got %v %v, want PROPFIND %v", r.Method, r.URL.Path, endpoint)
				}
				w.Header().Set("Content-Type", "application/xml; charset=utf-8")
				w.WriteHeader(http.StatusMultiStatus)
				io.WriteString(w, `<?xml version="1.0" encoding="utf-8"?>
<D:multistatus xmlns:D="DAV:">
  <D:response>
    <D:href>`+endpoint+`</D:href>
    <D:propstat><D:prop>`+strings.ReplaceAll(tc.prop, "{host}", r.Host)+`</D:prop><D:status>HTTP/1.1 200 OK</D:status></D:propstat>
  </D:response>
</D:multistatus>`)
			}))
			defer ts.Close()

			c, err := webdav.NewClient(ts.Client(), ts.URL+endpoint)
			if err != nil {
				t.Fatal(err)
			}
			got, err := c.FindCurrentUserPrincipal(context.Background())
			if tc.want == "" {
				if err == nil || !strings.HasPrefix(err.Error(), tc.wantErr) {
					t.Errorf("got %q, %v, want an error starting with %q", got, err, tc.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("FindCurrentUserPrincipal: %v", err)
			}
			if got != tc.want {
				t.Errorf("got %q, want %q", got, tc.want)
			}
		})
	}
}
