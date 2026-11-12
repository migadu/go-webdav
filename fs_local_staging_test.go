package webdav_test

import (
	"context"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"sync"
	"testing"

	"github.com/emersion/go-webdav"
)

// testStagingName is named like the files LocalFileSystem.Create stages
// uploads in.
const testStagingName = ".webdav-tmp-TESTSTAGINGNAME"

func readDirPaths(t *testing.T, fs webdav.FileSystem, name string, recursive bool) []string {
	t.Helper()
	l, err := fs.ReadDir(context.Background(), name, recursive)
	if err != nil {
		t.Fatalf("ReadDir(%q, %v) = %v", name, recursive, err)
	}
	var paths []string
	for _, fi := range l {
		paths = append(paths, fi.Path)
	}
	return paths
}

// The files Create stages uploads in aren't part of the file system: they
// aren't listed or copied, can't be read, and clients can't create files
// with such names.
func TestLocalFileSystemStagingFiles(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	writeTestFile(t, filepath.Join(dir, "a.txt"), "a")
	writeTestFile(t, filepath.Join(dir, testStagingName), "partial")
	writeTestFile(t, filepath.Join(dir, "sub", "b.txt"), "b")
	writeTestFile(t, filepath.Join(dir, "sub", testStagingName), "partial")
	fs := webdav.LocalFileSystem(dir)

	t.Run("ReadDir", func(t *testing.T) {
		if got, want := readDirPaths(t, fs, "/", false), []string{"/", "/a.txt", "/sub"}; !reflect.DeepEqual(got, want) {
			t.Errorf("ReadDir(/, false) = %v, want %v", got, want)
		}
		if got, want := readDirPaths(t, fs, "/", true), []string{"/", "/a.txt", "/sub", "/sub/b.txt"}; !reflect.DeepEqual(got, want) {
			t.Errorf("ReadDir(/, true) = %v, want %v", got, want)
		}
	})

	t.Run("not found", func(t *testing.T) {
		for _, name := range []string{"/" + testStagingName, "/sub/" + testStagingName} {
			_, err := fs.Stat(ctx, name)
			checkHTTPErrorCode(t, err, http.StatusNotFound)
			_, err = fs.Open(ctx, name)
			checkHTTPErrorCode(t, err, http.StatusNotFound)
			_, err = fs.ReadDir(ctx, name, false)
			checkHTTPErrorCode(t, err, http.StatusNotFound)
			err = fs.RemoveAll(ctx, name, &webdav.RemoveAllOptions{})
			checkHTTPErrorCode(t, err, http.StatusNotFound)
			_, err = fs.Copy(ctx, name, "/copied.txt", &webdav.CopyOptions{})
			checkHTTPErrorCode(t, err, http.StatusNotFound)
			_, err = fs.Move(ctx, name, "/moved.txt", &webdav.MoveOptions{})
			checkHTTPErrorCode(t, err, http.StatusNotFound)
		}
		checkTestFile(t, filepath.Join(dir, testStagingName), "partial")
		checkTestFile(t, filepath.Join(dir, "sub", testStagingName), "partial")
		checkNotExist(t, filepath.Join(dir, "copied.txt"))
		checkNotExist(t, filepath.Join(dir, "moved.txt"))
	})

	t.Run("reserved", func(t *testing.T) {
		const name = "/.webdav-tmp-new"
		_, _, err := fs.Create(ctx, name, io.NopCloser(strings.NewReader("new")), &webdav.CreateOptions{})
		checkHTTPErrorCode(t, err, http.StatusForbidden)
		checkHTTPErrorCode(t, fs.Mkdir(ctx, name), http.StatusForbidden)
		_, err = fs.Copy(ctx, "/a.txt", name, &webdav.CopyOptions{})
		checkHTTPErrorCode(t, err, http.StatusForbidden)
		_, err = fs.Move(ctx, "/a.txt", name, &webdav.MoveOptions{})
		checkHTTPErrorCode(t, err, http.StatusForbidden)
		checkNotExist(t, filepath.Join(dir, name))
		checkTestFile(t, filepath.Join(dir, "a.txt"), "a")
	})

	t.Run("Copy", func(t *testing.T) {
		if _, err := fs.Copy(ctx, "/sub", "/sub2", &webdav.CopyOptions{}); err != nil {
			t.Fatalf("Copy(/sub, /sub2) = %v", err)
		}
		checkTestFile(t, filepath.Join(dir, "sub2", "b.txt"), "b")
		checkNotExist(t, filepath.Join(dir, "sub2", testStagingName))
	})
}

// A listing made while files are being uploaded to the directory never shows
// their staging files, and doesn't fail when they're renamed away.
func TestLocalFileSystemReadDirDuringCreate(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	writeTestFile(t, filepath.Join(dir, "f.txt"), "f")
	fs := webdav.LocalFileSystem(dir)

	done := make(chan struct{})
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		for {
			select {
			case <-done:
				return
			default:
			}
			if _, _, err := fs.Create(ctx, "/f.txt", io.NopCloser(strings.NewReader("f")), &webdav.CreateOptions{}); err != nil {
				t.Errorf("Create() = %v", err)
				return
			}
		}
	}()
	defer func() {
		close(done)
		wg.Wait()
	}()

	for i := 0; i < 1000; i++ {
		l, err := fs.ReadDir(ctx, "/", false)
		if err != nil {
			t.Fatalf("ReadDir() = %v during an upload (listing %d)", err, i)
		}
		for _, fi := range l {
			if strings.Contains(fi.Path, ".webdav-tmp-") {
				t.Fatalf("ReadDir() lists %v during an upload (listing %d)", fi.Path, i)
			}
		}
	}
}

// Overwriting a file keeps its permissions, including those the process
// umask would clear from the staging file.
func TestLocalFileSystemCreateKeepsPermissions(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX permissions")
	}
	ctx := context.Background()
	for _, perm := range []os.FileMode{0666, 0640, 0755} {
		t.Run(perm.String(), func(t *testing.T) {
			dir := t.TempDir()
			target := filepath.Join(dir, "shared.txt")
			writeTestFile(t, target, "old")
			if err := os.Chmod(target, perm); err != nil {
				t.Fatal(err)
			}
			fs := webdav.LocalFileSystem(dir)

			if _, _, err := fs.Create(ctx, "/shared.txt", io.NopCloser(strings.NewReader("new")), &webdav.CreateOptions{}); err != nil {
				t.Fatalf("Create() = %v", err)
			}
			checkTestFile(t, target, "new")
			fi, err := os.Stat(target)
			if err != nil {
				t.Fatal(err)
			}
			if got := fi.Mode().Perm(); got != perm {
				t.Errorf("permissions after overwrite = %v, want %v", got, perm)
			}
		})
	}
}

// Overwriting a file needs write access to its directory, where the upload
// is staged: without it, the request is refused with 403 Forbidden, even if
// the file itself is writable, and the file is left alone.
func TestLocalFileSystemCreateInReadOnlyDir(t *testing.T) {
	if runtime.GOOS == "windows" || os.Geteuid() == 0 {
		t.Skip("POSIX permissions, not as root")
	}
	ctx := context.Background()
	dir := t.TempDir()
	roDir := filepath.Join(dir, "ro")
	writeTestFile(t, filepath.Join(roDir, "doc.txt"), "old")
	if err := os.Chmod(roDir, 0555); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Chmod(roDir, 0755) })
	fs := webdav.LocalFileSystem(dir)

	_, _, err := fs.Create(ctx, "/ro/doc.txt", io.NopCloser(strings.NewReader("new")), &webdav.CreateOptions{})
	checkHTTPErrorCode(t, err, http.StatusForbidden)
	checkTestFile(t, filepath.Join(roDir, "doc.txt"), "old")
	if entries, err := os.ReadDir(roDir); err != nil || len(entries) != 1 {
		t.Errorf("directory holds %v (%v), want only doc.txt", entries, err)
	}
}
