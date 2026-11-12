package webdav

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"testing"
)

// The DAV:owner element of a lock is returned in the lockdiscovery property
// of every resource in its scope: its size is limited.
func TestLockOwnerTooLarge(t *testing.T) {
	lt := newLockTest(t)
	lockInfo := func(owner string) string {
		return strings.Replace(exclusiveLockInfo, "<D:owner>alice</D:owner>", "<D:owner>"+owner+"</D:owner>", 1)
	}

	lt.expect(http.StatusRequestEntityTooLarge, "LOCK", "/file.txt", lockInfo(strings.Repeat("a", 4096)))
	lt.expect(http.StatusRequestEntityTooLarge, "LOCK", "/file.txt", lockInfo(strings.Repeat("<D:href>a</D:href>", 1000)))
	lt.expect(http.StatusNoContent, http.MethodPut, "/file.txt", "")

	token := lt.lock(http.StatusOK, "/file.txt", lockInfo("<D:href>mailto:"+strings.Repeat("a", 3000)+"@example.com</D:href>"))
	lt.expect(http.StatusOK, "LOCK", "/file.txt", "", "If", "(<"+token+">)")
}

// Shared locks don't conflict, and the lockdiscovery property of a resource
// lists the DAV:owner of each lock on it, for every resource in a PROPFIND
// response. With the default limits, LOCK requests with large owners can't
// add more than 64 KiB of owners to each resource (RFC 4918 section 20.2).
func TestLockOwnersOfResourceTooLarge(t *testing.T) {
	lt := newLockTest(t)
	owner := "<D:href>mailto:" + strings.Repeat("a", 4000) + "@example.com</D:href>"
	lockInfo := strings.Replace(sharedLockInfo, "<D:owner>bob</D:owner>", "<D:owner>"+owner+"</D:owner>", 1)

	granted := 0
	for granted < 64 {
		w := lt.serve("LOCK", "/", lockInfo, "Depth", "infinity")
		if w.Code != http.StatusOK {
			if w.Code != http.StatusInsufficientStorage {
				t.Errorf("LOCK %v: status = %v, want 507\n%v", granted+1, w.Code, w.Body.String())
			}
			break
		}
		granted++
	}

	locks, err := lt.ls.Locks(context.Background(), "/dir/a.txt", false)
	if err != nil {
		t.Fatal(err)
	}
	total := 0
	for _, lock := range locks {
		total += len(lock.Owner)
	}
	// The limit is reached, not exceeded
	if len(locks) == 0 || total > 64<<10 || total+len(locks[0].Owner) <= 64<<10 {
		t.Errorf("%v LOCK requests granted, with %v bytes of owners on /dir/a.txt, want at most 64 KiB, and no room for another", granted, total)
	}
}

// RFC 4918 section 10.4: each condition of the If header field may need the
// state of another resource. A header with an absurd number of conditions
// is refused with 400 Bad Request.
func TestIfTooManyConditions(t *testing.T) {
	lt := newLockTest(t)
	token := lt.lockFile()

	tagged := ""
	for i := 0; i < 101; i++ {
		tagged += fmt.Sprintf("</dir/f%v> ([\"e\"]) ", i)
	}
	lists := strings.Repeat("(Not <DAV:no-lock>) ", 99) + "(<" + token + ">)"
	for _, tc := range []struct {
		name, method, hif string
		code              int
	}{
		{"100 conditions", http.MethodPut, lists, http.StatusNoContent},
		{"101 lists", http.MethodPut, "(Not <DAV:no-lock>) " + lists, http.StatusBadRequest},
		{"101 conditions in a list", http.MethodPut, "(<" + token + "> " + strings.Repeat("[\"e\"] ", 100) + ")", http.StatusBadRequest},
		{"101 tagged lists", http.MethodGet, tagged, http.StatusBadRequest},
		{"101 tagged lists in a refresh", "LOCK", tagged, http.StatusBadRequest},
	} {
		if w := lt.serve(tc.method, "/file.txt", "", "If", tc.hif); w.Code != tc.code {
			t.Errorf("%v %v: status = %v, want %v\n%.200v", tc.method, tc.name, w.Code, tc.code, w.Body.String())
		}
	}
}
