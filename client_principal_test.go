package webdav_test

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/emersion/go-webdav"
)

// TestClientFindCurrentUserPrincipalHref checks that FindCurrentUserPrincipal
// tells an empty href, which refers to the request URL (RFC 3986 section
// 4.4), from a DAV:current-user-principal without any href or
// DAV:unauthenticated, which RFC 5397 section 3 doesn't allow.
func TestClientFindCurrentUserPrincipalHref(t *testing.T) {
	const endpoint = "/dav/"
	for _, tc := range []struct {
		name string
		prop string
		want string // empty if an error is expected
	}{
		{"href", `<D:current-user-principal><D:href>/principals/me/</D:href></D:current-user-principal>`, "/principals/me/"},
		{"empty href", `<D:current-user-principal><D:href></D:href></D:current-user-principal>`, endpoint},
		{"no href", `<D:current-user-principal/>`, ""},
		{"no href, other element", `<D:current-user-principal><D:foo/></D:current-user-principal>`, ""},
		{"unauthenticated", `<D:current-user-principal><D:unauthenticated/></D:current-user-principal>`, ""},
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
    <D:propstat><D:prop>`+tc.prop+`</D:prop><D:status>HTTP/1.1 200 OK</D:status></D:propstat>
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
				if err == nil {
					t.Errorf("got %q, want an error", got)
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
