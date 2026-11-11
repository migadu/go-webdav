package webdav

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
)

// RFC 4918 sections 9.8.5 and 9.9.4: COPY or MOVE of a resource onto itself
// is refused with 403 Forbidden, rather than overwriting it with itself.
func TestCopyMoveOntoItself(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "file.txt"), []byte("hello"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(dir, "dir"), 0755); err != nil {
		t.Fatal(err)
	}
	h := Handler{FileSystem: LocalFileSystem(dir)}

	for _, tc := range []struct {
		method, src, dest string
	}{
		{"COPY", "/file.txt", "/file.txt"},
		{"MOVE", "/file.txt", "/file.txt"},
		{"COPY", "/file.txt", "http://example.com/file.txt"},
		{"MOVE", "/dir/", "/dir"},
	} {
		req := httptest.NewRequest(tc.method, tc.src, nil)
		req.Header.Set("Destination", tc.dest)
		w := httptest.NewRecorder()
		h.ServeHTTP(w, req)
		if w.Code != http.StatusForbidden {
			t.Errorf("%s %s to %s returned %v, want 403", tc.method, tc.src, tc.dest, w.Code)
		}
	}

	if b, err := os.ReadFile(filepath.Join(dir, "file.txt")); err != nil || string(b) != "hello" {
		t.Errorf("file.txt = %q, %v, want it unchanged", b, err)
	}
	if fi, err := os.Stat(filepath.Join(dir, "dir")); err != nil || !fi.IsDir() {
		t.Errorf("dir is gone: %v", err)
	}
}
