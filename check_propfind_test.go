package webdav

import (
	"encoding/xml"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/emersion/go-webdav/internal"
)

// noCallFileSystem is a FileSystem none of whose methods can be called: they
// all panic.
type noCallFileSystem struct {
	FileSystem
}

// A PROPFIND request asking for no properties (no propname, allprop or prop
// element, RFC 4918 section 14.20) is refused before the file system is
// called.
func TestPropFindWithoutProp(t *testing.T) {
	for _, depth := range []string{"0", "1", "infinity"} {
		body := `<?xml version="1.0" encoding="UTF-8"?><D:propfind xmlns:D="DAV:"/>`
		req := httptest.NewRequest("PROPFIND", "/dir/", strings.NewReader(body))
		req.Header.Set("Content-Type", "application/xml")
		req.Header.Set("Depth", depth)
		w := httptest.NewRecorder()
		func() {
			defer func() {
				if v := recover(); v != nil {
					t.Errorf("Depth: %v: PROPFIND called the file system: %v", depth, v)
				}
			}()
			h := Handler{FileSystem: noCallFileSystem{}}
			h.ServeHTTP(w, req)
		}()
		if w.Code != http.StatusBadRequest {
			t.Errorf("Depth: %v: PROPFIND returned %v, want 400: %s", depth, w.Code, w.Body.String())
		}
	}
}

// A PROPFIND request has a propname, an allprop optionally followed by an
// include, or a prop element, but not several (RFC 4918 section 14.20): other
// combinations are refused, before the file system is called.
func TestPropFindExclusiveElements(t *testing.T) {
	for _, elts := range []string{
		`<D:allprop/><D:prop><D:getetag/></D:prop>`,
		`<D:propname/><D:allprop/>`,
		`<D:propname/><D:prop><D:getetag/></D:prop>`,
		`<D:prop><D:getetag/></D:prop><D:include><D:getetag/></D:include>`,
	} {
		body := `<D:propfind xmlns:D="DAV:">` + elts + `</D:propfind>`
		newRequest := func(p string) *http.Request {
			req := httptest.NewRequest("PROPFIND", p, strings.NewReader(body))
			req.Header.Set("Content-Type", "application/xml")
			req.Header.Set("Depth", "0")
			return req
		}

		w := httptest.NewRecorder()
		func() {
			defer func() {
				if v := recover(); v != nil {
					t.Errorf("%v: PROPFIND called the file system: %v", elts, v)
				}
			}()
			h := Handler{FileSystem: noCallFileSystem{}}
			h.ServeHTTP(w, newRequest("/dir/"))
		}()
		if w.Code != http.StatusBadRequest {
			t.Errorf("%v: PROPFIND returned %v, want 400: %s", elts, w.Code, w.Body.String())
		}

		w = httptest.NewRecorder()
		ServePrincipal(w, newRequest("/principal/"), &ServePrincipalOptions{CurrentUserPrincipalPath: "/principal/"})
		if w.Code != http.StatusBadRequest {
			t.Errorf("%v: PROPFIND of a principal returned %v, want 400: %s", elts, w.Code, w.Body.String())
		}
	}
}

// An empty prop element (RFC 4918 section 14.18: <!ELEMENT prop ANY >) asks
// for no property. Each resource still gets a response, which must hold
// either a status or at least one propstat (section 14.24): a 200 OK status.
func TestPropFindEmptyProp(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "a.txt"), []byte("a"), 0o644); err != nil {
		t.Fatal(err)
	}
	newRequest := func(p, depth string) *http.Request {
		req := httptest.NewRequest("PROPFIND", p, strings.NewReader(`<D:propfind xmlns:D="DAV:"><D:prop/></D:propfind>`))
		req.Header.Set("Content-Type", "application/xml")
		req.Header.Set("Depth", depth)
		return req
	}

	for _, tc := range []struct {
		name  string
		serve func(w http.ResponseWriter)
		want  int
	}{
		{"file system", func(w http.ResponseWriter) {
			h := Handler{FileSystem: LocalFileSystem(dir)}
			h.ServeHTTP(w, newRequest("/", "1"))
		}, 2},
		{"principal", func(w http.ResponseWriter) {
			ServePrincipal(w, newRequest("/principal/", "0"), &ServePrincipalOptions{CurrentUserPrincipalPath: "/principal/"})
		}, 1},
	} {
		w := httptest.NewRecorder()
		tc.serve(w)
		if w.Code != http.StatusMultiStatus {
			t.Fatalf("%v: PROPFIND returned %v, want 207: %s", tc.name, w.Code, w.Body.String())
		}
		var ms internal.MultiStatus
		if err := xml.Unmarshal(w.Body.Bytes(), &ms); err != nil {
			t.Fatalf("%v: %v", tc.name, err)
		}
		if len(ms.Responses) != tc.want {
			t.Errorf("%v: got %v responses, want %v: %s", tc.name, len(ms.Responses), tc.want, w.Body.String())
		}
		for _, resp := range ms.Responses {
			if resp.Status == nil || resp.Status.Code != http.StatusOK || len(resp.PropStats) != 0 {
				t.Errorf("%v: response for %v has status %v and %v propstats, want a 200 OK status alone", tc.name, resp.Paths(), resp.Status, len(resp.PropStats))
			}
		}
	}
}
