package webdav_test

import (
	"context"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/emersion/go-webdav"
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
// a case-insensitive file system. A destination which is a symbolic link to
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
		} {
			t.Run(op.name+" "+tc.src+" to "+tc.dst, func(t *testing.T) {
				dir := t.TempDir()
				if tc.caseInsensitive && !isCaseInsensitive(t, dir) {
					t.Skip("case-sensitive file system")
				}
				writeTestFile(t, filepath.Join(dir, "a", "b", "c.txt"), "precious")
				writeTestSymlink(t, filepath.Join(dir, "link"), "a")
				fs := webdav.LocalFileSystem(dir)

				checkHTTPErrorCode(t, op.do(fs, tc.src, tc.dst), http.StatusForbidden)
				checkTestFile(t, filepath.Join(dir, "a", "b", "c.txt"), "precious")
				if fi, err := os.Lstat(filepath.Join(dir, "link")); err != nil || fi.Mode()&os.ModeSymlink == 0 {
					t.Errorf("link is gone (%v)", err)
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
