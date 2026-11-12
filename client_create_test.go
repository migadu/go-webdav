package webdav_test

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/migadu/go-webdav"
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

// httpClientFunc is a webdav.HTTPClient calling a function.
type httpClientFunc func(req *http.Request) (*http.Response, error)

func (f httpClientFunc) Do(req *http.Request) (*http.Response, error) {
	return f(req)
}

// Close may be called while a Write is blocked, e.g. from another goroutine to
// abort a Write to a server which doesn't read the body: both return the
// request's error once it completes, rather than one of them hanging.
func TestClientCreateConcurrentWriteClose(t *testing.T) {
	release := make(chan struct{})
	c, err := webdav.NewClient(httpClientFunc(func(req *http.Request) (*http.Response, error) {
		// Don't read the body, so that Write blocks
		<-release
		req.Body.Close()
		return &http.Response{
			Status:     "412 Precondition Failed",
			StatusCode: http.StatusPreconditionFailed,
			Header:     make(http.Header),
			Body:       io.NopCloser(strings.NewReader("")),
			Request:    req,
		}, nil
	}), "http://example.org")
	if err != nil {
		t.Fatal(err)
	}

	wc, err := c.Create(context.Background(), "/file.txt", &webdav.CreateOptions{IfNoneMatch: "*"})
	if err != nil {
		t.Fatal(err)
	}

	errs := make(chan error, 2)
	go func() {
		_, err := wc.Write([]byte("hello"))
		errs <- err
	}()
	go func() {
		errs <- wc.Close()
	}()
	// Let both wait for the request before it completes
	time.Sleep(50 * time.Millisecond)
	close(release)

	timeout := time.After(5 * time.Second)
	for i := 0; i < 2; i++ {
		select {
		case err := <-errs:
			var httpErr *webdav.HTTPError
			if !errors.As(err, &httpErr) || httpErr.Code != http.StatusPreconditionFailed {
				t.Errorf("concurrent Write or Close = %v, want 412", err)
			}
		case <-timeout:
			t.Fatal("concurrent Write and Close hung")
		}
	}
}
