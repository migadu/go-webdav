package webdav_test

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"slices"
	"strings"
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

func writeTestSymlink(t *testing.T, name, target string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(name), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, name); err != nil {
		t.Fatal(err)
	}
}

func TestLocalFileSystemCopySymlinks(t *testing.T) {
	dir := t.TempDir()
	writeTestFile(t, filepath.Join(dir, "file.txt"), "hello")
	writeTestFile(t, filepath.Join(dir, "src", "a.txt"), "a")
	if err := os.Chmod(filepath.Join(dir, "src", "a.txt"), 0600); err != nil {
		t.Fatal(err)
	}
	writeTestFile(t, filepath.Join(dir, "src", "sub", "b.txt"), "b")
	links := map[string]string{
		"a-link.txt":      "a.txt",
		"sub-link":        "sub",
		"parent-link.txt": filepath.Join("..", "file.txt"),
	}
	for link, target := range links {
		writeTestSymlink(t, filepath.Join(dir, "src", link), target)
	}
	fs := webdav.LocalFileSystem(dir)
	ctx := context.Background()

	// Symbolic links are copied as links, not as the files they point to
	if _, err := fs.Copy(ctx, "/src", "/dst", &webdav.CopyOptions{}); err != nil {
		t.Fatalf("Copy() = %v", err)
	}
	for link, want := range links {
		name := filepath.Join(dir, "dst", link)
		if target, err := os.Readlink(name); err != nil || target != want {
			t.Errorf("Readlink(%v) = %q, %v, want %q", name, target, err, want)
		}
	}
	checkTestFile(t, filepath.Join(dir, "dst", "a-link.txt"), "a")
	if fi, err := os.Stat(filepath.Join(dir, "dst", "a-link.txt")); err != nil {
		t.Error(err)
	} else if runtime.GOOS != "windows" && fi.Mode().Perm() != 0600 {
		t.Errorf("copied link points to a file with permissions %v, want %v", fi.Mode().Perm(), os.FileMode(0600))
	}
	checkTestFile(t, filepath.Join(dir, "dst", "sub-link", "b.txt"), "b")
	checkTestFile(t, filepath.Join(dir, "dst", "parent-link.txt"), "hello")

	// Links which wouldn't point inside the root once copied are refused
	writeTestSymlink(t, filepath.Join(dir, "abs", "link.txt"), filepath.Join(dir, "file.txt"))
	writeTestSymlink(t, filepath.Join(dir, "deep", "sub", "link.txt"), filepath.Join("..", "..", "file.txt"))
	for _, tc := range []struct{ src, dst string }{
		{"/abs", "/abs-copy"},
		{"/deep/sub", "/shallow"},
	} {
		_, err := fs.Copy(ctx, tc.src, tc.dst, &webdav.CopyOptions{})
		checkHTTPErrorCode(t, err, http.StatusForbidden)
		checkNotExist(t, filepath.Join(dir, tc.dst, "link.txt"))
	}
}

func TestLocalFileSystemCopyReadOnly(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("directory permissions aren't enforced on Windows")
	}
	if os.Geteuid() == 0 {
		t.Skip("directory permissions aren't enforced for root")
	}
	dir := t.TempDir()
	t.Cleanup(func() {
		// Let t.TempDir remove the directories
		for _, name := range []string{"src", "src/sub", "dst", "dst/sub", "shallow"} {
			os.Chmod(filepath.Join(dir, name), 0755)
		}
	})
	writeTestFile(t, filepath.Join(dir, "src", "a.txt"), "a")
	writeTestFile(t, filepath.Join(dir, "src", "sub", "b.txt"), "b")
	if err := os.Chmod(filepath.Join(dir, "src", "sub"), 0500); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(filepath.Join(dir, "src"), 0555); err != nil {
		t.Fatal(err)
	}
	fs := webdav.LocalFileSystem(dir)
	ctx := context.Background()

	checkPerm := func(name string, want os.FileMode) {
		t.Helper()
		if fi, err := os.Stat(filepath.Join(dir, name)); err != nil {
			t.Error(err)
		} else if perm := fi.Mode().Perm(); perm != want {
			t.Errorf("%v has permissions %v, want %v", name, perm, want)
		}
	}

	if _, err := fs.Copy(ctx, "/src", "/dst", &webdav.CopyOptions{}); err != nil {
		t.Fatalf("Copy() = %v", err)
	}
	checkTestFile(t, filepath.Join(dir, "dst", "a.txt"), "a")
	checkTestFile(t, filepath.Join(dir, "dst", "sub", "b.txt"), "b")
	checkPerm("dst", 0555)
	checkPerm("dst/sub", 0500)

	if _, err := fs.Copy(ctx, "/src", "/shallow", &webdav.CopyOptions{NoRecursive: true}); err != nil {
		t.Fatalf("Copy() = %v", err)
	}
	checkPerm("shallow", 0555)
}

// newSymlinkTestDir creates a directory to be served by LocalFileSystem, and a
// sibling directory outside of it holding a secret file. The served directory
// contains symbolic links pointing to the outside directory.
func newSymlinkTestDir(t *testing.T) (dir, outside string) {
	t.Helper()
	base := t.TempDir()
	dir = filepath.Join(base, "root")
	outside = filepath.Join(base, "outside")
	writeTestFile(t, filepath.Join(outside, "secret.txt"), "secret")
	writeTestFile(t, filepath.Join(dir, "file.txt"), "hello")
	for link, target := range map[string]string{
		"secret-link.txt":  filepath.Join(outside, "secret.txt"),
		"outside-link":     outside,
		"outside-rel-link": filepath.Join("..", "outside"),
	} {
		if err := os.Symlink(target, filepath.Join(dir, link)); err != nil {
			t.Fatal(err)
		}
	}
	return dir, outside
}

func checkNotExist(t *testing.T, name string) {
	t.Helper()
	if _, err := os.Lstat(name); !os.IsNotExist(err) {
		t.Errorf("%v exists (lstat error: %v)", name, err)
	}
}

func TestLocalFileSystemSymlinkEscape(t *testing.T) {
	ctx := context.Background()
	escapingNames := []string{
		"/secret-link.txt",
		"/outside-link/secret.txt",
		"/outside-rel-link/secret.txt",
	}

	t.Run("Stat", func(t *testing.T) {
		dir, _ := newSymlinkTestDir(t)
		fs := webdav.LocalFileSystem(dir)
		for _, name := range escapingNames {
			fi, err := fs.Stat(ctx, name)
			if err == nil {
				t.Errorf("Stat(%q) = %+v, want an error", name, fi)
				continue
			}
			checkHTTPErrorCode(t, err, http.StatusForbidden)
		}
	})

	t.Run("Open", func(t *testing.T) {
		dir, _ := newSymlinkTestDir(t)
		fs := webdav.LocalFileSystem(dir)
		for _, name := range escapingNames {
			f, err := fs.Open(ctx, name)
			if err == nil {
				b, _ := io.ReadAll(f)
				f.Close()
				t.Errorf("Open(%q) returned %q from outside the root, want an error", name, b)
			}
		}
	})

	t.Run("ReadDir", func(t *testing.T) {
		dir, _ := newSymlinkTestDir(t)
		fs := webdav.LocalFileSystem(dir)
		for _, name := range []string{"/outside-link", "/outside-rel-link/"} {
			l, err := fs.ReadDir(ctx, name, true)
			if err == nil {
				t.Errorf("ReadDir(%q) = %+v, want an error", name, l)
			}
		}
	})

	t.Run("Create", func(t *testing.T) {
		dir, outside := newSymlinkTestDir(t)
		fs := webdav.LocalFileSystem(dir)
		for _, name := range []string{"/outside-link/new.txt", "/outside-rel-link/new.txt"} {
			body := io.NopCloser(strings.NewReader("pwned"))
			if _, _, err := fs.Create(ctx, name, body, &webdav.CreateOptions{}); err == nil {
				t.Errorf("Create(%q) succeeded, want an error", name)
			}
		}
		checkNotExist(t, filepath.Join(outside, "new.txt"))

		body := io.NopCloser(strings.NewReader("pwned"))
		if _, _, err := fs.Create(ctx, "/secret-link.txt", body, &webdav.CreateOptions{}); err == nil {
			t.Errorf("Create(%q) succeeded, want an error", "/secret-link.txt")
		}
		checkTestFile(t, filepath.Join(outside, "secret.txt"), "secret")
	})

	t.Run("Mkdir", func(t *testing.T) {
		dir, outside := newSymlinkTestDir(t)
		fs := webdav.LocalFileSystem(dir)
		if err := fs.Mkdir(ctx, "/outside-link/newdir"); err == nil {
			t.Errorf("Mkdir() succeeded, want an error")
		}
		checkNotExist(t, filepath.Join(outside, "newdir"))
	})

	t.Run("RemoveAll", func(t *testing.T) {
		dir, outside := newSymlinkTestDir(t)
		fs := webdav.LocalFileSystem(dir)
		for _, name := range []string{"/outside-link/secret.txt", "/outside-rel-link/secret.txt"} {
			if err := fs.RemoveAll(ctx, name, &webdav.RemoveAllOptions{}); err == nil {
				t.Errorf("RemoveAll(%q) succeeded, want an error", name)
			}
		}
		// Whatever happens to the links themselves, their targets must stay
		fs.RemoveAll(ctx, "/outside-link", &webdav.RemoveAllOptions{})
		fs.RemoveAll(ctx, "/secret-link.txt", &webdav.RemoveAllOptions{})
		checkTestFile(t, filepath.Join(outside, "secret.txt"), "secret")
	})

	t.Run("Copy", func(t *testing.T) {
		dir, outside := newSymlinkTestDir(t)
		fs := webdav.LocalFileSystem(dir)
		for _, src := range append(escapingNames, "/outside-link") {
			if _, err := fs.Copy(ctx, src, "/copy", &webdav.CopyOptions{}); err == nil {
				t.Errorf("Copy(%q, %q) succeeded, want an error", src, "/copy")
			}
			checkNotExist(t, filepath.Join(dir, "copy"))
			if err := os.RemoveAll(filepath.Join(dir, "copy")); err != nil {
				t.Fatal(err)
			}
		}

		for _, dst := range []string{"/outside-link/copy.txt", "/outside-link/secret.txt", "/secret-link.txt"} {
			if _, err := fs.Copy(ctx, "/file.txt", dst, &webdav.CopyOptions{}); err == nil {
				t.Errorf("Copy(%q, %q) succeeded, want an error", "/file.txt", dst)
			}
		}
		checkNotExist(t, filepath.Join(outside, "copy.txt"))
		checkTestFile(t, filepath.Join(outside, "secret.txt"), "secret")
	})

	t.Run("Move", func(t *testing.T) {
		dir, outside := newSymlinkTestDir(t)
		fs := webdav.LocalFileSystem(dir)
		for _, dst := range []string{"/outside-link/moved.txt", "/outside-link/secret.txt", "/outside-rel-link/secret.txt"} {
			if _, err := fs.Move(ctx, "/file.txt", dst, &webdav.MoveOptions{}); err == nil {
				t.Errorf("Move(%q, %q) succeeded, want an error", "/file.txt", dst)
			}
		}
		checkNotExist(t, filepath.Join(outside, "moved.txt"))
		checkTestFile(t, filepath.Join(outside, "secret.txt"), "secret")
		checkTestFile(t, filepath.Join(dir, "file.txt"), "hello")

		if _, err := fs.Move(ctx, "/outside-link/secret.txt", "/stolen.txt", &webdav.MoveOptions{}); err == nil {
			t.Errorf("Move(%q, %q) succeeded, want an error", "/outside-link/secret.txt", "/stolen.txt")
		}
		checkNotExist(t, filepath.Join(dir, "stolen.txt"))
		checkTestFile(t, filepath.Join(outside, "secret.txt"), "secret")
	})
}

func TestHandlerSymlinkEscape(t *testing.T) {
	dir, outside := newSymlinkTestDir(t)
	h := &webdav.Handler{FileSystem: webdav.LocalFileSystem(dir)}

	for _, tc := range []struct {
		method, path string
		body         string
		want         int
	}{
		{http.MethodGet, "/secret-link.txt", "", http.StatusForbidden},
		{http.MethodGet, "/outside-rel-link/secret.txt", "", http.StatusForbidden},
		{http.MethodPut, "/outside-link/new.txt", "pwned", http.StatusForbidden},
		{http.MethodDelete, "/outside-link/secret.txt", "", http.StatusForbidden},
		// Listing a collection containing escaping links still works
		{"PROPFIND", "/", "", http.StatusMultiStatus},
	} {
		req := httptest.NewRequest(tc.method, tc.path, strings.NewReader(tc.body))
		req.Header.Set("Depth", "1")
		w := httptest.NewRecorder()
		h.ServeHTTP(w, req)
		if w.Code != tc.want {
			t.Errorf("%v %v: status = %d, want %d; body:\n%s", tc.method, tc.path, w.Code, tc.want, w.Body)
		}
		if w.Body.String() == "secret" || strings.Contains(w.Body.String(), outside) {
			t.Errorf("%v %v: response leaks data from outside the root:\n%s", tc.method, tc.path, w.Body)
		}
	}
	checkNotExist(t, filepath.Join(outside, "new.txt"))
	checkTestFile(t, filepath.Join(outside, "secret.txt"), "secret")
}

func TestLocalFileSystemRemoveRoot(t *testing.T) {
	dir := t.TempDir()
	writeTestFile(t, filepath.Join(dir, "file.txt"), "hello")
	fs := webdav.LocalFileSystem(dir)

	err := fs.RemoveAll(context.Background(), "/", &webdav.RemoveAllOptions{})
	checkHTTPErrorCode(t, err, http.StatusForbidden)
	checkTestFile(t, filepath.Join(dir, "file.txt"), "hello")
}

func TestLocalFileSystem(t *testing.T) {
	dir := t.TempDir()
	writeTestFile(t, filepath.Join(dir, "file.txt"), "hello")
	writeTestFile(t, filepath.Join(dir, "dir", "a.txt"), "a")
	writeTestFile(t, filepath.Join(dir, "dir", "sub", "b.txt"), "b")
	// Symbolic links which stay inside the root keep working
	if err := os.Symlink("file.txt", filepath.Join(dir, "link.txt")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("dir", filepath.Join(dir, "dir-link")); err != nil {
		t.Fatal(err)
	}
	fs := webdav.LocalFileSystem(dir)
	ctx := context.Background()

	t.Run("Stat", func(t *testing.T) {
		osInfo, err := os.Stat(filepath.Join(dir, "file.txt"))
		if err != nil {
			t.Fatal(err)
		}
		fi, err := fs.Stat(ctx, "/file.txt")
		if err != nil {
			t.Fatalf("Stat() = %v", err)
		}
		want := webdav.FileInfo{
			Path:     "/file.txt",
			Size:     5,
			ModTime:  osInfo.ModTime(),
			MIMEType: "text/plain; charset=utf-8",
			ETag:     fmt.Sprintf("%x%x", osInfo.ModTime().UnixNano(), osInfo.Size()),
		}
		if *fi != want {
			t.Errorf("Stat() = %+v, want %+v", *fi, want)
		}

		if fi, err := fs.Stat(ctx, "/dir/"); err != nil {
			t.Errorf("Stat() = %v", err)
		} else if !fi.IsDir || fi.Path != "/dir/" {
			t.Errorf("Stat() = %+v, want collection at /dir/", *fi)
		}
		if fi, err := fs.Stat(ctx, "/"); err != nil {
			t.Errorf("Stat() = %v", err)
		} else if !fi.IsDir {
			t.Errorf("Stat(%q) = %+v, want a collection", "/", *fi)
		}
		if fi, err := fs.Stat(ctx, "/dir-link/sub/b.txt"); err != nil {
			t.Errorf("Stat() = %v", err)
		} else if fi.Size != 1 {
			t.Errorf("Stat() = %+v, want size 1", *fi)
		}
		if _, err := fs.Stat(ctx, "/../file.txt"); err != nil {
			t.Errorf("Stat(%q) = %v, want the path to be resolved inside the root", "/../file.txt", err)
		}

		_, err = fs.Stat(ctx, "/missing.txt")
		checkHTTPErrorCode(t, err, http.StatusNotFound)
	})

	t.Run("Open", func(t *testing.T) {
		for _, name := range []string{"/file.txt", "/link.txt", "/dir/../file.txt"} {
			f, err := fs.Open(ctx, name)
			if err != nil {
				t.Errorf("Open(%q) = %v", name, err)
				continue
			}
			if _, ok := f.(io.ReadSeeker); !ok {
				t.Errorf("Open(%q) didn't return an io.ReadSeeker", name)
			}
			b, err := io.ReadAll(f)
			f.Close()
			if err != nil || string(b) != "hello" {
				t.Errorf("Open(%q) read %q, %v, want %q", name, b, err, "hello")
			}
		}

		if _, err := fs.Open(ctx, "/missing.txt"); !errors.Is(err, os.ErrNotExist) {
			t.Errorf("Open(%q) = %v, want a not-exist error", "/missing.txt", err)
		}
	})

	t.Run("ReadDir", func(t *testing.T) {
		paths := func(l []webdav.FileInfo) []string {
			var s []string
			for _, fi := range l {
				s = append(s, fmt.Sprintf("%v dir=%v", fi.Path, fi.IsDir))
			}
			return s
		}

		l, err := fs.ReadDir(ctx, "/dir", true)
		if err != nil {
			t.Fatalf("ReadDir() = %v", err)
		}
		want := []string{"/dir dir=true", "/dir/a.txt dir=false", "/dir/sub dir=true", "/dir/sub/b.txt dir=false"}
		if got := paths(l); !reflect.DeepEqual(got, want) {
			t.Errorf("ReadDir(recursive) = %v, want %v", got, want)
		}

		l, err = fs.ReadDir(ctx, "/dir/", false)
		if err != nil {
			t.Fatalf("ReadDir() = %v", err)
		}
		want = []string{"/dir dir=true", "/dir/a.txt dir=false", "/dir/sub dir=true"}
		if got := paths(l); !reflect.DeepEqual(got, want) {
			t.Errorf("ReadDir(non-recursive) = %v, want %v", got, want)
		}

		l, err = fs.ReadDir(ctx, "/", false)
		if err != nil {
			t.Fatalf("ReadDir() = %v", err)
		}
		got := paths(l)
		for _, s := range []string{"/ dir=true", "/dir dir=true", "/dir-link dir=false", "/file.txt dir=false", "/link.txt dir=false"} {
			if !slices.Contains(got, s) {
				t.Errorf("ReadDir(%q) = %v, missing %q", "/", got, s)
			}
		}
		if len(got) != 5 {
			t.Errorf("ReadDir(%q) = %v, want the root and its 4 members", "/", got)
		}
		for _, fi := range l {
			if fi.Path == "/file.txt" && (fi.Size != 5 || fi.MIMEType != "text/plain; charset=utf-8" || fi.ETag == "") {
				t.Errorf("ReadDir() returned %+v for /file.txt", fi)
			}
		}

		_, err = fs.ReadDir(ctx, "/missing", false)
		checkHTTPErrorCode(t, err, http.StatusNotFound)
	})

	t.Run("Create", func(t *testing.T) {
		body := func(s string) io.ReadCloser { return io.NopCloser(strings.NewReader(s)) }

		fi, created, err := fs.Create(ctx, "/new.txt", body("new"), &webdav.CreateOptions{})
		if err != nil {
			t.Fatalf("Create() = %v", err)
		}
		if !created || fi.Path != "/new.txt" || fi.Size != 3 || fi.ETag == "" {
			t.Errorf("Create() = %+v, %v, want a created 3-byte file", *fi, created)
		}
		checkTestFile(t, filepath.Join(dir, "new.txt"), "new")

		_, _, err = fs.Create(ctx, "/new.txt", body("nope"), &webdav.CreateOptions{IfNoneMatch: "*"})
		checkHTTPErrorCode(t, err, http.StatusPreconditionFailed)
		_, _, err = fs.Create(ctx, "/new.txt", body("nope"), &webdav.CreateOptions{IfMatch: `"bogus"`})
		checkHTTPErrorCode(t, err, http.StatusPreconditionFailed)
		checkTestFile(t, filepath.Join(dir, "new.txt"), "new")

		ifMatch := webdav.ConditionalMatch(`"` + fi.ETag + `"`)
		fi, created, err = fs.Create(ctx, "/new.txt", body("newer"), &webdav.CreateOptions{IfMatch: ifMatch})
		if err != nil {
			t.Fatalf("Create() = %v", err)
		}
		if created || fi.Size != 5 {
			t.Errorf("Create() = %+v, %v, want an overwritten 5-byte file", *fi, created)
		}
		checkTestFile(t, filepath.Join(dir, "new.txt"), "newer")

		// Writing through a symbolic link which stays inside the root
		if _, _, err := fs.Create(ctx, "/dir-link/c.txt", body("c"), &webdav.CreateOptions{}); err != nil {
			t.Errorf("Create() = %v", err)
		}
		checkTestFile(t, filepath.Join(dir, "dir", "c.txt"), "c")

		_, _, err = fs.Create(ctx, "/missing/new.txt", body("new"), &webdav.CreateOptions{})
		checkHTTPErrorCode(t, err, http.StatusConflict)
	})

	t.Run("Mkdir", func(t *testing.T) {
		if err := fs.Mkdir(ctx, "/newdir"); err != nil {
			t.Fatalf("Mkdir() = %v", err)
		}
		if fi, err := os.Stat(filepath.Join(dir, "newdir")); err != nil || !fi.IsDir() {
			t.Errorf("Mkdir() didn't create a directory: %v", err)
		}
		checkHTTPErrorCode(t, fs.Mkdir(ctx, "/newdir"), http.StatusMethodNotAllowed)
		checkHTTPErrorCode(t, fs.Mkdir(ctx, "/missing/newdir"), http.StatusConflict)
	})

	t.Run("CopyMove", func(t *testing.T) {
		created, err := fs.Copy(ctx, "/file.txt", "/copy.txt", &webdav.CopyOptions{})
		if err != nil || !created {
			t.Fatalf("Copy() = %v, %v, want created", created, err)
		}
		checkTestFile(t, filepath.Join(dir, "copy.txt"), "hello")

		_, err = fs.Copy(ctx, "/dir/a.txt", "/copy.txt", &webdav.CopyOptions{NoOverwrite: true})
		checkHTTPErrorCode(t, err, http.StatusPreconditionFailed)
		created, err = fs.Copy(ctx, "/dir/a.txt", "/copy.txt", &webdav.CopyOptions{})
		if err != nil || created {
			t.Errorf("Copy() = %v, %v, want overwritten", created, err)
		}
		checkTestFile(t, filepath.Join(dir, "copy.txt"), "a")

		created, err = fs.Copy(ctx, "/dir", "/dir-copy", &webdav.CopyOptions{})
		if err != nil || !created {
			t.Fatalf("Copy() = %v, %v, want created", created, err)
		}
		checkTestFile(t, filepath.Join(dir, "dir-copy", "sub", "b.txt"), "b")

		_, err = fs.Copy(ctx, "/missing.txt", "/copy.txt", &webdav.CopyOptions{})
		checkHTTPErrorCode(t, err, http.StatusNotFound)

		created, err = fs.Move(ctx, "/copy.txt", "/moved.txt", &webdav.MoveOptions{})
		if err != nil || !created {
			t.Fatalf("Move() = %v, %v, want created", created, err)
		}
		checkNotExist(t, filepath.Join(dir, "copy.txt"))
		checkTestFile(t, filepath.Join(dir, "moved.txt"), "a")

		_, err = fs.Move(ctx, "/moved.txt", "/file.txt", &webdav.MoveOptions{NoOverwrite: true})
		checkHTTPErrorCode(t, err, http.StatusPreconditionFailed)
		created, err = fs.Move(ctx, "/dir-copy", "/moved.txt", &webdav.MoveOptions{})
		if err != nil || created {
			t.Errorf("Move() = %v, %v, want overwritten", created, err)
		}
		checkTestFile(t, filepath.Join(dir, "moved.txt", "a.txt"), "a")

		_, err = fs.Move(ctx, "/missing.txt", "/moved2.txt", &webdav.MoveOptions{})
		checkHTTPErrorCode(t, err, http.StatusNotFound)
	})

	t.Run("RemoveAll", func(t *testing.T) {
		writeTestFile(t, filepath.Join(dir, "remove.txt"), "remove")
		err := fs.RemoveAll(ctx, "/remove.txt", &webdav.RemoveAllOptions{IfMatch: `"bogus"`})
		checkHTTPErrorCode(t, err, http.StatusPreconditionFailed)
		checkTestFile(t, filepath.Join(dir, "remove.txt"), "remove")

		if err := fs.RemoveAll(ctx, "/remove.txt", &webdav.RemoveAllOptions{}); err != nil {
			t.Errorf("RemoveAll() = %v", err)
		}
		checkNotExist(t, filepath.Join(dir, "remove.txt"))

		if err := fs.RemoveAll(ctx, "/dir", &webdav.RemoveAllOptions{}); err != nil {
			t.Errorf("RemoveAll() = %v", err)
		}
		checkNotExist(t, filepath.Join(dir, "dir"))

		checkHTTPErrorCode(t, fs.RemoveAll(ctx, "/missing", &webdav.RemoveAllOptions{}), http.StatusNotFound)
	})
}
