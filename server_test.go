package webdav_test

import (
	"bytes"
	"compress/gzip"
	"compress/zlib"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/emersion/go-webdav"
)

// encodeBody applies the content codings listed in a Content-Encoding header
// value to b, in the order they are listed.
func encodeBody(t *testing.T, b []byte, codings string) []byte {
	t.Helper()
	for _, coding := range strings.Split(codings, ",") {
		var buf bytes.Buffer
		var wc io.WriteCloser
		switch strings.ToLower(strings.TrimSpace(coding)) {
		case "gzip", "x-gzip":
			wc = gzip.NewWriter(&buf)
		case "deflate":
			wc = zlib.NewWriter(&buf)
		case "identity", "":
			continue
		default:
			t.Fatalf("encodeBody: unsupported coding %q", coding)
		}
		if _, err := wc.Write(b); err != nil {
			t.Fatal(err)
		}
		if err := wc.Close(); err != nil {
			t.Fatal(err)
		}
		b = buf.Bytes()
	}
	return b
}

const propFindContentLength = `<?xml version="1.0" encoding="utf-8"?>
<d:propfind xmlns:d="DAV:">
  <d:prop>
    <d:getcontentlength/>
  </d:prop>
</d:propfind>`

func TestHandlerPropFindCompressedBody(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "hello.txt"), []byte("hello"), 0644); err != nil {
		t.Fatal(err)
	}
	h := &webdav.Handler{FileSystem: webdav.LocalFileSystem(dir)}

	for _, coding := range []string{"gzip", "x-gzip", "deflate", "identity", "GZip", "deflate, gzip"} {
		t.Run(coding, func(t *testing.T) {
			body := encodeBody(t, []byte(propFindContentLength), coding)
			req := httptest.NewRequest("PROPFIND", "/hello.txt", bytes.NewReader(body))
			req.Header.Set("Content-Type", "application/xml")
			req.Header.Set("Content-Encoding", coding)
			req.Header.Set("Depth", "0")
			w := httptest.NewRecorder()
			h.ServeHTTP(w, req)

			if w.Code != http.StatusMultiStatus {
				t.Fatalf("PROPFIND status = %d, want %d; body:\n%s", w.Code, http.StatusMultiStatus, w.Body)
			}
			if !strings.Contains(w.Body.String(), ">5</getcontentlength>") {
				t.Errorf("PROPFIND response lacks getcontentlength 5:\n%s", w.Body)
			}
		})
	}
}

func TestHandlerPropFindCompressedEmptyBody(t *testing.T) {
	h := &webdav.Handler{FileSystem: webdav.LocalFileSystem(t.TempDir())}

	// A PROPFIND without a body is an allprop request (RFC 4918 section 9.1).
	// The body must be considered empty once decoded, even though the
	// compressed representation of an empty body is not empty.
	for _, coding := range []string{"gzip", "deflate"} {
		t.Run(coding, func(t *testing.T) {
			req := httptest.NewRequest("PROPFIND", "/", bytes.NewReader(encodeBody(t, nil, coding)))
			req.Header.Set("Content-Encoding", coding)
			req.Header.Set("Depth", "0")
			w := httptest.NewRecorder()
			h.ServeHTTP(w, req)

			if w.Code != http.StatusMultiStatus {
				t.Fatalf("PROPFIND status = %d, want %d; body:\n%s", w.Code, http.StatusMultiStatus, w.Body)
			}
		})
	}
}

func TestHandlerPutCompressedBody(t *testing.T) {
	dir := t.TempDir()
	h := &webdav.Handler{FileSystem: webdav.LocalFileSystem(dir)}
	content := []byte(strings.Repeat("Hello, compressed world!\n", 100))

	for i, coding := range []string{"gzip", "x-gzip", "deflate", "identity", "GZIP", "deflate, gzip", "gzip,identity, gzip"} {
		t.Run(coding, func(t *testing.T) {
			name := fmt.Sprintf("file%d.txt", i)
			req := httptest.NewRequest(http.MethodPut, "/"+name, bytes.NewReader(encodeBody(t, content, coding)))
			req.Header.Set("Content-Encoding", coding)
			w := httptest.NewRecorder()
			h.ServeHTTP(w, req)

			if w.Code != http.StatusCreated {
				t.Fatalf("PUT status = %d, want %d; body:\n%s", w.Code, http.StatusCreated, w.Body)
			}
			got, err := os.ReadFile(filepath.Join(dir, name))
			if err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(got, content) {
				t.Errorf("stored file content = %q, want decoded request body", got)
			}
		})
	}
}

func TestHandlerUnsupportedContentEncoding(t *testing.T) {
	dir := t.TempDir()
	h := &webdav.Handler{FileSystem: webdav.LocalFileSystem(dir)}

	for _, coding := range []string{"br", "compress", "zstd", "gzip, br"} {
		t.Run(coding, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodPut, "/file.txt", strings.NewReader("whatever"))
			req.Header.Set("Content-Encoding", coding)
			w := httptest.NewRecorder()
			h.ServeHTTP(w, req)

			if w.Code != http.StatusUnsupportedMediaType {
				t.Fatalf("PUT status = %d, want %d; body:\n%s", w.Code, http.StatusUnsupportedMediaType, w.Body)
			}
			if got := w.Header().Get("Accept-Encoding"); !strings.Contains(got, "gzip") {
				t.Errorf("Accept-Encoding = %q, want a list of supported codings", got)
			}
			if _, err := os.Stat(filepath.Join(dir, "file.txt")); !os.IsNotExist(err) {
				t.Errorf("PUT with unsupported Content-Encoding created a file (stat error: %v)", err)
			}
		})
	}

	req := httptest.NewRequest("PROPFIND", "/", strings.NewReader(propFindContentLength))
	req.Header.Set("Content-Type", "application/xml")
	req.Header.Set("Content-Encoding", "br")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	if w.Code != http.StatusUnsupportedMediaType {
		t.Fatalf("PROPFIND status = %d, want %d; body:\n%s", w.Code, http.StatusUnsupportedMediaType, w.Body)
	}
}

func TestHandlerCompressedBodyTooLarge(t *testing.T) {
	dir := t.TempDir()
	h := &webdav.Handler{FileSystem: webdav.LocalFileSystem(dir)}

	// 16 MiB of zeros compress to about 16 KiB
	body := encodeBody(t, make([]byte, 16<<20), "gzip")
	req := httptest.NewRequest(http.MethodPut, "/bomb.txt", bytes.NewReader(body))
	req.Header.Set("Content-Encoding", "gzip")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)

	if w.Code != http.StatusRequestEntityTooLarge {
		t.Errorf("PUT status = %d, want %d; body:\n%s", w.Code, http.StatusRequestEntityTooLarge, w.Body)
	}
	if fi, err := os.Stat(filepath.Join(dir, "bomb.txt")); !os.IsNotExist(err) {
		t.Errorf("PUT of a decompression bomb left a file behind (stat: %v, %v)", fi, err)
	}

	propfind := strings.Replace(propFindContentLength, "<d:prop>", strings.Repeat(" ", 16<<20)+"<d:prop>", 1)
	body = encodeBody(t, []byte(propfind), "gzip")
	req = httptest.NewRequest("PROPFIND", "/", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/xml")
	req.Header.Set("Content-Encoding", "gzip")
	req.Header.Set("Depth", "0")
	w = httptest.NewRecorder()
	h.ServeHTTP(w, req)

	if w.Code != http.StatusRequestEntityTooLarge {
		t.Errorf("PROPFIND status = %d, want %d; body:\n%s", w.Code, http.StatusRequestEntityTooLarge, w.Body)
	}
}

const propFindCurrentUserPrincipal = `<?xml version="1.0" encoding="utf-8"?>
<d:propfind xmlns:d="DAV:">
  <d:prop>
    <d:current-user-principal/>
  </d:prop>
</d:propfind>`

func TestServePrincipalCompressedBody(t *testing.T) {
	body := encodeBody(t, []byte(propFindCurrentUserPrincipal), "gzip")
	req := httptest.NewRequest("PROPFIND", "/principal/", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/xml")
	req.Header.Set("Content-Encoding", "gzip")
	w := httptest.NewRecorder()
	webdav.ServePrincipal(w, req, &webdav.ServePrincipalOptions{
		CurrentUserPrincipalPath: "/principal/",
	})

	if w.Code != http.StatusMultiStatus {
		t.Fatalf("PROPFIND status = %d, want %d; body:\n%s", w.Code, http.StatusMultiStatus, w.Body)
	}
	if !strings.Contains(w.Body.String(), "<href>/principal/</href>") {
		t.Errorf("PROPFIND response lacks current-user-principal:\n%s", w.Body)
	}
}
