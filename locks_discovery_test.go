package webdav

import (
	"bytes"
	"encoding/xml"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// testActiveLock is a DAV:activelock element (RFC 4918 section 14.1).
type testActiveLock struct {
	Exclusive *struct{} `xml:"lockscope>exclusive"`
	Shared    *struct{} `xml:"lockscope>shared"`
	Write     *struct{} `xml:"locktype>write"`
	Depth     string    `xml:"depth"`
	Owner     *struct {
		InnerXML string `xml:",innerxml"`
	} `xml:"owner"`
	Timeout string `xml:"timeout"`
	Token   string `xml:"locktoken>href"`
	Root    string `xml:"lockroot>href"`
}

// checkWellFormed checks that an XML document has no element with an
// attribute twice, which encoding/xml doesn't detect.
func checkWellFormed(t *testing.T, b []byte) {
	t.Helper()
	d := xml.NewDecoder(bytes.NewReader(b))
	for {
		tok, err := d.RawToken()
		if err == io.EOF {
			return
		} else if err != nil {
			t.Fatalf("malformed XML: %v\n%s", err, b)
		}
		start, ok := tok.(xml.StartElement)
		if !ok {
			continue
		}
		seen := make(map[xml.Name]bool)
		for _, attr := range start.Attr {
			if seen[attr.Name] {
				t.Fatalf("malformed XML: attribute %v:%v repeated in element %v\n%s", attr.Name.Space, attr.Name.Local, start.Name.Local, b)
			}
			seen[attr.Name] = true
		}
	}
}

// lockResponse parses the response to a LOCK request: a DAV:prop element
// holding the DAV:lockdiscovery property.
func lockResponse(t *testing.T, w *httptest.ResponseRecorder) []testActiveLock {
	t.Helper()
	checkWellFormed(t, w.Body.Bytes())
	if ct := w.Header().Get("Content-Type"); !strings.Contains(ct, "xml") {
		t.Errorf("LOCK response Content-Type = %q, want XML", ct)
	}
	var prop struct {
		XMLName xml.Name         `xml:"DAV: prop"`
		Locks   []testActiveLock `xml:"lockdiscovery>activelock"`
	}
	if err := xml.Unmarshal(w.Body.Bytes(), &prop); err != nil {
		t.Fatalf("malformed LOCK response: %v\n%v", err, w.Body.String())
	}
	return prop.Locks
}

// lockDiscovery returns the DAV:lockdiscovery property of each resource of
// a PROPFIND response.
func lockDiscovery(t *testing.T, w *httptest.ResponseRecorder) map[string][]testActiveLock {
	t.Helper()
	checkWellFormed(t, w.Body.Bytes())
	var ms struct {
		Responses []struct {
			Href      string `xml:"href"`
			PropStats []struct {
				Status    string `xml:"status"`
				Discovery *struct {
					Locks []testActiveLock `xml:"activelock"`
				} `xml:"prop>lockdiscovery"`
			} `xml:"propstat"`
		} `xml:"response"`
	}
	if err := xml.Unmarshal(w.Body.Bytes(), &ms); err != nil {
		t.Fatalf("malformed PROPFIND response: %v\n%v", err, w.Body.String())
	}
	m := make(map[string][]testActiveLock)
	for _, resp := range ms.Responses {
		for _, ps := range resp.PropStats {
			if ps.Discovery != nil && strings.Contains(ps.Status, " 200 ") {
				m[resp.Href] = append([]testActiveLock{}, ps.Discovery.Locks...)
			}
		}
	}
	return m
}

const propFindLockDiscovery = `<?xml version="1.0" encoding="utf-8"?>
<D:propfind xmlns:D="DAV:"><D:prop><D:lockdiscovery/></D:prop></D:propfind>`

// RFC 4918 section 9.10.1: the response to a successful LOCK request holds
// the lockdiscovery property, with the lock as it was granted: its scope,
// depth, owner, timeout, token and root.
func TestLockResponse(t *testing.T) {
	lt := newLockTest(t)
	w := lt.expect(http.StatusOK, "LOCK", "/file.txt", exclusiveLockInfo, "Depth", "0", "Timeout", "Second-3600")
	token, err := lockTokenHeader(w)
	if err != nil {
		t.Fatalf("Lock-Token = %q", w.Header().Get("Lock-Token"))
	}
	locks := lockResponse(t, w)
	if len(locks) != 1 {
		t.Fatalf("LOCK response has %d active locks, want 1:\n%v", len(locks), w.Body.String())
	}
	got := locks[0]
	if got.Exclusive == nil || got.Shared != nil || got.Write == nil {
		t.Errorf("activelock has a wrong scope or type:\n%v", w.Body.String())
	}
	if got.Depth != "0" {
		t.Errorf("activelock depth = %q, want 0", got.Depth)
	}
	if got.Owner == nil || !strings.Contains(got.Owner.InnerXML, "alice") {
		t.Errorf("activelock owner = %+v, want alice", got.Owner)
	}
	if got.Timeout != "Second-3600" {
		t.Errorf("activelock timeout = %q, want Second-3600", got.Timeout)
	}
	if got.Token != token {
		t.Errorf("activelock token = %q, want %q", got.Token, token)
	}
	if got.Root != "/file.txt" {
		t.Errorf("activelock root = %q, want /file.txt", got.Root)
	}

	// RFC 4918 section 9.10.2: a refresh resets the timeout, ignoring the
	// Depth header field, and returns the lock without a Lock-Token header
	w = lt.expect(http.StatusOK, "LOCK", "/file.txt", "", "If", "(<"+token+">)", "Timeout", "Second-60", "Depth", "infinity")
	if h := w.Header().Get("Lock-Token"); h != "" {
		t.Errorf("LOCK refresh response has Lock-Token %q", h)
	}
	locks = lockResponse(t, w)
	if len(locks) != 1 || locks[0].Token != token || locks[0].Timeout != "Second-60" || locks[0].Depth != "0" || locks[0].Owner == nil {
		t.Errorf("LOCK refresh response = %+v, want the lock with a 60s timeout:\n%v", locks, w.Body.String())
	}
}

// RFC 4918 section 6.6: a refresh restarts the timeout of the lock, which
// keeps its timeout unless the refresh asks for another one.
func TestLockRefreshTimeout(t *testing.T) {
	lt := newLockTest(t)
	clock := newFakeClock()
	lt.ls.now = clock.now
	w := lt.expect(http.StatusOK, "LOCK", "/file.txt", exclusiveLockInfo, "Timeout", "Second-60")
	token, _ := lockTokenHeader(w)

	clock.advance(50 * time.Second)
	w = lt.expect(http.StatusOK, "LOCK", "/file.txt", "", "If", "(<"+token+">)")
	if locks := lockResponse(t, w); len(locks) != 1 || locks[0].Timeout != "Second-60" {
		t.Errorf("LOCK refresh without Timeout = %+v, want timeout Second-60", locks)
	}
	clock.advance(50 * time.Second)
	lt.expect(http.StatusLocked, http.MethodPut, "/file.txt", "")
	clock.advance(10 * time.Second)
	lt.expect(http.StatusNoContent, http.MethodPut, "/file.txt", "")
}

// RFC 4918 section 10.7: the Timeout header field is a list, and a server
// needn't honour it, so unknown values are ignored rather than rejected.
func TestLockTimeoutHeader(t *testing.T) {
	for _, tc := range []struct {
		timeout, want string
	}{
		{"", "Infinite"},
		{"Infinite, Second-4100000000", "Infinite"},
		{"Second-4100000000", "Second-4100000000"},
		{"bogus", "Infinite"},
	} {
		lt := newLockTest(t)
		lt.ls.MaxTimeout = -1
		w := lt.expect(http.StatusOK, "LOCK", "/file.txt", exclusiveLockInfo, "Timeout", tc.timeout)
		if locks := lockResponse(t, w); len(locks) != 1 || locks[0].Timeout != tc.want {
			t.Errorf("LOCK with Timeout %q = %+v, want timeout %v", tc.timeout, locks, tc.want)
		}
	}

	// The lock system may shorten the timeout
	lt := newLockTest(t)
	lt.h.LockSystem = &MemLockSystem{MaxTimeout: time.Minute}
	w := lt.expect(http.StatusOK, "LOCK", "/file.txt", exclusiveLockInfo, "Timeout", "Infinite")
	if locks := lockResponse(t, w); len(locks) != 1 || locks[0].Timeout != "Second-60" {
		t.Errorf("LOCK with MaxTimeout = %+v, want timeout Second-60", locks)
	}
}

// RFC 4918 section 6.6: a lock which isn't refreshed expires after its
// timeout.
func TestLockExpiry(t *testing.T) {
	lt := newLockTest(t)
	clock := newFakeClock()
	lt.ls.now = clock.now
	lt.expect(http.StatusOK, "LOCK", "/file.txt", exclusiveLockInfo, "Timeout", "Second-10")
	lt.expect(http.StatusLocked, http.MethodPut, "/file.txt", "")
	clock.advance(10 * time.Second)
	lt.expect(http.StatusNoContent, http.MethodPut, "/file.txt", "")
	w := lt.expect(http.StatusMultiStatus, "PROPFIND", "/file.txt", propFindLockDiscovery, "Depth", "0")
	if locks := lockDiscovery(t, w)["/file.txt"]; len(locks) != 0 {
		t.Errorf("lockdiscovery after expiry = %+v, want none", locks)
	}
}

// RFC 4918 section 9.10.3: LOCK only accepts "Depth: 0" and "Depth:
// infinity".
func TestLockDepthHeader(t *testing.T) {
	lt := newLockTest(t)
	lt.expect(http.StatusBadRequest, "LOCK", "/file.txt", exclusiveLockInfo, "Depth", "1")
	lt.expect(http.StatusBadRequest, "LOCK", "/file.txt", exclusiveLockInfo, "Depth", "bogus")
	w := lt.expect(http.StatusOK, "LOCK", "/file.txt", exclusiveLockInfo)
	if locks := lockResponse(t, w); len(locks) != 1 || locks[0].Depth != "infinity" {
		t.Errorf("LOCK without Depth = %+v, want depth infinity", locks)
	}
}

func TestLockRequestBody(t *testing.T) {
	lt := newLockTest(t)
	for _, body := range []string{
		`<?xml version="1.0"?><D:lockinfo xmlns:D="DAV:"><D:locktype><D:write/></D:locktype></D:lockinfo>`,
		`<?xml version="1.0"?><D:lockinfo xmlns:D="DAV:"><D:lockscope><D:exclusive/><D:shared/></D:lockscope><D:locktype><D:write/></D:locktype></D:lockinfo>`,
		`<?xml version="1.0"?><D:lockinfo xmlns:D="DAV:"><D:lockscope><D:exclusive/></D:lockscope></D:lockinfo>`,
		`<?xml version="1.0"?><D:lockinfo xmlns:D="DAV:"><D:lockscope><D:exclusive/></D:lockscope>`,
	} {
		lt.expect(http.StatusBadRequest, "LOCK", "/file.txt", body)
	}

	// A body without Content-Type is still a lockinfo element
	req := httptest.NewRequest("LOCK", "/file.txt", strings.NewReader(exclusiveLockInfo))
	w := httptest.NewRecorder()
	lt.h.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Errorf("LOCK without Content-Type: status = %v, want 200", w.Code)
	}
}

// RFC 4918 section 14.17: the owner element supplied by the client is
// returned as is, which needs the namespaces of its children to be declared
// again.
func TestLockOwner(t *testing.T) {
	for _, owner := range []string{
		`<D:owner><D:href>http://example.org/~alice</D:href></D:owner>`,
		`<D:owner xmlns:x="urn:x"><x:name x:id="1">alice</x:name></D:owner>`,
		`<D:owner><name xmlns="urn:y">alice</name></D:owner>`,
	} {
		lt := newLockTest(t)
		body := `<?xml version="1.0"?><D:lockinfo xmlns:D="DAV:"><D:lockscope><D:exclusive/></D:lockscope><D:locktype><D:write/></D:locktype>` + owner + `</D:lockinfo>`
		w := lt.expect(http.StatusOK, "LOCK", "/file.txt", body)
		locks := lockResponse(t, w)
		if len(locks) != 1 || locks[0].Owner == nil || !strings.Contains(locks[0].Owner.InnerXML, "alice") {
			t.Errorf("LOCK with owner %v = %+v\n%v", owner, locks, w.Body.String())
		}

		w = lt.expect(http.StatusMultiStatus, "PROPFIND", "/file.txt", propFindLockDiscovery, "Depth", "0")
		locks = lockDiscovery(t, w)["/file.txt"]
		if len(locks) != 1 || locks[0].Owner == nil || !strings.Contains(locks[0].Owner.InnerXML, "alice") {
			t.Errorf("lockdiscovery with owner %v = %+v\n%v", owner, locks, w.Body.String())
		}
	}
}

// RFC 4918 section 15.8: the lockdiscovery property lists the active locks
// whose scope includes the resource.
func TestLockDiscoveryProperty(t *testing.T) {
	lt := newLockTest(t)
	token := lt.lockFile()
	coll := lt.createLock(Lock{Root: "/dir", Recursive: true, Timeout: time.Hour})

	w := lt.expect(http.StatusMultiStatus, "PROPFIND", "/", propFindLockDiscovery, "Depth", "infinity")
	discovery := lockDiscovery(t, w)
	if locks := discovery["/file.txt"]; len(locks) != 1 || locks[0].Token != token || locks[0].Root != "/file.txt" {
		t.Errorf("lockdiscovery of /file.txt = %+v, want the lock", locks)
	}
	for _, href := range []string{"/dir", "/dir/a.txt"} {
		locks := discovery[href]
		if len(locks) != 1 || locks[0].Token != coll || locks[0].Root != "/dir/" || locks[0].Depth != "infinity" {
			t.Errorf("lockdiscovery of %v = %+v, want the lock of /dir/", href, locks)
		} else if !strings.HasPrefix(locks[0].Timeout, "Second-") {
			t.Errorf("lockdiscovery timeout of %v = %q, want the remaining seconds", href, locks[0].Timeout)
		}
	}
	w = lt.expect(http.StatusMultiStatus, "PROPFIND", "/", propFindLockDiscovery, "Depth", "0")
	if locks, ok := lockDiscovery(t, w)["/"]; !ok || len(locks) != 0 {
		t.Errorf("lockdiscovery of / = %+v, %v, want an empty property", locks, ok)
	}

	// allprop includes lockdiscovery
	w = lt.expect(http.StatusMultiStatus, "PROPFIND", "/file.txt", "", "Depth", "0")
	if locks := lockDiscovery(t, w)["/file.txt"]; len(locks) != 1 || locks[0].Token != token {
		t.Errorf("allprop lockdiscovery of /file.txt = %+v, want the lock", locks)
	}
}

// Hrefs are URL-encoded paths.
func TestLockRootHref(t *testing.T) {
	lt := newLockTest(t)
	if err := os.WriteFile(filepath.Join(lt.dir, "a b.txt"), nil, 0644); err != nil {
		t.Fatal(err)
	}
	w := lt.expect(http.StatusOK, "LOCK", "/a%20b.txt", exclusiveLockInfo)
	if locks := lockResponse(t, w); len(locks) != 1 || locks[0].Root != "/a%20b.txt" {
		t.Errorf("LOCK response = %+v, want root /a%%20b.txt", locks)
	}
}
