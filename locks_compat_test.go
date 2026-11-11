package webdav

import (
	"encoding/xml"
	"net/http"
	"os"
	"path/filepath"
	"testing"
)

// An empty If header field has no lists to evaluate: it's ignored rather
// than failing every request.
func TestEmptyIfHeader(t *testing.T) {
	lt := newLockTest(t)
	lt.expect(http.StatusOK, http.MethodGet, "/file.txt", "", "If", "")
	lt.expect(http.StatusNoContent, http.MethodPut, "/file.txt", "", "If", " ")
}

// RFC 2616 section 2.1: literals such as "Not" are case-insensitive.
func TestIfHeaderNotCase(t *testing.T) {
	lt := newLockTest(t)
	lt.expect(http.StatusNoContent, http.MethodPut, "/file.txt", "", "If", `(not ["bogus"])`)
	lt.expect(http.StatusPreconditionFailed, http.MethodPut, "/file.txt", "", "If", `(NOT <DAV:no-lock> <DAV:no-lock>)`)
}

// RFC 4918 section 7.3: locking an unmapped URL creates a non-collection
// resource, which can't be at a URL ending with a slash.
func TestLockUnmappedCollectionURL(t *testing.T) {
	lt := newLockTest(t)
	lt.expect(http.StatusConflict, "LOCK", "/newdir/", exclusiveLockInfo)
	if _, err := os.Stat(filepath.Join(lt.dir, "newdir")); !os.IsNotExist(err) {
		t.Errorf("LOCK created newdir: %v", err)
	}
	lt.expect(http.StatusCreated, "MKCOL", "/newdir/", "")
}

// An owner element in no namespace stays in no namespace, rather than taking
// the DAV: namespace of the lockdiscovery property.
func TestLockOwnerNoNamespace(t *testing.T) {
	lt := newLockTest(t)
	body := `<?xml version="1.0"?><D:lockinfo xmlns:D="DAV:"><D:lockscope><D:exclusive/></D:lockscope><D:locktype><D:write/></D:locktype><D:owner><name>alice</name></D:owner></D:lockinfo>`
	w := lt.expect(http.StatusOK, "LOCK", "/file.txt", body)
	checkWellFormed(t, w.Body.Bytes())
	var prop struct {
		Owner struct {
			Children []struct {
				XMLName xml.Name
				Value   string `xml:",chardata"`
			} `xml:",any"`
		} `xml:"lockdiscovery>activelock>owner"`
	}
	if err := xml.Unmarshal(w.Body.Bytes(), &prop); err != nil {
		t.Fatal(err)
	}
	if c := prop.Owner.Children; len(c) != 1 || c[0].XMLName != (xml.Name{Local: "name"}) || c[0].Value != "alice" {
		t.Errorf("owner = %+v, want a name element in no namespace:\n%v", c, w.Body.String())
	}
}
