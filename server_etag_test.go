package webdav

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// weakETagFileSystem serves a single file with a weak ETag.
type weakETagFileSystem struct {
	FileSystem
}

func (weakETagFileSystem) Stat(ctx context.Context, name string) (*FileInfo, error) {
	return &FileInfo{Path: name, Size: 3, ETag: `W/"xyzzy"`}, nil
}

func (weakETagFileSystem) Open(ctx context.Context, name string) (io.ReadCloser, error) {
	return io.NopCloser(strings.NewReader("abc")), nil
}

func TestServerWeakETag(t *testing.T) {
	h := Handler{FileSystem: weakETagFileSystem{}}

	req := httptest.NewRequest(http.MethodHead, "/file.txt", nil)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	if got, want := w.Header().Get("ETag"), `W/"xyzzy"`; got != want {
		t.Errorf("ETag header = %s, want %s", got, want)
	}

	req = httptest.NewRequest("PROPFIND", "/file.txt", strings.NewReader(`<?xml version="1.0"?>
<D:propfind xmlns:D="DAV:"><D:prop><D:getetag/></D:prop></D:propfind>`))
	req.Header.Set("Content-Type", "application/xml")
	req.Header.Set("Depth", "0")
	w = httptest.NewRecorder()
	h.ServeHTTP(w, req)
	if want := `<getetag xmlns="DAV:">W/&#34;xyzzy&#34;</getetag>`; !strings.Contains(w.Body.String(), want) {
		t.Errorf("PROPFIND response lacks %s:\n%s", want, w.Body.String())
	}
}
