package webdav_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"testing"

	"github.com/emersion/go-webdav"
)

// listTestDir returns the names of the entries of dir.
func listTestDir(t *testing.T, dir string) []string {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, e := range entries {
		names = append(names, e.Name())
	}
	return names
}

// Move refuses with 403 Forbidden to move a resource onto itself (RFC 4918
// section 9.9.4): the destination, which is removed first (RFC 4918 section
// 9.9.3), can't be removed without removing the source or what it links to,
// and renaming the source onto itself would do nothing.
func TestLocalFileSystemMoveOntoItself(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symbolic links")
	}
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
		// g.txt has another hard link, g-hard.txt, in the same directory
		{name: "through a symbolic link to the directory, with another hard link", src: "/dir-link/g.txt", dst: "/dir/g.txt"},
		{name: "dangling symbolic link through a symbolic link to the directory", src: "/dir-link/broken", dst: "/dir/broken"},
		// HARD.txt is an alias of hard.txt, but it can't be told from
		// an alias of orig.txt: renaming would do nothing
		{name: "case-only alias of another hard link", src: "/orig.txt", dst: "/HARD.txt", caseInsensitive: true},
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
			writeTestFile(t, filepath.Join(dir, "dir", "g.txt"), "precious")
			if err := os.Link(filepath.Join(dir, "dir", "g.txt"), filepath.Join(dir, "dir", "g-hard.txt")); err != nil {
				t.Fatal(err)
			}
			writeTestSymlink(t, filepath.Join(dir, "dir", "broken"), "missing")
			writeTestSymlink(t, filepath.Join(dir, "dir-link"), "dir")
			fs := webdav.LocalFileSystem(dir)

			_, err := fs.Move(ctx, tc.src, tc.dst, &webdav.MoveOptions{})
			checkHTTPErrorCode(t, err, http.StatusForbidden)
			for _, name := range []string{"orig.txt", "hard.txt", "link.txt", "dir/f.txt", "dir/g.txt", "dir/g-hard.txt"} {
				checkTestFile(t, filepath.Join(dir, name), "precious")
			}
			if want := []string{"dir", "dir-link", "hard.txt", "link.txt", "orig.txt"}; !slices.Equal(listTestDir(t, dir), want) {
				t.Errorf("directory holds %v, want %v", listTestDir(t, dir), want)
			}
			if want := []string{"broken", "f.txt", "g-hard.txt", "g.txt"}; !slices.Equal(listTestDir(t, filepath.Join(dir, "dir")), want) {
				t.Errorf("dir holds %v, want %v", listTestDir(t, filepath.Join(dir, "dir")), want)
			}
		})
	}

	// The same through the Handler
	t.Run("Handler", func(t *testing.T) {
		dir := t.TempDir()
		writeTestFile(t, filepath.Join(dir, "col", "a.txt"), "member")
		writeTestSymlink(t, filepath.Join(dir, "col-link"), "col")
		h := &webdav.Handler{FileSystem: webdav.LocalFileSystem(dir)}
		req := httptest.NewRequest("MOVE", "/col-link", nil)
		req.Header.Set("Destination", "/col")
		w := httptest.NewRecorder()
		h.ServeHTTP(w, req)
		if w.Code != http.StatusForbidden {
			t.Errorf("MOVE /col-link to /col: status = %d, want 403", w.Code)
		}
		checkTestFile(t, filepath.Join(dir, "col", "a.txt"), "member")
	})
}

// Move replaces a destination which is a link of its own to the source, a
// symbolic link or another hard link: it's removed first (RFC 4918 section
// 9.9.3), leaving the source alone.
func TestLocalFileSystemMoveOntoLink(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symbolic links")
	}
	ctx := context.Background()

	for _, tc := range []struct {
		name     string
		src, dst string
		// isDir is set if the moved source is a collection
		isDir bool
		// caseSensitive is set if the test needs a case-sensitive file
		// system
		caseSensitive bool
	}{
		{name: "hard link in the same directory", src: "/orig.txt", dst: "/hard.txt"},
		{name: "hard link in another directory", src: "/orig.txt", dst: "/sub/hard.txt"},
		{name: "hard link whose name differs in case", src: "/orig.txt", dst: "/ORIG.txt", caseSensitive: true},
		{name: "symbolic link to the source", src: "/orig.txt", dst: "/link.txt"},
		{name: "symbolic link to the source collection", src: "/col", dst: "/col-link", isDir: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			caseInsensitive := isCaseInsensitive(t, dir)
			if tc.caseSensitive && caseInsensitive {
				t.Skip("case-insensitive file system")
			}
			writeTestFile(t, filepath.Join(dir, "orig.txt"), "precious")
			writeTestFile(t, filepath.Join(dir, "col", "a.txt"), "precious")
			if err := os.MkdirAll(filepath.Join(dir, "sub"), 0755); err != nil {
				t.Fatal(err)
			}
			links := []string{"hard.txt", "sub/hard.txt"}
			if !caseInsensitive {
				links = append(links, "ORIG.txt")
			}
			for _, name := range links {
				if err := os.Link(filepath.Join(dir, "orig.txt"), filepath.Join(dir, name)); err != nil {
					t.Fatal(err)
				}
			}
			writeTestSymlink(t, filepath.Join(dir, "link.txt"), "orig.txt")
			writeTestSymlink(t, filepath.Join(dir, "col-link"), "col")
			fs := webdav.LocalFileSystem(dir)

			created, err := fs.Move(ctx, tc.src, tc.dst, &webdav.MoveOptions{})
			if err != nil {
				t.Fatalf("Move() = %v", err)
			}
			if created {
				t.Errorf("Move() created the destination, want it overwritten")
			}
			checkNotExist(t, filepath.Join(dir, filepath.FromSlash(tc.src)))
			dst := filepath.Join(dir, filepath.FromSlash(tc.dst))
			fi, err := os.Lstat(dst)
			if err != nil {
				t.Fatal(err)
			}
			if tc.isDir {
				if !fi.IsDir() {
					t.Errorf("destination is %v, want a directory", fi.Mode())
				}
				checkTestFile(t, filepath.Join(dst, "a.txt"), "precious")
			} else {
				if !fi.Mode().IsRegular() {
					t.Errorf("destination is %v, want a regular file", fi.Mode())
				}
				checkTestFile(t, dst, "precious")
			}
		})
	}
}

// On a case-insensitive file system, a case-only Move renames the source's
// entry in place: the destination is an alias of the source, which removing
// it would remove.
func TestLocalFileSystemMoveCaseOnly(t *testing.T) {
	ctx := context.Background()

	for _, tc := range []struct {
		name     string
		src, dst string
		want     []string
	}{
		{name: "file", src: "/readme.txt", dst: "/README.txt", want: []string{"README.txt", "a.txt", "b.txt", "col", "link", "target.txt"}},
		// b.txt is another hard link to a.txt
		{name: "file with another hard link", src: "/a.txt", dst: "/A.txt", want: []string{"A.txt", "b.txt", "col", "link", "readme.txt", "target.txt"}},
		{name: "collection", src: "/col", dst: "/Col", want: []string{"Col", "a.txt", "b.txt", "link", "readme.txt", "target.txt"}},
		{name: "symbolic link", src: "/link", dst: "/LINK", want: []string{"LINK", "a.txt", "b.txt", "col", "readme.txt", "target.txt"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			if !isCaseInsensitive(t, dir) {
				t.Skip("case-sensitive file system")
			}
			writeTestFile(t, filepath.Join(dir, "readme.txt"), "precious")
			writeTestFile(t, filepath.Join(dir, "a.txt"), "precious")
			if err := os.Link(filepath.Join(dir, "a.txt"), filepath.Join(dir, "b.txt")); err != nil {
				t.Fatal(err)
			}
			writeTestFile(t, filepath.Join(dir, "col", "m.txt"), "precious")
			writeTestFile(t, filepath.Join(dir, "target.txt"), "precious")
			writeTestSymlink(t, filepath.Join(dir, "link"), "target.txt")
			fs := webdav.LocalFileSystem(dir)

			// Without overwriting, the destination exists
			_, err := fs.Move(ctx, tc.src, tc.dst, &webdav.MoveOptions{NoOverwrite: true})
			checkHTTPErrorCode(t, err, http.StatusPreconditionFailed)

			created, err := fs.Move(ctx, tc.src, tc.dst, &webdav.MoveOptions{})
			if err != nil {
				t.Fatalf("Move() = %v", err)
			}
			if created {
				t.Errorf("Move() created the destination, want it overwritten")
			}
			if got := listTestDir(t, dir); !slices.Equal(got, tc.want) {
				t.Errorf("directory holds %v, want %v", got, tc.want)
			}
			for _, name := range []string{"readme.txt", "a.txt", "b.txt", "col/m.txt", "link"} {
				checkTestFile(t, filepath.Join(dir, name), "precious")
			}
		})
	}

	t.Run("dangling symbolic link", func(t *testing.T) {
		dir := t.TempDir()
		if !isCaseInsensitive(t, dir) {
			t.Skip("case-sensitive file system")
		}
		writeTestSymlink(t, filepath.Join(dir, "broken"), "missing")
		fs := webdav.LocalFileSystem(dir)

		if _, err := fs.Move(ctx, "/broken", "/BROKEN", &webdav.MoveOptions{}); err != nil {
			t.Fatalf("Move() = %v", err)
		}
		if got, want := listTestDir(t, dir), []string{"BROKEN"}; !slices.Equal(got, want) {
			t.Errorf("directory holds %v, want %v", got, want)
		}
		if target, err := os.Readlink(filepath.Join(dir, "BROKEN")); err != nil || target != "missing" {
			t.Errorf("BROKEN links to %q (%v), want %q", target, err, "missing")
		}
	})
}
