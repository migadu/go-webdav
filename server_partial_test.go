package webdav_test

import (
	"context"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/emersion/go-webdav"
	"github.com/emersion/go-webdav/internal"
)

// testFileSystem is a FileSystem whose directory listing and write
// operations return canned results.
type testFileSystem struct {
	children   []webdav.FileInfo
	readDirErr error
	removeErr  error
	copyErr    error
	moveErr    error
}

var _ webdav.FileSystem = (*testFileSystem)(nil)

func (fs *testFileSystem) Open(ctx context.Context, name string) (io.ReadCloser, error) {
	return nil, webdav.NewHTTPError(http.StatusNotFound, nil)
}

func (fs *testFileSystem) Stat(ctx context.Context, name string) (*webdav.FileInfo, error) {
	if strings.HasSuffix(name, "/") {
		return &webdav.FileInfo{Path: name, IsDir: true}, nil
	}
	return &webdav.FileInfo{Path: name, Size: 1}, nil
}

func (fs *testFileSystem) ReadDir(ctx context.Context, name string, recursive bool) ([]webdav.FileInfo, error) {
	return fs.children, fs.readDirErr
}

func (fs *testFileSystem) Create(ctx context.Context, name string, body io.ReadCloser, opts *webdav.CreateOptions) (*webdav.FileInfo, bool, error) {
	return nil, false, webdav.NewHTTPError(http.StatusForbidden, nil)
}

func (fs *testFileSystem) RemoveAll(ctx context.Context, name string, opts *webdav.RemoveAllOptions) error {
	return fs.removeErr
}

func (fs *testFileSystem) Mkdir(ctx context.Context, name string) error {
	return webdav.NewHTTPError(http.StatusForbidden, nil)
}

func (fs *testFileSystem) Copy(ctx context.Context, name, dest string, options *webdav.CopyOptions) (bool, error) {
	return false, fs.copyErr
}

func (fs *testFileSystem) Move(ctx context.Context, name, dest string, options *webdav.MoveOptions) (bool, error) {
	return false, fs.moveErr
}

var twoFiles = []webdav.FileInfo{
	{Path: "/dir/a.txt", Size: 1},
	{Path: "/dir/b.txt", Size: 2},
}

func serve(h http.Handler, method, target string, header map[string]string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, target, nil)
	for k, v := range header {
		req.Header.Set(k, v)
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	return w
}

// responseCodes decodes a multistatus body and maps each href to the status
// of its response. A response without a top-level status (i.e. a PROPFIND
// response carrying propstats) is reported as 200.
func responseCodes(t *testing.T, w *httptest.ResponseRecorder) (map[string]int, map[string]internal.Response) {
	t.Helper()
	if w.Code != http.StatusMultiStatus {
		t.Fatalf("status = %d, want %d; body:\n%s", w.Code, http.StatusMultiStatus, w.Body.String())
	}
	var ms internal.MultiStatus
	if err := xml.NewDecoder(w.Body).Decode(&ms); err != nil {
		t.Fatalf("failed to decode multistatus: %v", err)
	}
	codes := make(map[string]int)
	resps := make(map[string]internal.Response)
	for _, resp := range ms.Responses {
		if len(resp.Hrefs) != 1 {
			t.Fatalf("response has %d hrefs, want 1", len(resp.Hrefs))
		}
		p := resp.Hrefs[0].Path
		if _, dup := codes[p]; dup {
			t.Errorf("duplicate response for %q", p)
		}
		code := http.StatusOK
		if resp.Status != nil {
			code = resp.Status.Code
		}
		codes[p] = code
		resps[p] = resp
	}
	return codes, resps
}

func checkCodes(t *testing.T, got, want map[string]int) {
	t.Helper()
	if len(got) != len(want) {
		t.Errorf("got %d responses %v, want %d %v", len(got), got, len(want), want)
	}
	for p, code := range want {
		if got[p] != code {
			t.Errorf("response for %q: status = %d, want %d", p, got[p], code)
		}
	}
}

func TestHandler_propFindPartialFailure(t *testing.T) {
	fs := &testFileSystem{
		children: twoFiles,
		readDirErr: webdav.NewHrefError("/dir/c.txt",
			webdav.NewHTTPError(http.StatusForbidden, errors.New("permission denied"))),
	}
	w := serve(&webdav.Handler{FileSystem: fs}, "PROPFIND", "/dir/", map[string]string{"Depth": "1"})

	codes, resps := responseCodes(t, w)
	checkCodes(t, codes, map[string]int{
		"/dir/a.txt": http.StatusOK,
		"/dir/b.txt": http.StatusOK,
		"/dir/c.txt": http.StatusForbidden,
	})
	if desc := resps["/dir/c.txt"].ResponseDescription; !strings.Contains(desc, "permission denied") {
		t.Errorf("responsedescription = %q, want it to mention the cause", desc)
	}
	if len(resps["/dir/c.txt"].PropStats) != 0 {
		t.Errorf("failed response has propstats: %+v", resps["/dir/c.txt"].PropStats)
	}
}

func TestHandler_propFindPartialFailureJoined(t *testing.T) {
	fs := &testFileSystem{
		children: twoFiles,
		readDirErr: errors.Join(
			webdav.NewHrefError("/dir/c.txt", webdav.NewHTTPError(http.StatusForbidden, nil)),
			// Without an HTTPError, the status defaults to 500.
			webdav.NewHrefError("/dir/d.txt", errors.New("I/O error")),
		),
	}
	w := serve(&webdav.Handler{FileSystem: fs}, "PROPFIND", "/dir/", map[string]string{"Depth": "1"})

	codes, _ := responseCodes(t, w)
	checkCodes(t, codes, map[string]int{
		"/dir/a.txt": http.StatusOK,
		"/dir/b.txt": http.StatusOK,
		"/dir/c.txt": http.StatusForbidden,
		"/dir/d.txt": http.StatusInternalServerError,
	})
}

func TestHandler_propFindPlainError(t *testing.T) {
	for _, tc := range []struct {
		name string
		err  error
		code int
	}{
		{"HTTPError", webdav.NewHTTPError(http.StatusForbidden, nil), http.StatusForbidden},
		{"error", errors.New("disk on fire"), http.StatusInternalServerError},
		{
			"joined with a plain error",
			errors.Join(webdav.NewHrefError("/dir/c.txt", errors.New("I/O error")), errors.New("disk on fire")),
			http.StatusInternalServerError,
		},
		{
			"wrapped HrefError",
			fmt.Errorf("listing: %w", webdav.NewHrefError("/dir/c.txt", webdav.NewHTTPError(http.StatusForbidden, nil))),
			http.StatusForbidden,
		},
		{
			// An href is an absolute path (RFC 4918 section 8.3): an empty
			// one would refer to the request target
			"HrefError with an empty path",
			webdav.NewHrefError("", webdav.NewHTTPError(http.StatusForbidden, nil)),
			http.StatusForbidden,
		},
		{
			"HrefError with a relative path",
			errors.Join(
				webdav.NewHrefError("/dir/c.txt", webdav.NewHTTPError(http.StatusForbidden, nil)),
				webdav.NewHrefError("d.txt", webdav.NewHTTPError(http.StatusForbidden, nil)),
			),
			http.StatusForbidden,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fs := &testFileSystem{children: twoFiles, readDirErr: tc.err}
			w := serve(&webdav.Handler{FileSystem: fs}, "PROPFIND", "/dir/", map[string]string{"Depth": "1"})
			if w.Code != tc.code {
				t.Errorf("status = %d, want %d; body:\n%s", w.Code, tc.code, w.Body.String())
			}
		})
	}
}

func TestHandler_deletePartialFailure(t *testing.T) {
	fs := &testFileSystem{
		removeErr: webdav.NewHrefError("/dir/locked.txt", webdav.NewHTTPError(http.StatusLocked, nil)),
	}
	w := serve(&webdav.Handler{FileSystem: fs}, http.MethodDelete, "/dir/", nil)

	// RFC 4918 section 9.6.1: only the members that couldn't be deleted are
	// listed, without a 424 for their parents.
	codes, _ := responseCodes(t, w)
	checkCodes(t, codes, map[string]int{
		"/dir/locked.txt": http.StatusLocked,
	})
}

func TestHandler_deletePartialFailureJoined(t *testing.T) {
	fs := &testFileSystem{
		removeErr: errors.Join(
			webdav.NewHrefError("/dir/locked.txt", webdav.NewHTTPError(http.StatusLocked, nil)),
			webdav.NewHrefError("/dir/sub/", webdav.NewHTTPError(http.StatusForbidden, nil)),
		),
	}
	w := serve(&webdav.Handler{FileSystem: fs}, http.MethodDelete, "/dir/", nil)

	codes, _ := responseCodes(t, w)
	checkCodes(t, codes, map[string]int{
		"/dir/locked.txt": http.StatusLocked,
		"/dir/sub/":       http.StatusForbidden,
	})
}

func TestHandler_deleteError(t *testing.T) {
	for _, tc := range []struct {
		name   string
		target string
		err    error
		code   int
	}{
		{"none", "/dir/", nil, http.StatusNoContent},
		{"HTTPError", "/dir/", webdav.NewHTTPError(http.StatusLocked, nil), http.StatusLocked},
		{"error", "/dir/", errors.New("disk on fire"), http.StatusInternalServerError},
		{
			// A failure of the request target itself isn't a partial failure.
			"HrefError for the target",
			"/dir/file.txt",
			webdav.NewHrefError("/dir/file.txt", webdav.NewHTTPError(http.StatusPreconditionFailed, nil)),
			http.StatusPreconditionFailed,
		},
		{
			"HrefError for the target without trailing slash",
			"/dir/",
			webdav.NewHrefError("/dir", webdav.NewHTTPError(http.StatusForbidden, nil)),
			http.StatusForbidden,
		},
		{
			// An href is an absolute path (RFC 4918 section 8.3): an empty
			// one would refer to the request target, so does the error
			"HrefError with an empty path, on the root",
			"/",
			webdav.NewHrefError("", webdav.NewHTTPError(http.StatusLocked, nil)),
			http.StatusLocked,
		},
		{
			"HrefError with an empty path",
			"/dir/",
			webdav.NewHrefError("", webdav.NewHTTPError(http.StatusLocked, nil)),
			http.StatusLocked,
		},
		{
			"HrefError with a relative path",
			"/dir/",
			webdav.NewHrefError("locked.txt", webdav.NewHTTPError(http.StatusLocked, nil)),
			http.StatusLocked,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fs := &testFileSystem{removeErr: tc.err}
			w := serve(&webdav.Handler{FileSystem: fs}, http.MethodDelete, tc.target, nil)
			if w.Code != tc.code {
				t.Errorf("status = %d, want %d; body:\n%s", w.Code, tc.code, w.Body.String())
			}
		})
	}
}

func TestHandler_copyMovePartialFailure(t *testing.T) {
	hrefErr := webdav.NewHrefError("/dst/locked.txt", webdav.NewHTTPError(http.StatusLocked, nil))
	for _, method := range []string{"COPY", "MOVE"} {
		t.Run(method, func(t *testing.T) {
			fs := &testFileSystem{copyErr: hrefErr, moveErr: hrefErr}
			w := serve(&webdav.Handler{FileSystem: fs}, method, "/dir/", map[string]string{
				"Destination": "http://example.com/dst/",
			})

			codes, _ := responseCodes(t, w)
			checkCodes(t, codes, map[string]int{
				"/dst/locked.txt": http.StatusLocked,
			})
		})
	}
}

func TestHandler_copyMoveError(t *testing.T) {
	for _, tc := range []struct {
		name   string
		target string
		dest   string
		err    error
		code   int
	}{
		{"HTTPError", "/dir/", "http://example.com/dst/", webdav.NewHTTPError(http.StatusForbidden, nil), http.StatusForbidden},
		{
			// A failure of the source itself isn't a partial failure.
			"HrefError for the source",
			"/a.txt", "http://example.com/b.txt",
			webdav.NewHrefError("/a.txt", webdav.NewHTTPError(http.StatusLocked, nil)),
			http.StatusLocked,
		},
		{
			// Neither is a failure of the destination itself, e.g. a locked
			// destination (RFC 4918 section 9.8.5).
			"HrefError for the destination",
			"/a.txt", "http://example.com/b.txt",
			webdav.NewHrefError("/b.txt", webdav.NewHTTPError(http.StatusLocked, nil)),
			http.StatusLocked,
		},
		{
			"HrefError for the destination without trailing slash",
			"/dir/", "http://example.com/dst/",
			webdav.NewHrefError("/dst", webdav.NewHTTPError(http.StatusForbidden, nil)),
			http.StatusForbidden,
		},
		{
			"HrefError with an empty path, from the root",
			"/", "http://example.com/dst/",
			webdav.NewHrefError("", webdav.NewHTTPError(http.StatusLocked, nil)),
			http.StatusLocked,
		},
		{
			"HrefError with an empty path",
			"/dir/", "http://example.com/dst/",
			webdav.NewHrefError("", webdav.NewHTTPError(http.StatusLocked, nil)),
			http.StatusLocked,
		},
		{
			"HrefErrors for the source and the destination",
			"/a.txt", "/b.txt",
			errors.Join(
				webdav.NewHrefError("/a.txt", webdav.NewHTTPError(http.StatusLocked, nil)),
				webdav.NewHrefError("/b.txt", webdav.NewHTTPError(http.StatusLocked, nil)),
			),
			http.StatusLocked,
		},
	} {
		for _, method := range []string{"COPY", "MOVE"} {
			t.Run(tc.name+"/"+method, func(t *testing.T) {
				fs := &testFileSystem{copyErr: tc.err, moveErr: tc.err}
				w := serve(&webdav.Handler{FileSystem: fs}, method, tc.target, map[string]string{
					"Destination": tc.dest,
				})
				if w.Code != tc.code {
					t.Errorf("status = %d, want %d; body:\n%s", w.Code, tc.code, w.Body.String())
				}
			})
		}
	}
}

// TestClient_readDirPartialFailure checks that the client decodes the
// multistatus sent for a partial failure back into FileInfos and per-href
// errors.
func TestClient_readDirPartialFailure(t *testing.T) {
	fs := &testFileSystem{
		children: twoFiles,
		readDirErr: webdav.NewHrefError("/dir/c.txt",
			webdav.NewHTTPError(http.StatusForbidden, errors.New("permission denied"))),
	}
	ts := httptest.NewServer(&webdav.Handler{FileSystem: fs})
	defer ts.Close()

	c, err := webdav.NewClient(nil, ts.URL)
	if err != nil {
		t.Fatal(err)
	}
	fis, err := c.ReadDir(context.Background(), "/dir/", false)

	if len(fis) != 2 || fis[0].Path != "/dir/a.txt" || fis[1].Path != "/dir/b.txt" {
		t.Errorf("ReadDir() = %+v, want /dir/a.txt and /dir/b.txt", fis)
	} else if fis[0].Size != 1 || fis[1].Size != 2 {
		t.Errorf("ReadDir() sizes = %d, %d, want 1, 2", fis[0].Size, fis[1].Size)
	}

	var hrefErr *webdav.HrefError
	if !errors.As(err, &hrefErr) {
		t.Fatalf("ReadDir() error = %v, want an HrefError", err)
	}
	if hrefErr.Href.Path != "/dir/c.txt" {
		t.Errorf("HrefError.Href.Path = %q, want %q", hrefErr.Href.Path, "/dir/c.txt")
	}
	var httpErr *webdav.HTTPError
	if !errors.As(err, &httpErr) {
		t.Fatalf("ReadDir() error = %v, want an HTTPError", err)
	}
	if httpErr.Code != http.StatusForbidden {
		t.Errorf("HTTPError.Code = %d, want %d", httpErr.Code, http.StatusForbidden)
	}
	if got, want := err.Error(), "/dir/c.txt: 403 Forbidden: permission denied"; got != want {
		t.Errorf("ReadDir() error = %q, want %q", got, want)
	}
}
