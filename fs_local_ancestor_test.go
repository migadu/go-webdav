package webdav_test

import (
	"context"
	"net/http"
	"path/filepath"
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
