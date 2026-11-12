package webdav_test

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/migadu/go-webdav"
	"github.com/migadu/go-webdav/internal"
)

// TestClientEscapedSlashEndpoint checks that the requests for the endpoint and
// for the paths relative to it keep an escaped "/", "%2F", in its path, as
// DiscoverContextURL may return it from a DNS TXT record: /dav/a%2Fb/ isn't
// /dav/a/b/.
func TestClientEscapedSlashEndpoint(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch key := r.Method + " " + r.URL.RequestURI(); key {
		case "PROPFIND /dav/a%2Fb/":
			w.Header().Set("Content-Type", "application/xml; charset=utf-8")
			w.WriteHeader(http.StatusMultiStatus)
			io.WriteString(w, `<?xml version="1.0" encoding="utf-8"?>
<D:multistatus xmlns:D="DAV:"><D:response><D:href>/dav/a%2Fb/</D:href>
<D:propstat><D:prop><D:current-user-principal><D:href>/principals/me/</D:href></D:current-user-principal></D:prop>
<D:status>HTTP/1.1 200 OK</D:status></D:propstat></D:response></D:multistatus>`)
		case "GET /dav/a%2Fb/c.txt":
			io.WriteString(w, "c")
		default:
			t.Errorf("unexpected request %v", key)
			http.NotFound(w, r)
		}
	}))
	defer ts.Close()

	c, err := webdav.NewClient(nil, ts.URL+"/dav/a%2Fb/")
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if principal, err := c.FindCurrentUserPrincipal(ctx); err != nil {
		t.Errorf("FindCurrentUserPrincipal() = %v", err)
	} else if principal != "/principals/me/" {
		t.Errorf("FindCurrentUserPrincipal() = %q, want %q", principal, "/principals/me/")
	}
	rc, err := c.Open(ctx, "c.txt")
	if err != nil {
		t.Fatalf("Open() = %v", err)
	}
	b, err := io.ReadAll(rc)
	rc.Close()
	if err != nil || string(b) != "c" {
		t.Errorf("Open() read %q, %v, want %q", b, err, "c")
	}
}

func TestClientErrorCarriesStatus(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.NotFound(w, r)
	}))
	defer ts.Close()

	c, err := webdav.NewClient(nil, ts.URL)
	if err != nil {
		t.Fatal(err)
	}
	_, err = c.Open(context.Background(), "/gone.txt")
	var httpErr *webdav.HTTPError
	if !errors.As(err, &httpErr) {
		t.Fatalf("Open() = %v, want an HTTPError", err)
	}
	if httpErr.Code != http.StatusNotFound {
		t.Errorf("HTTPError.Code = %d, want %d", httpErr.Code, http.StatusNotFound)
	}
}

// writeOps are the client operations that may affect several resources at
// once, and for which a server reports a partial failure with a 207
// Multi-Status response (RFC 4918 sections 9.6.1, 9.8.8 and 9.9.4).
var writeOps = []struct {
	method string
	do     func(ctx context.Context, c *webdav.Client) error
}{
	{"DELETE", func(ctx context.Context, c *webdav.Client) error {
		return c.RemoveAll(ctx, "/container/")
	}},
	{"COPY", func(ctx context.Context, c *webdav.Client) error {
		return c.Copy(ctx, "/container/", "/othercontainer/", nil)
	}},
	{"MOVE", func(ctx context.Context, c *webdav.Client) error {
		return c.Move(ctx, "/container/", "/othercontainer/", nil)
	}},
}

func newWriteOpServer(t *testing.T, method string, code int, body string) *webdav.Client {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != method {
			t.Errorf("request method = %q, want %q", r.Method, method)
		}
		if body != "" {
			w.Header().Set("Content-Type", "application/xml; charset=\"utf-8\"")
		}
		w.WriteHeader(code)
		w.Write([]byte(body))
	}))
	t.Cleanup(ts.Close)

	c, err := webdav.NewClient(nil, ts.URL)
	if err != nil {
		t.Fatal(err)
	}
	return c
}

// Adapted from RFC 4918 section 9.6.2: the server deleted every member of
// the collection but one, which is locked.
const multiStatusLocked = `<?xml version="1.0" encoding="utf-8" ?>
<d:multistatus xmlns:d="DAV:">
  <d:response>
    <d:href>/container/resource1</d:href>
    <d:status>HTTP/1.1 204 No Content</d:status>
  </d:response>
  <d:response>
    <d:href>/container/resource3</d:href>
    <d:status>HTTP/1.1 423 Locked</d:status>
    <d:error><d:lock-token-submitted/></d:error>
  </d:response>
</d:multistatus>`

func TestClientWriteMultiStatusError(t *testing.T) {
	for _, op := range writeOps {
		t.Run(op.method, func(t *testing.T) {
			c := newWriteOpServer(t, op.method, http.StatusMultiStatus, multiStatusLocked)

			err := op.do(context.Background(), c)
			if err == nil {
				t.Fatalf("%v: got nil error for a 207 response with a 423 member", op.method)
			}

			var httpErr *webdav.HTTPError
			if !errors.As(err, &httpErr) {
				t.Fatalf("%v: error %v is not an HTTPError", op.method, err)
			}
			if httpErr.Code != http.StatusLocked {
				t.Errorf("%v: HTTPError.Code = %d, want %d", op.method, httpErr.Code, http.StatusLocked)
			}

			var hrefErr *internal.HrefError
			if !errors.As(err, &hrefErr) {
				t.Fatalf("%v: error %v is not an HrefError", op.method, err)
			}
			if hrefErr.Href.Path != "/container/resource3" {
				t.Errorf("%v: HrefError.Href = %q, want %q", op.method, hrefErr.Href.Path, "/container/resource3")
			}
		})
	}
}

func TestClientWriteMultiStatusSuccess(t *testing.T) {
	const body = `<?xml version="1.0" encoding="utf-8" ?>
<d:multistatus xmlns:d="DAV:">
  <d:response>
    <d:href>/container/resource1</d:href>
    <d:status>HTTP/1.1 204 No Content</d:status>
  </d:response>
</d:multistatus>`

	for _, op := range writeOps {
		t.Run(op.method, func(t *testing.T) {
			c := newWriteOpServer(t, op.method, http.StatusMultiStatus, body)
			if err := op.do(context.Background(), c); err != nil {
				t.Errorf("%v: got error %v for a 207 response without failures", op.method, err)
			}
		})
	}
}

func TestClientWriteNoContent(t *testing.T) {
	for _, op := range writeOps {
		t.Run(op.method, func(t *testing.T) {
			c := newWriteOpServer(t, op.method, http.StatusNoContent, "")
			if err := op.do(context.Background(), c); err != nil {
				t.Errorf("%v: got error %v for a 204 response", op.method, err)
			}
		})
	}
}
