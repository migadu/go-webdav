package webdav

import (
	"context"
	"encoding/xml"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const sharedLockInfo = `<?xml version="1.0" encoding="utf-8"?>
<D:lockinfo xmlns:D="DAV:">
  <D:lockscope><D:shared/></D:lockscope>
  <D:locktype><D:write/></D:locktype>
  <D:owner>bob</D:owner>
</D:lockinfo>`

// lock creates a lock with LOCK, and returns its token.
func (lt *lockTest) lock(code int, target, body string, header ...string) string {
	lt.t.Helper()
	w := lt.expect(code, "LOCK", target, body, header...)
	if w.Code != code {
		return ""
	}
	token, err := lockTokenHeader(w)
	if err != nil {
		lt.t.Fatalf("LOCK %v: Lock-Token = %q", target, w.Header().Get("Lock-Token"))
	}
	locks := lockResponse(lt.t, w)
	if len(locks) == 0 || locks[0].Token != token {
		lt.t.Errorf("LOCK %v: response = %+v, want the lock first", target, locks)
	}
	return token
}

// RFC 4918 sections 6.1 and 7.4: a depth-infinity lock on a collection
// locks the collection and all of its members. These are the lock_collection
// tests of the litmus locks test suite, where neon tags its If header fields
// with the root of the lock.
func TestLockCollection(t *testing.T) {
	lt := newLockTest(t)
	token := lt.lock(http.StatusOK, "/dir/", exclusiveLockInfo)
	w := lt.expect(http.StatusMultiStatus, "PROPFIND", "/dir/a.txt", propFindLockDiscovery, "Depth", "0")
	if locks := lockDiscovery(t, w)["/dir/a.txt"]; len(locks) != 1 || locks[0].Root != "/dir/" || locks[0].Depth != "infinity" {
		t.Errorf("lockdiscovery of /dir/a.txt = %+v, want the lock of /dir/", locks)
	}

	neonIf := []string{"If", "<http://example.com/dir/> (<" + token + ">)"}
	w = lt.expect(http.StatusLocked, http.MethodPut, "/dir/a.txt", "")
	expectPrecondition(t, w, "lock-token-submitted", "/dir/")
	lt.expect(http.StatusNoContent, http.MethodPut, "/dir/a.txt", "", neonIf...)
	lt.expect(http.StatusCreated, http.MethodPut, "/dir/lockme.txt", "", neonIf...)
	lt.expect(http.StatusCreated, "COPY", "/dir/lockme.txt", "", "Destination", "/whocares")
	lt.expect(http.StatusNoContent, http.MethodDelete, "/whocares", "")
	lt.expect(http.StatusLocked, http.MethodDelete, "/dir/lockme.txt", "")
	lt.expect(http.StatusLocked, "MOVE", "/dir/lockme.txt", "", "Destination", "/moved.txt")

	// A lock covering the collection can't be shared with another
	w = lt.expect(http.StatusLocked, "LOCK", "/dir/a.txt", exclusiveLockInfo)
	expectPrecondition(t, w, "no-conflicting-lock", "/dir/")
	// But a depth 0 lock on its parent doesn't cover it
	parent := lt.lock(http.StatusOK, "/", exclusiveLockInfo, "Depth", "0")
	lt.expect(http.StatusNoContent, "UNLOCK", "/", "", "Lock-Token", "<"+parent+">")

	// Refreshing through a member (litmus indirect_refresh)
	lt.expect(http.StatusOK, "LOCK", "/dir/lockme.txt", "", "If", "(<"+token+">)")
	lt.expect(http.StatusNoContent, "UNLOCK", "/dir/", "", "Lock-Token", "<"+token+">")
	lt.expect(http.StatusNoContent, http.MethodPut, "/dir/a.txt", "")
}

// RFC 4918 section 7.4: a depth 0 lock on a collection protects its set of
// members, not their contents.
func TestLockCollectionDepthZero(t *testing.T) {
	lt := newLockTest(t)
	token := lt.lock(http.StatusOK, "/dir/", exclusiveLockInfo, "Depth", "0")
	lt.expect(http.StatusNoContent, http.MethodPut, "/dir/a.txt", "")
	w := lt.expect(http.StatusLocked, http.MethodPut, "/dir/new.txt", "")
	expectPrecondition(t, w, "lock-token-submitted", "/dir/")
	lt.expect(http.StatusCreated, http.MethodPut, "/dir/new.txt", "", "If", "</dir/> (<"+token+">)")
	// Members can be locked on their own
	lt.lock(http.StatusOK, "/dir/a.txt", exclusiveLockInfo)
}

// RFC 4918 section 9.10.9: a depth-infinity lock which can't be granted
// because of a lock on a member is refused with a multi-status response
// listing the member, with the Request-URI failing with 424 Failed
// Dependency.
func TestLockCollectionMemberConflict(t *testing.T) {
	lt := newLockTest(t)
	lt.lock(http.StatusOK, "/dir/a.txt", exclusiveLockInfo)
	w := lt.expect(http.StatusMultiStatus, "LOCK", "/dir/", exclusiveLockInfo)
	codes := make(map[string]int)
	var ms struct {
		Responses []struct {
			Href   string `xml:"href"`
			Status string `xml:"status"`
			Error  *struct {
				NoConflictingLock *struct{} `xml:"no-conflicting-lock"`
			} `xml:"error"`
		} `xml:"response"`
	}
	if err := xml.Unmarshal(w.Body.Bytes(), &ms); err != nil {
		t.Fatalf("malformed multi-status: %v\n%v", err, w.Body.String())
	}
	for _, resp := range ms.Responses {
		var code int
		for _, c := range []int{http.StatusLocked, http.StatusFailedDependency} {
			if strings.Contains(resp.Status, " "+http.StatusText(c)) {
				code = c
			}
		}
		codes[resp.Href] = code
		if code == http.StatusLocked && (resp.Error == nil || resp.Error.NoConflictingLock == nil) {
			t.Errorf("%v: no DAV:no-conflicting-lock precondition", resp.Href)
		}
	}
	if codes["/dir/a.txt"] != http.StatusLocked || codes["/dir/"] != http.StatusFailedDependency || len(codes) != 2 {
		t.Errorf("LOCK multi-status = %v, want /dir/a.txt 423 and /dir/ 424:\n%v", codes, w.Body.String())
	}
	// A depth 0 lock doesn't conflict
	lt.lock(http.StatusOK, "/dir/", exclusiveLockInfo, "Depth", "0")
}

// RFC 4918 section 6.1: shared locks can be held by several clients at
// once, but not along with an exclusive lock. Any of the shared locks allows
// modifying the resource.
func TestSharedLocks(t *testing.T) {
	lt := newLockTest(t)
	w := lt.expect(http.StatusMultiStatus, "PROPFIND", "/file.txt", `<?xml version="1.0"?><D:propfind xmlns:D="DAV:"><D:prop><D:supportedlock/></D:prop></D:propfind>`, "Depth", "0")
	if body := w.Body.String(); !strings.Contains(body, "<exclusive>") || !strings.Contains(body, "<shared>") {
		t.Errorf("supportedlock lacks exclusive or shared locks:\n%v", body)
	}

	t1 := lt.lock(http.StatusOK, "/file.txt", sharedLockInfo)
	t2 := lt.lock(http.StatusOK, "/file.txt", sharedLockInfo)
	if t1 == t2 {
		t.Fatalf("both shared locks have token %v", t1)
	}
	w = lt.expect(http.StatusLocked, "LOCK", "/file.txt", exclusiveLockInfo)
	expectPrecondition(t, w, "no-conflicting-lock", "/file.txt")

	w = lt.expect(http.StatusMultiStatus, "PROPFIND", "/file.txt", propFindLockDiscovery, "Depth", "0")
	locks := lockDiscovery(t, w)["/file.txt"]
	if len(locks) != 2 || locks[0].Shared == nil || locks[1].Shared == nil {
		t.Errorf("lockdiscovery = %+v, want both shared locks", locks)
	}

	// The response to a refresh lists the refreshed lock first
	w = lt.expect(http.StatusOK, "LOCK", "/file.txt", "", "If", "(<"+t2+">)")
	if locks := lockResponse(t, w); len(locks) != 2 || locks[0].Token != t2 {
		t.Errorf("LOCK refresh response = %+v, want both locks, %v first", locks, t2)
	}

	w = lt.expect(http.StatusLocked, http.MethodPut, "/file.txt", "")
	expectPrecondition(t, w, "lock-token-submitted", "/file.txt")
	lt.expect(http.StatusNoContent, http.MethodPut, "/file.txt", "", "If", "(<"+t1+">)")
	lt.expect(http.StatusNoContent, http.MethodPut, "/file.txt", "", "If", "(<"+t2+">)")
	lt.expect(http.StatusNoContent, "UNLOCK", "/file.txt", "", "Lock-Token", "<"+t1+">")
	lt.expect(http.StatusLocked, http.MethodPut, "/file.txt", "", "If", "(Not <"+t1+">)")
	lt.expect(http.StatusNoContent, "UNLOCK", "/file.txt", "", "Lock-Token", "<"+t2+">")
	lt.lock(http.StatusOK, "/file.txt", exclusiveLockInfo)
}

// RFC 4918 section 7.3: locking an unmapped URL creates an empty resource,
// replying with 201 Created.
func TestLockUnmappedURL(t *testing.T) {
	lt := newLockTest(t)
	w := lt.serve(http.MethodOptions, "/new.txt", "")
	if allow := allowedMethods(w.Header()); !hasToken(allow, "LOCK") {
		t.Errorf("OPTIONS of an unmapped URL: Allow = %v, want LOCK", allow)
	}

	token := lt.lock(http.StatusCreated, "/new.txt", exclusiveLockInfo)
	if b, err := os.ReadFile(filepath.Join(lt.dir, "new.txt")); err != nil || len(b) != 0 {
		t.Errorf("new.txt = %q, %v, want an empty file", b, err)
	}
	lt.expect(http.StatusOK, http.MethodGet, "/new.txt", "")
	lt.expect(http.StatusLocked, http.MethodPut, "/new.txt", "hello")
	lt.expect(http.StatusNoContent, http.MethodPut, "/new.txt", "hello", "If", "(<"+token+">)")
	if b, err := os.ReadFile(filepath.Join(lt.dir, "new.txt")); err != nil || string(b) != "hello" {
		t.Errorf("new.txt = %q, %v, want hello", b, err)
	}

	// The parent collection must exist (RFC 4918 section 9.10.6)
	lt.expect(http.StatusConflict, "LOCK", "/missing/new.txt", exclusiveLockInfo)
	if locks, _ := lt.ls.Locks(context.Background(), "/missing/new.txt", false); len(locks) != 0 {
		t.Errorf("locks after a failed LOCK = %+v, want none", locks)
	}

	// Creating a member of a locked collection needs its lock
	coll := lt.createLock(Lock{Root: "/dir"})
	w = lt.expect(http.StatusLocked, "LOCK", "/dir/new.txt", exclusiveLockInfo)
	expectPrecondition(t, w, "lock-token-submitted", "/dir/")
	if _, err := os.Stat(filepath.Join(lt.dir, "dir", "new.txt")); !os.IsNotExist(err) {
		t.Errorf("dir/new.txt was created by a failed LOCK: %v", err)
	}
	lt.lock(http.StatusCreated, "/dir/new.txt", exclusiveLockInfo, "If", "</dir/> (<"+coll+">)")

	// A conflicting lock leaves no resource behind
	lt.createLock(Lock{Root: "/dir2", Recursive: true})
	if err := os.Mkdir(filepath.Join(lt.dir, "dir2"), 0755); err != nil {
		t.Fatal(err)
	}
	lt.expect(http.StatusLocked, "LOCK", "/dir2/new.txt", exclusiveLockInfo)
	if _, err := os.Stat(filepath.Join(lt.dir, "dir2", "new.txt")); !os.IsNotExist(err) {
		t.Errorf("dir2/new.txt was created by a failed LOCK: %v", err)
	}
}
