package webdav_test

import (
	"context"
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
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
