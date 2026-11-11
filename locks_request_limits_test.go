package webdav

import (
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
