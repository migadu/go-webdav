package webdav

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// serveIfLines serves a request without body carrying several If header
// lines.
func (lt *lockTest) serveIfLines(method, target string, lines ...string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, target, nil)
	for _, line := range lines {
		req.Header.Add("If", line)
	}
	w := httptest.NewRecorder()
	lt.h.ServeHTTP(w, req)
	return w
}

// RFC 4918 section 10.4.2 doesn't allow several If header lines. They're
// read as one If header field holding all of their lists, the same way for
// a lock refresh as for any other request: a refresh naming two lock tokens
// is refused (section 9.10.2), whichever line holds the right one.
func TestIfHeaderLines(t *testing.T) {
	lt := newLockTest(t)
	token := lt.lockFile()
	const bogus = "opaquelocktoken:4a8b2c4d-0000-4000-8000-000000000000"

	for _, lines := range [][]string{
		{"(<" + token + ">)", "(<" + bogus + ">)"},
		{"(<" + bogus + ">)", "(<" + token + ">)"},
	} {
		if w := lt.serveIfLines("LOCK", "/file.txt", lines...); w.Code != http.StatusBadRequest {
			t.Errorf("LOCK refresh with If lines %q: status = %v, want 400\n%v", lines, w.Code, w.Body.String())
		}
	}
	// A blank line adds no list
	if w := lt.serveIfLines("LOCK", "/file.txt", "(<"+token+">)", ""); w.Code != http.StatusOK {
		t.Errorf("LOCK refresh with a blank If line: status = %v, want 200\n%v", w.Code, w.Body.String())
	}

	// Any list of any line can be true, and all the tokens are submitted
	if w := lt.serveIfLines(http.MethodPut, "/file.txt", "(<"+bogus+">)", "(<"+token+">)"); w.Code != http.StatusNoContent {
		t.Errorf("PUT with If lines: status = %v, want 204\n%v", w.Code, w.Body.String())
	}
}

// RFC 4918 sections 9.10.2 and 10.4: the If header field of a lock refresh
// is still evaluated. A tagged list applies to the tagged resource, which
// must be in the scope of the lock; the lock must also cover the
// Request-URI.
func TestLockRefreshTaggedIf(t *testing.T) {
	lt := newLockTest(t)
	token := lt.lockFile()
	coll := lt.createLock(Lock{Root: "/dir", Recursive: true})

	for _, tc := range []struct {
		target, hif  string
		code         int
		precondition string
	}{
		{"/file.txt", "</file.txt> (<" + token + ">)", http.StatusOK, ""},
		{"/file.txt", "<http://example.com/file.txt> (<" + token + ">)", http.StatusOK, ""},
		{"/dir/a.txt", "</dir/> (<" + coll + ">)", http.StatusOK, ""},
		{"/dir/a.txt", "</dir/a.txt> (<" + coll + ">)", http.StatusOK, ""},
		// The tagged resource isn't in the scope of the lock
		{"/file.txt", "</dir/a.txt> (<" + token + ">)", http.StatusPreconditionFailed, ""},
		{"/file.txt", "</unrelated.txt> (<" + token + ">)", http.StatusPreconditionFailed, ""},
		{"/dir/a.txt", "</file.txt> (<" + coll + ">)", http.StatusPreconditionFailed, ""},
		// The tagged resource is, the Request-URI isn't
		{"/file.txt", "</dir/> (<" + coll + ">)", http.StatusPreconditionFailed, "lock-token-matches-request-uri"},
	} {
		w := lt.serve("LOCK", tc.target, "", "If", tc.hif)
		if w.Code != tc.code {
			t.Errorf("LOCK %v refresh with If %q: status = %v, want %v\n%v", tc.target, tc.hif, w.Code, tc.code, w.Body.String())
		} else if tc.precondition != "" {
			expectPrecondition(t, w, tc.precondition)
		}
	}
}

// RFC 4918 sections 8.3 and 10.4.2: a Resource-Tag is an absolute URI or an
// absolute path. A relative reference is malformed. An absolute URI which
// isn't an HTTP URL, such as a URN, identifies no resource of the server:
// it holds no lock and has no entity-tag.
func TestIfResourceTag(t *testing.T) {
	lt := newLockTest(t)
	root := lt.createLock(Lock{Root: "/", Recursive: true})

	for _, tc := range []struct {
		hif  string
		code int
	}{
		{"</> (<" + root + ">)", http.StatusNoContent},
		{"<http://example.com> (<" + root + ">)", http.StatusNoContent},
		{"<http://example.com/file.txt> ([ETAG]) (<" + root + ">)", http.StatusNoContent},
		// Not the root collection
		{"<urn:example> (<" + root + ">)", http.StatusPreconditionFailed},
		{"<mailto:alice@example.com> (<" + root + ">)", http.StatusPreconditionFailed},
		{"<urn:example> (Not <" + root + ">)", http.StatusNoContent},
		// Not resolved against the Request-URI, nor the root
		{"<file.txt> (<" + root + ">)", http.StatusBadRequest},
		{"<dir/a.txt> (Not <" + root + ">)", http.StatusBadRequest},
		{"<//example.com/file.txt> (<" + root + ">)", http.StatusBadRequest},
		{"<?a=b> (<" + root + ">)", http.StatusBadRequest},
		// Malformed even if an earlier list is true
		{"</file.txt> (<" + root + ">) <file.txt> (<" + root + ">)", http.StatusBadRequest},
	} {
		// Each PUT changes the entity-tag
		hif := strings.ReplaceAll(tc.hif, "ETAG", lt.etag("/file.txt"))
		lt.expect(tc.code, http.MethodPut, "/file.txt", "", "If", hif)
	}
}
