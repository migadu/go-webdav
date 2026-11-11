package webdav

import (
	"encoding/xml"
	"net/http"
	"strings"
	"testing"
)

// countHref returns the number of DAV:href elements holding href.
func countHref(body, href string) int {
	return strings.Count(body, "<href>"+href+"</href>")
}

// RFC 4918 section 14.24: an href appears at most once among the responses
// of a multi-status response. Neither do preconditions list the same href
// twice when several shared locks have the same root.
func TestSharedLocksHrefsOnce(t *testing.T) {
	lt := newLockTest(t)
	for i := 0; i < 2; i++ {
		lt.createLock(Lock{Root: "/file.txt", Shared: true})
		lt.createLock(Lock{Root: "/dir/a.txt", Shared: true})
	}

	// DAV:lock-token-submitted
	w := lt.expect(http.StatusLocked, http.MethodPut, "/file.txt", "")
	expectPrecondition(t, w, "lock-token-submitted", "/file.txt")
	if n := countHref(w.Body.String(), "/file.txt"); n != 1 {
		t.Errorf("PUT: /file.txt listed %v times, want once:\n%v", n, w.Body.String())
	}
	w = lt.expect(http.StatusLocked, http.MethodDelete, "/dir/", "")
	expectPrecondition(t, w, "lock-token-submitted", "/dir/a.txt")
	if n := countHref(w.Body.String(), "/dir/a.txt"); n != 1 {
		t.Errorf("DELETE: /dir/a.txt listed %v times, want once:\n%v", n, w.Body.String())
	}

	// DAV:no-conflicting-lock
	w = lt.expect(http.StatusLocked, "LOCK", "/file.txt", exclusiveLockInfo)
	expectPrecondition(t, w, "no-conflicting-lock", "/file.txt")
	if n := countHref(w.Body.String(), "/file.txt"); n != 1 {
		t.Errorf("LOCK: /file.txt listed %v times, want once:\n%v", n, w.Body.String())
	}

	// A response per member in the multi-status response
	w = lt.expect(http.StatusMultiStatus, "LOCK", "/dir/", exclusiveLockInfo)
	var ms struct {
		Responses []struct {
			Hrefs []string `xml:"href"`
		} `xml:"response"`
	}
	if err := xml.Unmarshal(w.Body.Bytes(), &ms); err != nil {
		t.Fatalf("malformed multi-status: %v\n%v", err, w.Body.String())
	}
	seen := make(map[string]int)
	for _, resp := range ms.Responses {
		for _, href := range resp.Hrefs {
			seen[href]++
		}
	}
	if seen["/dir/a.txt"] != 1 || seen["/dir/"] != 1 || len(seen) != 2 {
		t.Errorf("LOCK /dir/: multi-status hrefs = %v, want /dir/a.txt and /dir/ once each:\n%v", seen, w.Body.String())
	}
}
