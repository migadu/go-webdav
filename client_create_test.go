package webdav_test

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/emersion/go-webdav"
)

// Create sends If-Match and If-None-Match, and a failed precondition is
// returned by Close.
func TestClientCreateConditional(t *testing.T) {
	ts := httptest.NewServer(&webdav.Handler{FileSystem: webdav.LocalFileSystem(t.TempDir())})
	defer ts.Close()

	c, err := webdav.NewClient(nil, ts.URL)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	create := func(opts *webdav.CreateOptions) error {
		wc, err := c.Create(ctx, "/file.txt", opts)
		if err != nil {
			return err
		}
		if _, err := io.WriteString(wc, "hello"); err != nil {
			return err
		}
		return wc.Close()
	}
	isPreconditionFailed := func(err error) bool {
		var httpErr *webdav.HTTPError
		return errors.As(err, &httpErr) && httpErr.Code == http.StatusPreconditionFailed
	}

	if err := create(&webdav.CreateOptions{IfNoneMatch: "*"}); err != nil {
		t.Fatalf("create-only Create of a new file: %v", err)
	}
	if err := create(&webdav.CreateOptions{IfNoneMatch: "*"}); !isPreconditionFailed(err) {
		t.Fatalf("create-only Create of an existing file = %v, want 412", err)
	}

	fi, err := c.Stat(ctx, "/file.txt")
	if err != nil {
		t.Fatal(err)
	}
	if err := create(&webdav.CreateOptions{IfMatch: webdav.ConditionalMatch(`"stale"`)}); !isPreconditionFailed(err) {
		t.Fatalf("Create with a stale If-Match = %v, want 412", err)
	}
	if err := create(&webdav.CreateOptions{IfMatch: webdav.ConditionalMatch(`"` + fi.ETag + `"`)}); err != nil {
		t.Fatalf("Create with the current If-Match: %v", err)
	}
	if err := create(nil); err != nil {
		t.Fatalf("unconditional Create: %v", err)
	}

	// The server refuses the request without reading a large body: Write
	// reports why, rather than a closed pipe
	wc, err := c.Create(ctx, "/file.txt", &webdav.CreateOptions{IfNoneMatch: "*"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := wc.Write(make([]byte, 4<<20)); !isPreconditionFailed(err) {
		t.Errorf("Write of a refused large body = %v, want 412", err)
	}
	if err := wc.Close(); !isPreconditionFailed(err) {
		t.Errorf("Close of a refused large body = %v, want 412", err)
	}
}
