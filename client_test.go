package webdav_test

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/emersion/go-webdav"
	"github.com/emersion/go-webdav/internal"
)

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
