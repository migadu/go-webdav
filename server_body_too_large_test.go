package webdav

import (
	"bytes"
	"compress/gzip"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// A LOCK request body larger than allowed is refused with 413 Content Too
// Large, as other request bodies are: whether http.MaxBytesReader limits it,
// or it's a decompression bomb.
func TestLockBodyTooLarge(t *testing.T) {
	lt := newLockTest(t)

	req := httptest.NewRequest("LOCK", "/file.txt", strings.NewReader(exclusiveLockInfo))
	req.Header.Set("Content-Type", "application/xml")
	w := httptest.NewRecorder()
	req.Body = http.MaxBytesReader(w, req.Body, 64)
	lt.h.ServeHTTP(w, req)
	if w.Code != http.StatusRequestEntityTooLarge {
		t.Errorf("LOCK over http.MaxBytesReader limit: status = %v, want 413\n%v", w.Code, w.Body.String())
	}

	// Other bodies too
	req = httptest.NewRequest("PROPFIND", "/file.txt", strings.NewReader(propFindLockDiscovery))
	req.Header.Set("Content-Type", "application/xml")
	w = httptest.NewRecorder()
	req.Body = http.MaxBytesReader(w, req.Body, 64)
	lt.h.ServeHTTP(w, req)
	if w.Code != http.StatusRequestEntityTooLarge {
		t.Errorf("PROPFIND over http.MaxBytesReader limit: status = %v, want 413\n%v", w.Code, w.Body.String())
	}

	// 16 MiB of whitespace compress to about 16 KiB
	bomb := strings.Replace(exclusiveLockInfo, "<D:locktype>", strings.Repeat(" ", 16<<20)+"<D:locktype>", 1)
	var buf bytes.Buffer
	zw := gzip.NewWriter(&buf)
	zw.Write([]byte(bomb))
	zw.Close()
	req = httptest.NewRequest("LOCK", "/file.txt", &buf)
	req.Header.Set("Content-Type", "application/xml")
	req.Header.Set("Content-Encoding", "gzip")
	w = httptest.NewRecorder()
	lt.h.ServeHTTP(w, req)
	if w.Code != http.StatusRequestEntityTooLarge {
		t.Errorf("LOCK with a decompression bomb: status = %v, want 413\n%v", w.Code, w.Body.String())
	}

	// None of them created a lock
	lt.expect(http.StatusNoContent, http.MethodPut, "/file.txt", "")
}
