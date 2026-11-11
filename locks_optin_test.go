package webdav

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const exclusiveLockInfo = `<?xml version="1.0" encoding="utf-8"?>
<D:lockinfo xmlns:D="DAV:">
  <D:lockscope><D:exclusive/></D:lockscope>
  <D:locktype><D:write/></D:locktype>
  <D:owner>alice</D:owner>
</D:lockinfo>`

// serveLockTest serves a request with the given body and header fields,
// given as name-value pairs.
func serveLockTest(h http.Handler, method, target, body string, header ...string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, target, strings.NewReader(body))
	if body != "" {
		req.Header.Set("Content-Type", "application/xml")
	}
	for i := 0; i+1 < len(header); i += 2 {
		req.Header.Set(header[i], header[i+1])
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	return w
}

// newLockTestDir creates a directory holding file.txt and an empty dir.
func newLockTestDir(t *testing.T) string {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "file.txt"), []byte("hello"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(dir, "dir"), 0755); err != nil {
		t.Fatal(err)
	}
	return dir
}

// headerList splits the comma-separated values of a header field.
func headerList(h http.Header, name string) []string {
	var l []string
	for _, v := range h.Values(name) {
		for _, c := range strings.Split(v, ",") {
			l = append(l, strings.TrimSpace(c))
		}
	}
	return l
}

func davClasses(h http.Header) []string {
	return headerList(h, "DAV")
}

func hasToken(l []string, s string) bool {
	for _, v := range l {
		if v == s {
			return true
		}
	}
	return false
}

func allowedMethods(h http.Header) []string {
	return headerList(h, "Allow")
}

// Locking is opt-in: a Handler without a LockSystem doesn't advertise DAV
// class 2, refuses LOCK and UNLOCK as unsupported methods, and ignores the
// If header field, as it did before locking was added.
func TestHandlerWithoutLockSystem(t *testing.T) {
	dir := newLockTestDir(t)
	h := &Handler{FileSystem: LocalFileSystem(dir)}

	for _, target := range []string{"/file.txt", "/dir/", "/unmapped"} {
		w := serveLockTest(h, http.MethodOptions, target, "")
		if classes := davClasses(w.Header()); hasToken(classes, "2") {
			t.Errorf("OPTIONS %v: DAV = %v, want no class 2", target, classes)
		}
		if allow := allowedMethods(w.Header()); hasToken(allow, "LOCK") || hasToken(allow, "UNLOCK") {
			t.Errorf("OPTIONS %v: Allow = %v, want neither LOCK nor UNLOCK", target, allow)
		}
	}

	for _, tc := range []struct {
		name, method, body string
		header             []string
	}{
		{"LOCK", "LOCK", exclusiveLockInfo, nil},
		{"LOCK refresh", "LOCK", "", []string{"If", "(<opaquelocktoken:foo>)"}},
		{"LOCK without If", "LOCK", "", nil},
		{"LOCK with a malformed body", "LOCK", "<lockinfo", nil},
		{"UNLOCK", "UNLOCK", "", []string{"Lock-Token", "<opaquelocktoken:foo>"}},
		{"UNLOCK without Lock-Token", "UNLOCK", "", nil},
	} {
		w := serveLockTest(h, tc.method, "/file.txt", tc.body, tc.header...)
		if w.Code != http.StatusMethodNotAllowed {
			t.Errorf("%v: status = %v, want 405", tc.name, w.Code)
		}
	}

	w := serveLockTest(h, "PROPFIND", "/file.txt", "", "Depth", "0")
	if w.Code != http.StatusMultiStatus {
		t.Fatalf("PROPFIND: status = %v, want 207", w.Code)
	}
	for _, name := range []string{"supportedlock", "lockdiscovery"} {
		if strings.Contains(w.Body.String(), name) {
			t.Errorf("PROPFIND allprop returned %v:\n%v", name, w.Body.String())
		}
	}

	// Without locking, the If header field isn't evaluated
	w = serveLockTest(h, http.MethodPut, "/file.txt", "", "If", "(<opaquelocktoken:foo>)")
	if w.Code != http.StatusNoContent {
		t.Errorf("PUT with an unknown lock token: status = %v, want 204", w.Code)
	}
	w = serveLockTest(h, http.MethodDelete, "/file.txt", "", "If", `(["bogus"])`)
	if w.Code != http.StatusNoContent {
		t.Errorf("DELETE with a mismatched ETag in If: status = %v, want 204", w.Code)
	}
}
