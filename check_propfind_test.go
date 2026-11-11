package webdav

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
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
