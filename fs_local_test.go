package webdav_test

import (
	"context"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"testing"

	"github.com/emersion/go-webdav"
)

func writeTestFile(t *testing.T, name, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(name), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(name, []byte(content), 0644); err != nil {
		t.Fatal(err)
	}
}

func checkTestFile(t *testing.T, name, want string) {
	t.Helper()
	b, err := os.ReadFile(name)
	if err != nil {
		t.Errorf("failed to read %v: %v", name, err)
	} else if string(b) != want {
		t.Errorf("%v contains %q, want %q", name, b, want)
	}
}

func checkHTTPErrorCode(t *testing.T, err error, code int) {
	t.Helper()
	var httpErr *webdav.HTTPError
	if !errors.As(err, &httpErr) || httpErr.Code != code {
		t.Errorf("got error %v, want HTTP %d", err, code)
	}
}

func TestLocalFileSystemCopyCollection(t *testing.T) {
	dir := t.TempDir()
	writeTestFile(t, filepath.Join(dir, "src", "a.txt"), "a")
	writeTestFile(t, filepath.Join(dir, "src", "sub", "b.txt"), "b")
	fs := webdav.LocalFileSystem(dir)
	ctx := context.Background()

	created, err := fs.Copy(ctx, "/src", "/dst", &webdav.CopyOptions{})
	if err != nil {
		t.Fatalf("Copy() = %v", err)
	}
	if !created {
		t.Errorf("Copy() created = false, want true")
	}
	checkTestFile(t, filepath.Join(dir, "dst", "a.txt"), "a")
	checkTestFile(t, filepath.Join(dir, "dst", "sub", "b.txt"), "b")

	// Overwrite the existing destination
	writeTestFile(t, filepath.Join(dir, "dst", "stale.txt"), "stale")
	created, err = fs.Copy(ctx, "/src", "/dst", &webdav.CopyOptions{})
	if err != nil {
		t.Fatalf("Copy() = %v", err)
	}
	if created {
		t.Errorf("Copy() created = true, want false")
	}
	checkTestFile(t, filepath.Join(dir, "dst", "sub", "b.txt"), "b")
	if _, err := os.Stat(filepath.Join(dir, "dst", "stale.txt")); !os.IsNotExist(err) {
		t.Errorf("overwritten destination still has stale member (stat error: %v)", err)
	}

	// "Depth: 0" copies the collection without its members
	if _, err := fs.Copy(ctx, "/src", "/shallow", &webdav.CopyOptions{NoRecursive: true}); err != nil {
		t.Fatalf("Copy() = %v", err)
	}
	if entries, err := os.ReadDir(filepath.Join(dir, "shallow")); err != nil {
		t.Errorf("failed to read shallow copy: %v", err)
	} else if len(entries) != 0 {
		t.Errorf("shallow copy has %d members, want 0", len(entries))
	}

	// A collection can't be copied into itself
	_, err = fs.Copy(ctx, "/src", "/src/sub/copy", &webdav.CopyOptions{})
	checkHTTPErrorCode(t, err, http.StatusForbidden)
	if _, err := os.Stat(filepath.Join(dir, "src", "sub", "copy")); !os.IsNotExist(err) {
		t.Errorf("copying a collection into itself created the destination (stat error: %v)", err)
	}
}
