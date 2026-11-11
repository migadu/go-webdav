package webdav

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// lockTest is a Handler with a MemLockSystem, serving a directory holding
// file.txt, dir/ and dir/a.txt.
type lockTest struct {
	t   *testing.T
	dir string
	ls  *MemLockSystem
	h   *Handler
}

func newLockTest(t *testing.T) *lockTest {
	dir := newLockTestDir(t)
	if err := os.WriteFile(filepath.Join(dir, "dir", "a.txt"), []byte("a"), 0644); err != nil {
		t.Fatal(err)
	}
	ls := &MemLockSystem{}
	return &lockTest{t: t, dir: dir, ls: ls, h: &Handler{FileSystem: LocalFileSystem(dir), LockSystem: ls}}
}

func (lt *lockTest) serve(method, target, body string, header ...string) *httptest.ResponseRecorder {
	return serveLockTest(lt.h, method, target, body, header...)
}

// expect serves a request and checks its status code.
func (lt *lockTest) expect(code int, method, target, body string, header ...string) *httptest.ResponseRecorder {
	lt.t.Helper()
	w := lt.serve(method, target, body, header...)
	if w.Code != code {
		lt.t.Errorf("%v %v %q: status = %v, want %v\n%v", method, target, header, w.Code, code, w.Body.String())
	}
	return w
}

// etag returns the current entity-tag of a resource.
func (lt *lockTest) etag(target string) string {
	lt.t.Helper()
	w := lt.serve(http.MethodHead, target, "")
	etag := w.Header().Get("ETag")
	if etag == "" {
		lt.t.Fatalf("HEAD %v: no ETag", target)
	}
	return etag
}

// createLock adds a lock directly to the lock system.
func (lt *lockTest) createLock(lock Lock) string {
	lt.t.Helper()
	lock, err := lt.ls.Create(context.Background(), lock)
	if err != nil {
		lt.t.Fatalf("Create(%+v) = %v", lock, err)
	}
	return lock.Token
}

// lockFile locks /file.txt with LOCK.
func (lt *lockTest) lockFile() string {
	lt.t.Helper()
	w := lt.expect(http.StatusOK, "LOCK", "/file.txt", exclusiveLockInfo)
	token, err := lockTokenHeader(w)
	if err != nil {
		lt.t.Fatal(err)
	}
	return token
}

func lockTokenHeader(w *httptest.ResponseRecorder) (string, error) {
	s := w.Header().Get("Lock-Token")
	if len(s) < 2 || s[0] != '<' || s[len(s)-1] != '>' {
		return "", &HTTPError{Code: w.Code}
	}
	return s[1 : len(s)-1], nil
}

// expectPrecondition checks that a response carries a DAV:error element with
// the given precondition, and the given hrefs in it.
func expectPrecondition(t *testing.T, w *httptest.ResponseRecorder, name string, hrefs ...string) {
	t.Helper()
	body := w.Body.String()
	if ct := w.Header().Get("Content-Type"); !strings.Contains(ct, "xml") {
		t.Errorf("Content-Type = %q, want XML", ct)
	}
	i := strings.Index(body, "<"+name)
	if !strings.Contains(body, `<error xmlns="DAV:">`) || i < 0 {
		t.Errorf("response lacks the DAV:%v precondition:\n%v", name, body)
		return
	}
	for _, href := range hrefs {
		if !strings.Contains(body[i:], "<href>"+href+"</href>") {
			t.Errorf("DAV:%v lacks href %v:\n%v", name, href, body)
		}
	}
}

// RFC 4918 section 10.4: with locking, the If header field is evaluated for
// every request: it's true if any of its lists is, and a list is true if all
// of its conditions are. Otherwise the request fails with 412 Precondition
// Failed.
func TestIfHeaderEvaluation(t *testing.T) {
	lt := newLockTest(t)

	for _, tc := range []struct {
		hif  string
		code int
	}{
		{"([ETAG])", http.StatusNoContent},
		{`(["bogus"])`, http.StatusPreconditionFailed},
		{`(Not ["bogus"])`, http.StatusNoContent},
		{"(Not [ETAG])", http.StatusPreconditionFailed},
		{`(["bogus"]) ([ETAG])`, http.StatusNoContent},
		{`([ETAG] ["bogus"])`, http.StatusPreconditionFailed},
		{"(<DAV:no-lock>)", http.StatusPreconditionFailed},
		{"(Not <DAV:no-lock>)", http.StatusNoContent},
		// A lock token which doesn't exist (any longer)
		{"(<opaquelocktoken:4a8b2c4d-0000-4000-8000-000000000000>)", http.StatusPreconditionFailed},
		// Tagged lists apply to the tagged resource
		{"</file.txt> ([ETAG])", http.StatusNoContent},
		{"<http://example.com/file.txt> ([ETAG])", http.StatusNoContent},
		{"</dir/a.txt> ([ETAG])", http.StatusPreconditionFailed},
		{"</dir/a.txt> ([" + lt.etag("/dir/a.txt") + "])", http.StatusNoContent},
		{"</dir/a.txt> ([ETAG]) </file.txt> ([ETAG])", http.StatusNoContent},
		// Malformed
		{"(<urn:x>", http.StatusBadRequest},
		{"([ETAG]) </file.txt> ([ETAG])", http.StatusBadRequest},
	} {
		// The entity-tag changes with each successful PUT
		hif := strings.ReplaceAll(tc.hif, "ETAG", lt.etag("/file.txt"))
		lt.expect(tc.code, http.MethodPut, "/file.txt", "", "If", hif)
	}

	// The If header field applies to all methods
	lt.expect(http.StatusPreconditionFailed, http.MethodGet, "/file.txt", "", "If", `(["bogus"])`)
	lt.expect(http.StatusPreconditionFailed, "PROPFIND", "/file.txt", "", "If", `(["bogus"])`, "Depth", "0")
	lt.expect(http.StatusPreconditionFailed, http.MethodDelete, "/file.txt", "", "If", `(["bogus"])`)
	lt.expect(http.StatusMultiStatus, "PROPFIND", "/file.txt", "", "If", "(["+lt.etag("/file.txt")+"])", "Depth", "0")

	// Unmapped URLs have no entity-tag
	lt.expect(http.StatusPreconditionFailed, http.MethodPut, "/new.txt", "", "If", `(["bogus"])`)
	lt.expect(http.StatusCreated, http.MethodPut, "/new.txt", "", "If", `(Not ["bogus"])`)
}

// RFC 4918 sections 7 and 10.4.1: a lock token is submitted if it appears
// anywhere in the If header field. Modifying a locked resource requires the
// token, otherwise the request fails with 423 Locked and the
// DAV:lock-token-submitted precondition. These are the conditional PUTs of
// the litmus locks test suite.
func TestLockTokenSubmitted(t *testing.T) {
	lt := newLockTest(t)
	token := lt.lockFile()

	for _, tc := range []struct {
		name string
		hif  string
		code int
	}{
		{"no If", "", http.StatusLocked},
		{"cond_put", "<http://example.com/file.txt> (<" + token + "> [ETAG])", http.StatusNoContent},
		{"fail_cond_put", "<http://example.com/file.txt> (<DAV:no-lock> [ETAG])", http.StatusPreconditionFailed},
		{"cond_put_with_not", "<http://example.com/file.txt> (<" + token + ">) (Not <DAV:no-lock>)", http.StatusNoContent},
		{"cond_put_corrupt_token", "<http://example.com/file.txt> (<" + token + "x>) (Not <DAV:no-lock>)", http.StatusLocked},
		{"complex_cond_put", "(<" + token + "> [ETAG]) (Not <DAV:no-lock> [ETAG])", http.StatusNoContent},
		{"fail_complex_cond_put", `(<` + token + `> ["bogus"]) (Not <DAV:no-lock> ["bogus"])`, http.StatusPreconditionFailed},
		{"token in a Not condition", "(Not <" + token + ">) (Not <DAV:no-lock>)", http.StatusNoContent},
		{"token in a list evaluated to false", `(<` + token + `> ["bogus"]) (Not <DAV:no-lock>)`, http.StatusNoContent},
	} {
		t.Run(tc.name, func(t *testing.T) {
			lt.t = t
			var header []string
			if tc.hif != "" {
				header = []string{"If", strings.ReplaceAll(tc.hif, "ETAG", lt.etag("/file.txt"))}
			}
			w := lt.expect(tc.code, http.MethodPut, "/file.txt", "", header...)
			if tc.code == http.StatusLocked {
				expectPrecondition(t, w, "lock-token-submitted", "/file.txt")
			}
		})
	}
	lt.t = t

	hif := []string{"If", "(<" + token + ">)"}
	proppatch := `<?xml version="1.0"?><D:propertyupdate xmlns:D="DAV:"><D:set><D:prop><D:displayname>x</D:displayname></D:prop></D:set></D:propertyupdate>`
	w := lt.expect(http.StatusLocked, "PROPPATCH", "/file.txt", proppatch)
	expectPrecondition(t, w, "lock-token-submitted", "/file.txt")
	lt.expect(http.StatusMultiStatus, "PROPPATCH", "/file.txt", proppatch, hif...)

	// Reading and copying a locked resource doesn't need the token
	lt.expect(http.StatusOK, http.MethodGet, "/file.txt", "")
	lt.expect(http.StatusMultiStatus, "PROPFIND", "/file.txt", "", "Depth", "0")
	lt.expect(http.StatusCreated, "COPY", "/file.txt", "", "Destination", "/copy.txt")

	// Overwriting, moving and deleting it does
	w = lt.expect(http.StatusLocked, "COPY", "/dir/a.txt", "", "Destination", "/file.txt")
	expectPrecondition(t, w, "lock-token-submitted", "/file.txt")
	w = lt.expect(http.StatusLocked, "MOVE", "/copy.txt", "", "Destination", "/file.txt")
	expectPrecondition(t, w, "lock-token-submitted", "/file.txt")
	w = lt.expect(http.StatusLocked, "MOVE", "/file.txt", "", "Destination", "/moved.txt")
	expectPrecondition(t, w, "lock-token-submitted", "/file.txt")
	w = lt.expect(http.StatusLocked, http.MethodDelete, "/file.txt", "")
	expectPrecondition(t, w, "lock-token-submitted", "/file.txt")
	lt.expect(http.StatusNoContent, http.MethodDelete, "/file.txt", "", hif...)

	// Deleting the resource destroys its lock (RFC 4918 section 9.6)
	if locks, _ := lt.ls.Locks(context.Background(), "/", true); len(locks) != 0 {
		t.Errorf("locks after DELETE = %+v, want none", locks)
	}
	lt.expect(http.StatusCreated, http.MethodPut, "/file.txt", "")
}

// RFC 4918 section 7.5: a lock on a collection protects its set of members:
// adding, removing or renaming a member needs the token of the collection's
// lock, but modifying an existing member only needs the token of a lock on
// the member itself. A depth-infinity lock also protects the members.
func TestCollectionLockEnforcement(t *testing.T) {
	lt := newLockTest(t)
	coll := lt.createLock(Lock{Root: "/dir"})
	collIf := []string{"If", "</dir/> (<" + coll + ">)"}

	lt.expect(http.StatusNoContent, http.MethodPut, "/dir/a.txt", "")
	lt.expect(http.StatusMultiStatus, "PROPPATCH", "/dir/a.txt", `<?xml version="1.0"?><D:propertyupdate xmlns:D="DAV:"><D:set><D:prop><D:displayname>x</D:displayname></D:prop></D:set></D:propertyupdate>`)

	for _, tc := range []struct {
		method, target, dest string
	}{
		{http.MethodPut, "/dir/new.txt", ""},
		{"MKCOL", "/dir/sub", ""},
		{http.MethodDelete, "/dir/a.txt", ""},
		{http.MethodDelete, "/dir/", ""},
		{"MOVE", "/dir/a.txt", "/b.txt"},
		{"MOVE", "/file.txt", "/dir/f.txt"},
		{"MOVE", "/dir/a.txt", "/dir/b.txt"},
		{"COPY", "/file.txt", "/dir/f.txt"},
		{"COPY", "/file.txt", "/dir/a.txt"},
	} {
		var header []string
		if tc.dest != "" {
			header = []string{"Destination", tc.dest}
		}
		w := lt.expect(http.StatusLocked, tc.method, tc.target, "", header...)
		expectPrecondition(t, w, "lock-token-submitted", "/dir/")
	}

	// The scope of a depth 0 lock on a collection doesn't include its members
	lt.expect(http.StatusPreconditionFailed, http.MethodPut, "/dir/new.txt", "", "If", "(<"+coll+">)")
	lt.expect(http.StatusCreated, http.MethodPut, "/dir/new.txt", "", collIf...)
	lt.expect(http.StatusCreated, "MOVE", "/file.txt", "", append([]string{"Destination", "/dir/f.txt"}, collIf...)...)
	lt.expect(http.StatusCreated, "MOVE", "/dir/f.txt", "", append([]string{"Destination", "/file.txt"}, collIf...)...)
	lt.expect(http.StatusNoContent, http.MethodDelete, "/dir/new.txt", "", collIf...)
	if err := lt.ls.Unlock(context.Background(), coll); err != nil {
		t.Fatal(err)
	}

	// Depth infinity: members are locked too
	inf := lt.createLock(Lock{Root: "/dir", Recursive: true})
	w := lt.expect(http.StatusLocked, http.MethodPut, "/dir/a.txt", "")
	expectPrecondition(t, w, "lock-token-submitted", "/dir/")
	lt.expect(http.StatusNoContent, http.MethodPut, "/dir/a.txt", "", "If", "(<"+inf+">)")
	lt.expect(http.StatusCreated, http.MethodPut, "/dir/new.txt", "", "If", "(<"+inf+">)")
	if err := lt.ls.Unlock(context.Background(), inf); err != nil {
		t.Fatal(err)
	}

	// Deleting or moving a collection needs the tokens of the locks on its
	// members (RFC 4918 section 9.6.1)
	member := lt.createLock(Lock{Root: "/dir/a.txt"})
	w = lt.expect(http.StatusLocked, http.MethodDelete, "/dir/", "")
	expectPrecondition(t, w, "lock-token-submitted", "/dir/a.txt")
	w = lt.expect(http.StatusLocked, "MOVE", "/dir/", "", "Destination", "/dir2/")
	expectPrecondition(t, w, "lock-token-submitted", "/dir/a.txt")
	lt.expect(http.StatusCreated, "MOVE", "/dir/", "", "Destination", "/dir2/", "If", "</dir/a.txt> (<"+member+">)")

	// Moving the collection destroys the locks on its members (RFC 4918
	// section 9.9.4)
	if locks, _ := lt.ls.Locks(context.Background(), "/", true); len(locks) != 0 {
		t.Errorf("locks after MOVE = %+v, want none", locks)
	}
}

// RFC 4918 section 9.11: the lock token of an UNLOCK request must identify
// a lock whose scope includes the Request-URI, otherwise the request fails
// with 409 Conflict and the DAV:lock-token-matches-request-uri precondition.
func TestUnlockPreconditions(t *testing.T) {
	lt := newLockTest(t)
	token := lt.lockFile()

	lt.expect(http.StatusBadRequest, "UNLOCK", "/file.txt", "")
	lt.expect(http.StatusBadRequest, "UNLOCK", "/file.txt", "", "Lock-Token", token)
	w := lt.expect(http.StatusConflict, "UNLOCK", "/file.txt", "", "Lock-Token", "<opaquelocktoken:4a8b2c4d-0000-4000-8000-000000000000>")
	expectPrecondition(t, w, "lock-token-matches-request-uri")
	w = lt.expect(http.StatusConflict, "UNLOCK", "/dir/a.txt", "", "Lock-Token", "<"+token+">")
	expectPrecondition(t, w, "lock-token-matches-request-uri")
	lt.expect(http.StatusLocked, http.MethodPut, "/file.txt", "")
	lt.expect(http.StatusNoContent, "UNLOCK", "/file.txt", "", "Lock-Token", "<"+token+">")
	lt.expect(http.StatusNoContent, http.MethodPut, "/file.txt", "")
	w = lt.expect(http.StatusConflict, "UNLOCK", "/file.txt", "", "Lock-Token", "<"+token+">")
	expectPrecondition(t, w, "lock-token-matches-request-uri")

	// A member of a depth-infinity lock is in its scope
	inf := lt.createLock(Lock{Root: "/dir", Recursive: true})
	lt.expect(http.StatusNoContent, "UNLOCK", "/dir/a.txt", "", "Lock-Token", "<"+inf+">")
}

// RFC 4918 section 9.10.2: a lock refresh names in the If header field a
// lock whose scope includes the Request-URI, otherwise it fails with 412
// Precondition Failed and the DAV:lock-token-matches-request-uri
// precondition.
func TestLockRefreshPreconditions(t *testing.T) {
	lt := newLockTest(t)
	token := lt.lockFile()

	w := lt.expect(http.StatusPreconditionFailed, "LOCK", "/file.txt", "", "If", "(<opaquelocktoken:4a8b2c4d-0000-4000-8000-000000000000>)")
	expectPrecondition(t, w, "lock-token-matches-request-uri")
	w = lt.expect(http.StatusPreconditionFailed, "LOCK", "/dir/a.txt", "", "If", "(<"+token+">)")
	expectPrecondition(t, w, "lock-token-matches-request-uri")
	lt.expect(http.StatusBadRequest, "LOCK", "/file.txt", "")
	lt.expect(http.StatusBadRequest, "LOCK", "/file.txt", "", "If", "(<"+token+"> <"+token+">)")

	w = lt.expect(http.StatusOK, "LOCK", "/file.txt", "", "If", "(<"+token+">)")
	if !strings.Contains(w.Body.String(), token) {
		t.Errorf("LOCK refresh response lacks the lock token:\n%v", w.Body.String())
	}
	if h := w.Header().Get("Lock-Token"); h != "" {
		t.Errorf("LOCK refresh response has Lock-Token %q, want none", h)
	}
}

// RFC 4918 section 9.10.6: a LOCK request conflicting with an existing lock
// fails with 423 Locked and the DAV:no-conflicting-lock precondition.
func TestLockConflictPrecondition(t *testing.T) {
	lt := newLockTest(t)
	lt.lockFile()
	w := lt.expect(http.StatusLocked, "LOCK", "/file.txt", exclusiveLockInfo)
	expectPrecondition(t, w, "no-conflicting-lock")
}
