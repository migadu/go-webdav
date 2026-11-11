package webdav_test

import (
	"bytes"
	"context"
	"encoding/xml"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/emersion/go-webdav"
)

// streamTimeout bounds the wait for a response in the streaming tests, so
// that they fail rather than hang if responses aren't streamed.
const streamTimeout = 10 * time.Second

// streamFileSystem is a deadPropFileSystem listing /dir/, /dir/a.txt and
// /dir/b.txt in this order, whose dead properties are returned by
// deadProps.
type streamFileSystem struct {
	*deadPropFileSystem
	deadProps func(ctx context.Context, name string) ([]webdav.DeadProperty, error)
}

func newStreamFileSystem(deadProps func(ctx context.Context, name string) ([]webdav.DeadProperty, error)) *streamFileSystem {
	fs := newDeadPropFileSystem()
	fs.files["/dir/b.txt"] = webdav.FileInfo{Path: "/dir/b.txt", Size: 3, MIMEType: "text/plain", ETag: "b1"}
	return &streamFileSystem{deadPropFileSystem: fs, deadProps: deadProps}
}

func (fs *streamFileSystem) ReadDir(ctx context.Context, name string, recursive bool) ([]webdav.FileInfo, error) {
	var l []webdav.FileInfo
	for _, p := range []string{"/dir/", "/dir/a.txt", "/dir/b.txt"} {
		fi, err := fs.Stat(ctx, p)
		if err != nil {
			return nil, err
		}
		l = append(l, *fi)
	}
	return l, nil
}

func (fs *streamFileSystem) DeadProperties(ctx context.Context, name string) ([]webdav.DeadProperty, error) {
	return fs.deadProps(ctx, name)
}

// largeDeadProps returns a dead property larger than the buffers of net/http
// (a few KiB), so that writing the response holding it to an
// http.ResponseWriter sends it to the client.
func largeDeadProps() []webdav.DeadProperty {
	return []webdav.DeadProperty{{
		XMLName:  xml.Name{Space: zNamespace, Local: "big"},
		InnerXML: []byte(strings.Repeat("Streaming multistatus. ", 1500)),
	}}
}

// readUntil reads from r until what it read contains marker.
func readUntil(r io.Reader, marker string) ([]byte, error) {
	var b []byte
	buf := make([]byte, 512)
	for {
		n, err := r.Read(buf)
		b = append(b, buf[:n]...)
		if bytes.Contains(b, []byte(marker)) {
			return b, nil
		} else if err != nil {
			return b, err
		}
	}
}

func newPropFindRequest(t *testing.T, ctx context.Context, url string) *http.Request {
	t.Helper()
	req, err := http.NewRequestWithContext(ctx, "PROPFIND", url, strings.NewReader(propFindBody("<Z:big/>")))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/xml")
	req.Header.Set("Depth", "1")
	return req
}

// The response for each file of a Depth: 1 PROPFIND is sent to the client as
// soon as it's built, before the dead properties of the next file are
// fetched.
func TestPropFindStreaming(t *testing.T) {
	release := make(chan struct{})
	var releaseOnce sync.Once
	unblock := func() { releaseOnce.Do(func() { close(release) }) }

	fs := newStreamFileSystem(func(ctx context.Context, name string) ([]webdav.DeadProperty, error) {
		switch name {
		case "/dir/a.txt":
			return largeDeadProps(), nil
		case "/dir/b.txt":
			select {
			case <-release:
			case <-ctx.Done():
				return nil, ctx.Err()
			}
		}
		return nil, nil
	})
	srv := httptest.NewServer(&webdav.Handler{FileSystem: fs})
	defer srv.Close()
	defer unblock()

	ctx, cancel := context.WithTimeout(context.Background(), streamTimeout)
	defer cancel()
	resp, err := srv.Client().Do(newPropFindRequest(t, ctx, srv.URL+"/dir/"))
	if err != nil {
		t.Fatalf("no response while the last file is being read: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusMultiStatus {
		t.Fatalf("PROPFIND returned %v, want 207", resp.Status)
	}

	first, err := readUntil(resp.Body, "/dir/a.txt</href>")
	if err != nil {
		t.Fatalf("response for /dir/a.txt not received while the last file is being read: %v", err)
	}
	unblock()
	rest, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("failed to read the rest of the response: %v", err)
	}
	if b := append(first, rest...); !bytes.Contains(b, []byte("/dir/b.txt</href>")) || !bytes.HasSuffix(b, []byte("</multistatus>")) {
		t.Errorf("incomplete response: %s", b)
	}
}

var errDeadProps = errors.New("dead properties are gone")

// failingDeadProps returns the dead properties of a file, failing for the
// file failing.
func failingDeadProps(failing string) func(ctx context.Context, name string) ([]webdav.DeadProperty, error) {
	return func(ctx context.Context, name string) ([]webdav.DeadProperty, error) {
		switch name {
		case failing:
			return nil, errDeadProps
		case "/dir/a.txt":
			return largeDeadProps(), nil
		}
		return nil, nil
	}
}

// An error after some responses have been sent can't change the status of
// the request anymore: the response is aborted, without closing the
// multistatus element, so that the client can't mistake the responses it
// got for a complete result.
func TestPropFindErrorAfterStart(t *testing.T) {
	fs := newStreamFileSystem(failingDeadProps("/dir/b.txt"))
	h := &webdav.Handler{FileSystem: fs}

	t.Run("server", func(t *testing.T) {
		srv := httptest.NewServer(h)
		defer srv.Close()

		ctx, cancel := context.WithTimeout(context.Background(), streamTimeout)
		defer cancel()
		resp, err := srv.Client().Do(newPropFindRequest(t, ctx, srv.URL+"/dir/"))
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusMultiStatus {
			t.Fatalf("PROPFIND returned %v, want 207", resp.Status)
		}
		b, err := io.ReadAll(resp.Body)
		if !errors.Is(err, io.ErrUnexpectedEOF) {
			t.Errorf("reading the response returned %v, want %v", err, io.ErrUnexpectedEOF)
		}
		if !bytes.Contains(b, []byte("/dir/a.txt</href>")) {
			t.Errorf("response for /dir/a.txt missing: %s", b)
		}
		if bytes.Contains(b, []byte("</multistatus>")) || bytes.Contains(b, []byte(errDeadProps.Error())) {
			t.Errorf("aborted response looks complete or holds the error: %s", b)
		}
	})

	// Without an http.Server to abort the response, it's only left
	// unterminated
	t.Run("recorder", func(t *testing.T) {
		w := davRequest(h, "PROPFIND", "/dir/", "1", propFindBody("<Z:big/>"))
		if w.Code != http.StatusMultiStatus {
			t.Fatalf("PROPFIND returned %v, want 207: %s", w.Code, w.Body.String())
		}
		b := w.Body.Bytes()
		if !bytes.Contains(b, []byte("/dir/a.txt</href>")) {
			t.Errorf("response for /dir/a.txt missing: %s", b)
		}
		if bytes.Contains(b, []byte("/dir/b.txt</href>")) || bytes.Contains(b, []byte("</multistatus>")) || bytes.Contains(b, []byte(errDeadProps.Error())) {
			t.Errorf("aborted response looks complete or holds the error: %s", b)
		}
	})
}

// An error before any response has been written is served as the status of
// the request, as before.
func TestPropFindErrorBeforeStart(t *testing.T) {
	fs := newStreamFileSystem(failingDeadProps("/dir/"))
	h := &webdav.Handler{FileSystem: fs}

	w := davRequest(h, "PROPFIND", "/dir/", "1", propFindBody("<Z:big/>"))
	if w.Code != http.StatusInternalServerError {
		t.Errorf("PROPFIND returned %v, want 500: %s", w.Code, w.Body.String())
	}
	if strings.Contains(w.Body.String(), "multistatus") {
		t.Errorf("PROPFIND returned a multistatus along with its error: %s", w.Body.String())
	}
}
