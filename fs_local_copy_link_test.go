package webdav_test

import (
	"context"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"testing"

	"github.com/emersion/go-webdav"
)

// checkNotSameFile checks that a and b, two existing files, are distinct
// files.
func checkNotSameFile(t *testing.T, a, b string) {
	t.Helper()
	ai, err := os.Stat(a)
	if err != nil {
		t.Fatal(err)
	}
	bi, err := os.Stat(b)
	if err != nil {
		t.Fatal(err)
	}
	if os.SameFile(ai, bi) {
		t.Errorf("%v and %v are still the same file", a, b)
	}
}

// isCaseInsensitive reports whether the file system holding dir ignores case
// in names.
func isCaseInsensitive(t *testing.T, dir string) bool {
	t.Helper()
	writeTestFile(t, filepath.Join(dir, "case-probe"), "")
	defer os.Remove(filepath.Join(dir, "case-probe"))
	_, err := os.Stat(filepath.Join(dir, "CASE-PROBE"))
	return err == nil
}

// Copy replaces a destination which is a link of its own to the source file,
// another hard link or a symbolic link, with a copy of the source: it's
// removed first (RFC 4918 section 9.8.4), and the source stays in place.
func TestLocalFileSystemCopyOntoLink(t *testing.T) {
	ctx := context.Background()

	for _, tc := range []struct {
		name string
		dst  string
	}{
		{"hard link in the same directory", "hard.txt"},
		{"hard link in another directory", "sub/hard.txt"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			src, dst := filepath.Join(dir, "orig.txt"), filepath.Join(dir, tc.dst)
			writeTestFile(t, src, "precious")
			if err := os.MkdirAll(filepath.Join(dir, "sub"), 0755); err != nil {
				t.Fatal(err)
			}
			if err := os.Link(src, dst); err != nil {
				t.Fatal(err)
			}
			fs := webdav.LocalFileSystem(dir)

			// Without overwriting, the destination exists
			_, err := fs.Copy(ctx, "/orig.txt", "/"+tc.dst, &webdav.CopyOptions{NoOverwrite: true})
			checkHTTPErrorCode(t, err, http.StatusPreconditionFailed)

			created, err := fs.Copy(ctx, "/orig.txt", "/"+tc.dst, &webdav.CopyOptions{})
			if err != nil {
				t.Fatalf("Copy() = %v", err)
			}
			if created {
				t.Errorf("Copy() created the destination, want it overwritten")
			}
			checkTestFile(t, src, "precious")
			checkTestFile(t, dst, "precious")
			checkNotSameFile(t, src, dst)
		})
	}

	t.Run("symbolic link to the source", func(t *testing.T) {
		dir := t.TempDir()
		src, dst := filepath.Join(dir, "orig.txt"), filepath.Join(dir, "link.txt")
		writeTestFile(t, src, "precious")
		writeTestSymlink(t, dst, "orig.txt")
		fs := webdav.LocalFileSystem(dir)

		if _, err := fs.Copy(ctx, "/orig.txt", "/link.txt", &webdav.CopyOptions{}); err != nil {
			t.Fatalf("Copy() = %v", err)
		}
		checkTestFile(t, src, "precious")
		checkTestFile(t, dst, "precious")
		if fi, err := os.Lstat(dst); err != nil || !fi.Mode().IsRegular() {
			t.Errorf("destination is %v (%v), want a regular file", fi.Mode(), err)
		}
	})

	t.Run("symbolic link to the source collection", func(t *testing.T) {
		dir := t.TempDir()
		writeTestFile(t, filepath.Join(dir, "col", "a.txt"), "precious")
		writeTestSymlink(t, filepath.Join(dir, "col-link"), "col")
		fs := webdav.LocalFileSystem(dir)

		if _, err := fs.Copy(ctx, "/col", "/col-link", &webdav.CopyOptions{}); err != nil {
			t.Fatalf("Copy() = %v", err)
		}
		checkTestFile(t, filepath.Join(dir, "col", "a.txt"), "precious")
		checkTestFile(t, filepath.Join(dir, "col-link", "a.txt"), "precious")
		if fi, err := os.Lstat(filepath.Join(dir, "col-link")); err != nil || !fi.IsDir() {
			t.Errorf("destination is %v (%v), want a directory", fi.Mode(), err)
		}
	})
}

// Copy still refuses with 403 Forbidden to copy a resource onto itself (RFC
// 4918 section 9.8.5): the destination can't be removed without removing the
// source.
func TestLocalFileSystemCopyOntoItself(t *testing.T) {
	ctx := context.Background()

	for _, tc := range []struct {
		name     string
		src, dst string
		// caseInsensitive is set if the test needs a case-insensitive
		// file system
		caseInsensitive bool
	}{
		{name: "source is a symbolic link to the destination", src: "/link.txt", dst: "/orig.txt"},
		{name: "source is a symbolic link to the destination collection", src: "/dir-link", dst: "/dir"},
		{name: "through a symbolic link to the directory", src: "/dir-link/f.txt", dst: "/dir/f.txt"},
		{name: "through a symbolic link to the directory, reversed", src: "/dir/f.txt", dst: "/dir-link/f.txt"},
		// The file has another hard link, hard.txt, in the same directory:
		// isSameDirEntry would count two entries for it, and take the
		// alias for the other one
		{name: "case-only alias", src: "/orig.txt", dst: "/ORIG.txt", caseInsensitive: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			if tc.caseInsensitive && !isCaseInsensitive(t, dir) {
				t.Skip("case-sensitive file system")
			}
			writeTestFile(t, filepath.Join(dir, "orig.txt"), "precious")
			if err := os.Link(filepath.Join(dir, "orig.txt"), filepath.Join(dir, "hard.txt")); err != nil {
				t.Fatal(err)
			}
			writeTestSymlink(t, filepath.Join(dir, "link.txt"), "orig.txt")
			writeTestFile(t, filepath.Join(dir, "dir", "f.txt"), "precious")
			writeTestSymlink(t, filepath.Join(dir, "dir-link"), "dir")
			fs := webdav.LocalFileSystem(dir)

			_, err := fs.Copy(ctx, tc.src, tc.dst, &webdav.CopyOptions{})
			checkHTTPErrorCode(t, err, http.StatusForbidden)
			for _, name := range []string{"orig.txt", "hard.txt", "link.txt", "dir/f.txt", "dir-link/f.txt"} {
				checkTestFile(t, filepath.Join(dir, name), "precious")
			}
			entries, err := os.ReadDir(dir)
			if err != nil {
				t.Fatal(err)
			}
			var names []string
			for _, e := range entries {
				names = append(names, e.Name())
			}
			if want := []string{"dir", "dir-link", "hard.txt", "link.txt", "orig.txt"}; !slices.Equal(names, want) {
				t.Errorf("directory holds %v, want %v", names, want)
			}
		})
	}
}
