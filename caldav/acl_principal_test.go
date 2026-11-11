package caldav

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// aclPrincipalBackend is testBackend with another principal and calendar
// home set.
type aclPrincipalBackend struct {
	testBackend
	principal, homeSet string
}

func (b aclPrincipalBackend) CurrentUserPrincipal(ctx context.Context) (string, error) {
	return b.principal, nil
}

func (b aclPrincipalBackend) CalendarHomeSetPath(ctx context.Context) (string, error) {
	return b.homeSet, nil
}

// propFindACLPrincipal sends a PROPFIND request for the DAV:owner and
// DAV:principal-collection-set of the calendar at calPath to h.
func propFindACLPrincipal(t *testing.T, h http.Handler, calPath string) string {
	t.Helper()
	req := httptest.NewRequest("PROPFIND", calPath, strings.NewReader(propFindACLProps))
	req.Header.Set("Content-Type", "application/xml")
	req.Header.Set("Depth", "0")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	if w.Code != http.StatusMultiStatus {
		t.Fatalf("PROPFIND %v returned %v:\n%s", calPath, w.Code, w.Body)
	}
	return w.Body.String()
}

// RFC 3744 section 5.1: a resource served without a current user, such as a
// public calendar, has an empty DAV:owner rather than an empty href.
func TestPropFindACLPropsNoPrincipal(t *testing.T) {
	const calPath = "/public/calendars/a/"
	h := &Handler{Backend: aclPrincipalBackend{
		testBackend: testBackend{calendars: []Calendar{{Path: calPath}}},
		homeSet:     "/public/calendars/",
	}}
	body := propFindACLPrincipal(t, h, calPath)
	for _, want := range []string{
		`<owner xmlns="DAV:"></owner>`,
		`<principal-collection-set xmlns="DAV:"></principal-collection-set>`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("want %s in:\n%s", want, body)
		}
	}
	if strings.Contains(body, "<href></href>") {
		t.Errorf("response has an empty href:\n%s", body)
	}
}

// RFC 3744 section 5.8: DAV:principal-collection-set is the collection which
// holds the principal, wherever it is: a principal shared with other servers
// (see webdav.ServePrincipal) can be outside Handler.Prefix, and principal
// collections "MAY be located on different hosts from the resource".
func TestPropFindPrincipalCollectionSetOutsidePrefix(t *testing.T) {
	const calPath = "/dav/user/calendars/a/"
	h := &Handler{
		Backend: aclPrincipalBackend{
			testBackend: testBackend{calendars: []Calendar{{Path: calPath}}},
			principal:   "/principals/user/",
			homeSet:     "/dav/user/calendars/",
		},
		Prefix: "/dav",
	}
	body := propFindACLPrincipal(t, h, calPath)
	for _, want := range []string{
		`<owner xmlns="DAV:"><href>/principals/user/</href></owner>`,
		`<principal-collection-set xmlns="DAV:"><href>/principals/</href></principal-collection-set>`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("want %s in:\n%s", want, body)
		}
	}
}
