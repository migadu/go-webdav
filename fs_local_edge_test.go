package webdav_test

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/emersion/go-webdav"
)

type failingReader struct {
	prefix string
	sent   bool
}

func (r *failingReader) Read(p []byte) (int, error) {
	if !r.sent {
		r.sent = true
		return copy(p, r.prefix), nil
	}
	return 0, errors.New("simulated read error")
}

func (r *failingReader) Close() error { return nil }

func TestLocalFileSystemEdgeCases(t *testing.T) {
	ctx := context.Background()

	t.Run("ReadDir root returns / instead of /.", func(t *testing.T) {
		dir := t.TempDir()
		writeTestFile(t, filepath.Join(dir, "a.txt"), "a")
		fs := webdav.LocalFileSystem(dir)

		entries, err := fs.ReadDir(ctx, "/", false)
		if err != nil {
			t.Fatalf("ReadDir(/): %v", err)
		}
		if len(entries) == 0 || entries[0].Path != "/" {
			t.Errorf("got first ReadDir(/) entry %+v, want Path=\"/\"", entries)
		}
	})

	t.Run("missing parent collection returns 409 Conflict across operations", func(t *testing.T) {
		dir := t.TempDir()
		writeTestFile(t, filepath.Join(dir, "file.txt"), "hello")
		writeTestFile(t, filepath.Join(dir, "col", "item.txt"), "item")
		writeTestSymlink(t, filepath.Join(dir, "link.txt"), "file.txt")
		fs := webdav.LocalFileSystem(dir)

		_, _, err := fs.Create(ctx, "/no-such-dir/new.txt", io.NopCloser(&failingReader{}), &webdav.CreateOptions{})
		checkHTTPErrorCode(t, err, http.StatusConflict)

		checkHTTPErrorCode(t, fs.Mkdir(ctx, "/no-such-dir/sub"), http.StatusConflict)

		_, err = fs.Copy(ctx, "/file.txt", "/no-such-dir/file.txt", &webdav.CopyOptions{})
		checkHTTPErrorCode(t, err, http.StatusConflict)

		_, err = fs.Copy(ctx, "/col", "/no-such-dir/col", &webdav.CopyOptions{})
		checkHTTPErrorCode(t, err, http.StatusConflict)

		_, err = fs.Copy(ctx, "/link.txt", "/no-such-dir/link.txt", &webdav.CopyOptions{})
		checkHTTPErrorCode(t, err, http.StatusConflict)

		_, err = fs.Move(ctx, "/file.txt", "/no-such-dir/file.txt", &webdav.MoveOptions{})
		checkHTTPErrorCode(t, err, http.StatusConflict)
		checkTestFile(t, filepath.Join(dir, "file.txt"), "hello")
	})

	t.Run("Move rejects moving directory into its own subtree", func(t *testing.T) {
		dir := t.TempDir()
		writeTestFile(t, filepath.Join(dir, "dir", "sub", "a.txt"), "a")
		fs := webdav.LocalFileSystem(dir)

		_, err := fs.Move(ctx, "/dir", "/dir/sub/moved", &webdav.MoveOptions{})
		checkHTTPErrorCode(t, err, http.StatusForbidden)
		checkTestFile(t, filepath.Join(dir, "dir", "sub", "a.txt"), "a")
	})

	t.Run("Copy and Move preserve data on same-file hardlink and case-insensitive aliases", func(t *testing.T) {
		dir := t.TempDir()
		origPath := filepath.Join(dir, "orig.txt")
		hardPath := filepath.Join(dir, "hard.txt")
		writeTestFile(t, origPath, "precious")
		if err := os.Link(origPath, hardPath); err != nil {
			t.Fatalf("os.Link: %v", err)
		}
		fs := webdav.LocalFileSystem(dir)

		// Copy onto hardlink of same file replaces it with a copy without losing data.
		if _, err := fs.Copy(ctx, "/orig.txt", "/hard.txt", &webdav.CopyOptions{}); err != nil {
			t.Fatalf("Copy(/orig.txt, /hard.txt): %v", err)
		}
		checkTestFile(t, origPath, "precious")
		checkTestFile(t, hardPath, "precious")
		if err := os.Remove(hardPath); err != nil {
			t.Fatal(err)
		}
		if err := os.Link(origPath, hardPath); err != nil {
			t.Fatalf("os.Link: %v", err)
		}

		// Move onto hardlink of same file must succeed and remove orig.txt while keeping hard.txt.
		created, err := fs.Move(ctx, "/orig.txt", "/hard.txt", &webdav.MoveOptions{})
		if err != nil {
			t.Fatalf("Move(/orig.txt, /hard.txt): %v", err)
		}
		if created {
			t.Errorf("Move onto existing hardlink reported created=true, want false")
		}
		if _, err := os.Lstat(origPath); !os.IsNotExist(err) {
			t.Errorf("expected orig.txt to be removed after Move, got stat err=%v", err)
		}
		checkTestFile(t, hardPath, "precious")

		// Check case-insensitive filesystem behavior if applicable (e.g. macOS APFS).
		caseLower := filepath.Join(dir, "readme.txt")
		caseUpper := filepath.Join(dir, "README.txt")
		writeTestFile(t, caseLower, "case-data")
		if fi1, err1 := os.Lstat(caseLower); err1 == nil {
			if fi2, err2 := os.Lstat(caseUpper); err2 == nil && os.SameFile(fi1, fi2) {
				_, err := fs.Copy(ctx, "/readme.txt", "/README.txt", &webdav.CopyOptions{})
				checkHTTPErrorCode(t, err, http.StatusForbidden)
				checkTestFile(t, caseUpper, "case-data")

				if _, err := fs.Move(ctx, "/readme.txt", "/README.txt", &webdav.MoveOptions{}); err != nil {
					t.Fatalf("case-only Move(/readme.txt, /README.txt) failed: %v", err)
				}
				checkTestFile(t, caseUpper, "case-data")
			}
		}
	})

	t.Run("failed Create overwrite preserves existing file and leaves no temp files", func(t *testing.T) {
		dir := t.TempDir()
		target := filepath.Join(dir, "keep.txt")
		writeTestFile(t, target, "original-content")
		if err := os.Chmod(target, 0600); err != nil {
			t.Fatal(err)
		}
		fs := webdav.LocalFileSystem(dir)

		_, _, err := fs.Create(ctx, "/keep.txt", &failingReader{prefix: "partial"}, &webdav.CreateOptions{})
		if err == nil {
			t.Fatalf("expected Create with failingReader to fail, got nil")
		}
		checkTestFile(t, target, "original-content")

		entries, err := os.ReadDir(dir)
		if err != nil {
			t.Fatal(err)
		}
		if len(entries) != 1 || entries[0].Name() != "keep.txt" {
			t.Errorf("unexpected leftover files in dir after failed Create: %v", entries)
		}
	})
}

// A destination whose path runs through a file, not a collection, has no
// parent collection either: PUT, MKCOL, COPY and MOVE fail with 409 Conflict
// (RFC 4918 sections 9.7.1, 9.3.1, 9.8.5 and 9.9.4).
func TestLocalFileSystemParentThroughFile(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	writeTestFile(t, filepath.Join(dir, "file.txt"), "hello")
	writeTestFile(t, filepath.Join(dir, "src.txt"), "src")
	writeTestFile(t, filepath.Join(dir, "col", "a.txt"), "a")
	fs := webdav.LocalFileSystem(dir)

	for _, parent := range []string{"/file.txt", "/file.txt/sub"} {
		_, _, err := fs.Create(ctx, parent+"/new.txt", io.NopCloser(strings.NewReader("x")), &webdav.CreateOptions{})
		checkHTTPErrorCode(t, err, http.StatusConflict)

		checkHTTPErrorCode(t, fs.Mkdir(ctx, parent+"/new"), http.StatusConflict)

		_, err = fs.Copy(ctx, "/src.txt", parent+"/copy.txt", &webdav.CopyOptions{})
		checkHTTPErrorCode(t, err, http.StatusConflict)

		_, err = fs.Copy(ctx, "/col", parent+"/col", &webdav.CopyOptions{})
		checkHTTPErrorCode(t, err, http.StatusConflict)

		_, err = fs.Move(ctx, "/src.txt", parent+"/moved.txt", &webdav.MoveOptions{})
		checkHTTPErrorCode(t, err, http.StatusConflict)
	}
	checkTestFile(t, filepath.Join(dir, "file.txt"), "hello")
	checkTestFile(t, filepath.Join(dir, "src.txt"), "src")
	checkTestFile(t, filepath.Join(dir, "col", "a.txt"), "a")

	h := &webdav.Handler{FileSystem: fs}
	for _, tc := range []struct {
		method, path, dest, body string
	}{
		{method: http.MethodPut, path: "/file.txt/sub/new.txt", body: "x"},
		// A MKCOL body would be refused with 415
		{method: "MKCOL", path: "/file.txt/sub/new"},
		{method: "COPY", path: "/src.txt", dest: "/file.txt/sub/copy.txt"},
		{method: "MOVE", path: "/src.txt", dest: "/file.txt/sub/moved.txt"},
	} {
		req := httptest.NewRequest(tc.method, tc.path, strings.NewReader(tc.body))
		if tc.dest != "" {
			req.Header.Set("Destination", tc.dest)
		}
		w := httptest.NewRecorder()
		h.ServeHTTP(w, req)
		if w.Code != http.StatusConflict {
			t.Errorf("%v %v: status = %d, want 409; body: %s", tc.method, tc.path, w.Code, strings.TrimSpace(w.Body.String()))
		}
	}
}

// A path running through a file maps to no resource: it's 404 Not Found
// (RFC 9110 section 15.5.5).
func TestLocalFileSystemPathThroughFileNotFound(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	writeTestFile(t, filepath.Join(dir, "file.txt"), "hello")
	fs := webdav.LocalFileSystem(dir)

	_, err := fs.Open(ctx, "/file.txt/x")
	checkHTTPErrorCode(t, err, http.StatusNotFound)
	_, err = fs.Stat(ctx, "/file.txt/x")
	checkHTTPErrorCode(t, err, http.StatusNotFound)
	_, err = fs.ReadDir(ctx, "/file.txt/x", false)
	checkHTTPErrorCode(t, err, http.StatusNotFound)
	checkHTTPErrorCode(t, fs.RemoveAll(ctx, "/file.txt/x", &webdav.RemoveAllOptions{}), http.StatusNotFound)
	_, err = fs.Copy(ctx, "/file.txt/x", "/copy.txt", &webdav.CopyOptions{})
	checkHTTPErrorCode(t, err, http.StatusNotFound)
	_, err = fs.Move(ctx, "/file.txt/x", "/moved.txt", &webdav.MoveOptions{})
	checkHTTPErrorCode(t, err, http.StatusNotFound)

	h := &webdav.Handler{FileSystem: fs}
	for _, method := range []string{http.MethodGet, "PROPFIND", http.MethodDelete} {
		req := httptest.NewRequest(method, "/file.txt/x", nil)
		w := httptest.NewRecorder()
		h.ServeHTTP(w, req)
		if w.Code != http.StatusNotFound {
			t.Errorf("%v /file.txt/x: status = %d, want 404; body: %s", method, w.Code, strings.TrimSpace(w.Body.String()))
		}
	}
	checkTestFile(t, filepath.Join(dir, "file.txt"), "hello")
}
