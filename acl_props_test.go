package webdav_test

import (
	"io"
	"net/http"
	"strings"
	"testing"
)

// RFC 3744 section 5: the principal ServePrincipal serves reports the access
// control properties clients ask for, such as DAV:principal-collection-set,
// which Apple clients ask for when discovering a principal.
func TestServePrincipalACLProps(t *testing.T) {
	srv := newAggregateServer(t)

	const body = `<?xml version="1.0" encoding="UTF-8"?>
<A:propfind xmlns:A="DAV:">
  <A:prop>
    <A:owner/>
    <A:supported-privilege-set/>
    <A:principal-collection-set/>
  </A:prop>
</A:propfind>`

	req, err := http.NewRequest("PROPFIND", srv.URL+aggPrincipal, strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/xml")
	req.Header.Set("Depth", "0")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	b, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	s := string(b)

	if strings.Contains(s, "404 Not Found") {
		t.Errorf("access control properties reported as not found:\n%s", s)
	}
	for _, want := range []string{
		// The principal owns itself
		`<owner xmlns="DAV:"><href>` + aggPrincipal + `</href></owner>`,
		`<principal-collection-set xmlns="DAV:"><href>/</href></principal-collection-set>`,
		`<supported-privilege xmlns="DAV:"><privilege xmlns="DAV:"><all xmlns="DAV:"></all></privilege><abstract xmlns="DAV:"></abstract><description xmlns="DAV:" xml:lang="en">`,
		`<privilege xmlns="DAV:"><read xmlns="DAV:"></read></privilege>`,
		`<privilege xmlns="DAV:"><write xmlns="DAV:"></write></privilege>`,
	} {
		if !strings.Contains(s, want) {
			t.Errorf("missing %s in:\n%s", want, s)
		}
	}
}
