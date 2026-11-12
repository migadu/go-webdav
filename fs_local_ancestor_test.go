package webdav_test

import (
	"context"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"testing"
	"time"

	"github.com/migadu/go-webdav"
)

// Copy and Move refuse to overwrite a collection containing the source: the
// destination is removed first (RFC 4918 sections 9.8.4 and 9.9.3), which
// would remove the source with it.
func TestLocalFileSystemOverwriteSourceAncestor(t *testing.T) {
	ctx := context.Background()
	ops := []struct {
		name string
		do   func(fs webdav.LocalFileSystem, src, dst string, noOverwrite bool) error
	}{
		{"Copy", func(fs webdav.LocalFileSystem, src, dst string, noOverwrite bool) error {
			_, err := fs.Copy(ctx, src, dst, &webdav.CopyOptions{NoOverwrite: noOverwrite})
			return err
		}},
		{"Move", func(fs webdav.LocalFileSystem, src, dst string, noOverwrite bool) error {
			_, err := fs.Move(ctx, src, dst, &webdav.MoveOptions{NoOverwrite: noOverwrite})
			return err
		}},
	}
	for _, op := range ops {
		for _, tc := range []struct{ src, dst string }{
			{"/a/b", "/a"},
			{"/a/b/c.txt", "/a"},
			{"/a/b/c.txt", "/a/b"},
		} {
			t.Run(op.name+" "+tc.src+" to "+tc.dst, func(t *testing.T) {
				dir := t.TempDir()
				writeTestFile(t, filepath.Join(dir, "a", "b", "c.txt"), "precious")
				fs := webdav.LocalFileSystem(dir)

				checkHTTPErrorCode(t, op.do(fs, tc.src, tc.dst, false), http.StatusForbidden)
				checkTestFile(t, filepath.Join(dir, "a", "b", "c.txt"), "precious")

				checkHTTPErrorCode(t, op.do(fs, tc.src, tc.dst, true), http.StatusPreconditionFailed)
				checkTestFile(t, filepath.Join(dir, "a", "b", "c.txt"), "precious")
			})
		}
	}
}

// The same, for a destination which is a collection containing the source
// under another name: through a symbolic link to a directory, or an alias on
// a case-insensitive file system, or for a source which is a symbolic link to
// a file inside the destination. A destination which is a symbolic link to
// such a collection is only a link, removed without the source.
func TestLocalFileSystemOverwriteSourceAncestorThroughAlias(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symbolic links")
	}
	ctx := context.Background()
	ops := []struct {
		name string
		do   func(fs webdav.LocalFileSystem, src, dst string) error
	}{
		{"Copy", func(fs webdav.LocalFileSystem, src, dst string) error {
			_, err := fs.Copy(ctx, src, dst, &webdav.CopyOptions{})
			return err
		}},
		{"Move", func(fs webdav.LocalFileSystem, src, dst string) error {
			_, err := fs.Move(ctx, src, dst, &webdav.MoveOptions{})
			return err
		}},
	}
	for _, op := range ops {
		for _, tc := range []struct {
			src, dst string
			// caseInsensitive is set if the test needs a
			// case-insensitive file system
			caseInsensitive bool
		}{
			{src: "/link/b/c.txt", dst: "/a"},
			{src: "/link/b/c.txt", dst: "/a/b"},
			{src: "/link/b", dst: "/a"},
			{src: "/a/b/c.txt", dst: "/A", caseInsensitive: true},
			{src: "/a/b/c.txt", dst: "/A/B", caseInsensitive: true},
			// Removing LINK would remove link, through which the source
			// is reached
			{src: "/link/b/c.txt", dst: "/LINK", caseInsensitive: true},
			// The source is a symbolic link to a file or collection
			// inside the destination, possibly through another link:
			// removing the destination would remove what it links to
			{src: "/flink", dst: "/a"},
			{src: "/flink", dst: "/a/b"},
			{src: "/flink2", dst: "/a"},
			{src: "/blink", dst: "/a"},
		} {
			t.Run(op.name+" "+tc.src+" to "+tc.dst, func(t *testing.T) {
				dir := t.TempDir()
				if tc.caseInsensitive && !isCaseInsensitive(t, dir) {
					t.Skip("case-sensitive file system")
				}
				writeTestFile(t, filepath.Join(dir, "a", "b", "c.txt"), "precious")
				writeTestSymlink(t, filepath.Join(dir, "link"), "a")
				writeTestSymlink(t, filepath.Join(dir, "flink"), "a/b/c.txt")
				writeTestSymlink(t, filepath.Join(dir, "flink2"), "flink")
				writeTestSymlink(t, filepath.Join(dir, "blink"), "a/b")
				fs := webdav.LocalFileSystem(dir)

				checkHTTPErrorCode(t, op.do(fs, tc.src, tc.dst), http.StatusForbidden)
				checkTestFile(t, filepath.Join(dir, "a", "b", "c.txt"), "precious")
				for _, name := range []string{"link", "flink", "flink2", "blink"} {
					if fi, err := os.Lstat(filepath.Join(dir, name)); err != nil || fi.Mode()&os.ModeSymlink == 0 {
						t.Errorf("%v is gone (%v)", name, err)
					}
				}
			})
		}

		t.Run(op.name+" onto a symbolic link to a collection containing the source", func(t *testing.T) {
			dir := t.TempDir()
			writeTestFile(t, filepath.Join(dir, "a", "b", "c.txt"), "precious")
			writeTestSymlink(t, filepath.Join(dir, "link"), "a")
			fs := webdav.LocalFileSystem(dir)

			if err := op.do(fs, "/a/b/c.txt", "/link"); err != nil {
				t.Fatalf("%v() = %v", op.name, err)
			}
			checkTestFile(t, filepath.Join(dir, "link"), "precious")
			if fi, err := os.Lstat(filepath.Join(dir, "link")); err != nil || !fi.Mode().IsRegular() {
				t.Errorf("destination isn't a regular file (%v)", err)
			}
			if op.name == "Copy" {
				checkTestFile(t, filepath.Join(dir, "a", "b", "c.txt"), "precious")
			}
		})
	}
}

// The same, for a source which resolves outside of the destination, but
// through a directory entry inside it: removing the destination removes that
// entry, through which the source is reached. Here a/x/mid is a symbolic link
// to real, outside of a.
func TestLocalFileSystemOverwriteSourcePathThroughDestination(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symbolic links")
	}
	ctx := context.Background()
	ops := []struct {
		name string
		do   func(fs webdav.LocalFileSystem, src, dst string) error
	}{
		{"Copy", func(fs webdav.LocalFileSystem, src, dst string) error {
			_, err := fs.Copy(ctx, src, dst, &webdav.CopyOptions{})
			return err
		}},
		{"Move", func(fs webdav.LocalFileSystem, src, dst string) error {
			_, err := fs.Move(ctx, src, dst, &webdav.MoveOptions{})
			return err
		}},
	}
	newTestDir := func(t *testing.T) string {
		dir := t.TempDir()
		writeTestFile(t, filepath.Join(dir, "real", "file.txt"), "precious")
		writeTestFile(t, filepath.Join(dir, "a", "x", "other.txt"), "other")
		writeTestSymlink(t, filepath.Join(dir, "a", "x", "mid"), "../../real")
		// The link to the file passes through a/x/mid
		writeTestSymlink(t, filepath.Join(dir, "ilink"), "a/x/mid/file.txt")
		// The source's path passes through dlink, a link to a/x/mid
		writeTestSymlink(t, filepath.Join(dir, "dlink"), "a/x/mid")
		// The link passes through a, if only to leave it with ".."
		writeTestSymlink(t, filepath.Join(dir, "ulink"), "a/../real/file.txt")
		writeTestSymlink(t, filepath.Join(dir, "rlink"), "real/file.txt")
		return dir
	}
	for _, op := range ops {
		for _, src := range []string{"/ilink", "/dlink/file.txt", "/ulink"} {
			t.Run(op.name+" "+src+" to /a", func(t *testing.T) {
				dir := newTestDir(t)
				fs := webdav.LocalFileSystem(dir)

				checkHTTPErrorCode(t, op.do(fs, src, "/a"), http.StatusForbidden)
				checkTestFile(t, filepath.Join(dir, "real", "file.txt"), "precious")
				checkTestFile(t, filepath.Join(dir, "a", "x", "other.txt"), "other")
				for _, name := range []string{"a/x/mid", "ilink", "dlink", "ulink"} {
					if fi, err := os.Lstat(filepath.Join(dir, name)); err != nil || fi.Mode()&os.ModeSymlink == 0 {
						t.Errorf("%v is gone (%v)", name, err)
					}
				}
				checkTestFile(t, filepath.Join(dir, filepath.FromSlash(src)), "precious")
			})
		}

		// A link which doesn't pass through the destination is copied
		// or moved over it
		t.Run(op.name+" /rlink to /a", func(t *testing.T) {
			dir := newTestDir(t)
			fs := webdav.LocalFileSystem(dir)

			if err := op.do(fs, "/rlink", "/a"); err != nil {
				t.Fatalf("%v() = %v", op.name, err)
			}
			checkTestFile(t, filepath.Join(dir, "real", "file.txt"), "precious")
			checkTestFile(t, filepath.Join(dir, "a"), "precious")
		})
	}
}

// returnsWithin returns the result of do, or fails the test if do doesn't
// return within a few seconds, as when copying a collection into itself
// recurses without end.
func returnsWithin(t *testing.T, do func() error) error {
	t.Helper()
	done := make(chan error, 1)
	go func() { done <- do() }()
	select {
	case err := <-done:
		return err
	case <-time.After(10 * time.Second):
		t.Fatalf("didn't return within 10s")
		return nil
	}
}

// Copy and Move refuse with 403 Forbidden to copy or move a collection into
// itself (RFC 4918 section 9.8.3) under another name: through a symbolic link
// to it or to one of its members, or an alias on a case-insensitive file
// system. An existing destination would be removed first, and a copy would
// copy itself without end.
func TestLocalFileSystemIntoItselfThroughAlias(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symbolic links")
	}
	ctx := context.Background()
	ops := []struct {
		name string
		do   func(fs webdav.LocalFileSystem, src, dst string) error
	}{
		{"Copy", func(fs webdav.LocalFileSystem, src, dst string) error {
			_, err := fs.Copy(ctx, src, dst, &webdav.CopyOptions{})
			return err
		}},
		{"Move", func(fs webdav.LocalFileSystem, src, dst string) error {
			_, err := fs.Move(ctx, src, dst, &webdav.MoveOptions{})
			return err
		}},
	}
	for _, op := range ops {
		for _, tc := range []struct {
			src, dst string
			// caseInsensitive is set if the test needs a
			// case-insensitive file system
			caseInsensitive bool
		}{
			{src: "/a", dst: "/link/new"},
			{src: "/a", dst: "/link/b"},
			// The parent of the destination, b, is in a, though no
			// prefix of its path is a
			{src: "/a", dst: "/blink/new"},
			// The source is a symbolic link to the collection
			{src: "/link", dst: "/a/new"},
			{src: "/link", dst: "/link/new"},
			{src: "/a", dst: "/A/new", caseInsensitive: true},
			{src: "/a", dst: "/A/b", caseInsensitive: true},
		} {
			t.Run(op.name+" "+tc.src+" to "+tc.dst, func(t *testing.T) {
				dir := t.TempDir()
				if tc.caseInsensitive && !isCaseInsensitive(t, dir) {
					t.Skip("case-sensitive file system")
				}
				writeTestFile(t, filepath.Join(dir, "a", "b", "c.txt"), "precious")
				writeTestSymlink(t, filepath.Join(dir, "link"), "a")
				writeTestSymlink(t, filepath.Join(dir, "blink"), "a/b")
				fs := webdav.LocalFileSystem(dir)

				err := returnsWithin(t, func() error { return op.do(fs, tc.src, tc.dst) })
				checkHTTPErrorCode(t, err, http.StatusForbidden)
				checkTestFile(t, filepath.Join(dir, "a", "b", "c.txt"), "precious")
				if got := listTestDir(t, filepath.Join(dir, "a")); !slices.Equal(got, []string{"b"}) {
					t.Errorf("a holds %v, want [b]", got)
				}
				if got := listTestDir(t, filepath.Join(dir, "a", "b")); !slices.Equal(got, []string{"c.txt"}) {
					t.Errorf("a/b holds %v, want [c.txt]", got)
				}
				if got := listTestDir(t, dir); !slices.Equal(got, []string{"a", "blink", "link"}) {
					t.Errorf("directory holds %v, want [a blink link]", got)
				}
			})
		}
	}

	// As for a destination beneath the source's own path, a "Depth: 0"
	// copy copies no members, so it's allowed
	t.Run("Copy with Depth 0", func(t *testing.T) {
		dir := t.TempDir()
		writeTestFile(t, filepath.Join(dir, "a", "b", "c.txt"), "precious")
		writeTestSymlink(t, filepath.Join(dir, "link"), "a")
		fs := webdav.LocalFileSystem(dir)

		err := returnsWithin(t, func() error {
			_, err := fs.Copy(ctx, "/a", "/link/new", &webdav.CopyOptions{NoRecursive: true})
			return err
		})
		if err != nil {
			t.Fatalf("Copy() = %v", err)
		}
		if got := listTestDir(t, filepath.Join(dir, "a", "new")); len(got) != 0 {
			t.Errorf("a/new holds %v, want nothing", got)
		}
		checkTestFile(t, filepath.Join(dir, "a", "b", "c.txt"), "precious")
	})
}
