package caldav

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// CalDAV servers don't support locking: they don't advertise DAV class 2,
// and refuse LOCK and UNLOCK as unsupported methods whatever the request.
func TestHandlerNoLocking(t *testing.T) {
	h := Handler{Backend: testBackend{calendars: []Calendar{{Path: "/user/calendars/a/"}}}}

	req := httptest.NewRequest(http.MethodOptions, "/user/calendars/a/", nil)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	for _, v := range strings.Split(w.Header().Get("DAV"), ",") {
		if strings.TrimSpace(v) == "2" {
			t.Errorf("DAV = %q, want no class 2", w.Header().Get("DAV"))
		}
	}
	if allow := w.Header().Get("Allow"); strings.Contains(allow, "LOCK") {
		t.Errorf("Allow = %q, want neither LOCK nor UNLOCK", allow)
	}

	for _, tc := range []struct {
		method, body string
		header       []string
	}{
		{"LOCK", `<?xml version="1.0"?><D:lockinfo xmlns:D="DAV:"><D:lockscope><D:exclusive/></D:lockscope><D:locktype><D:write/></D:locktype></D:lockinfo>`, []string{"Content-Type", "application/xml"}},
		{"LOCK", "", nil},
		{"LOCK", "", []string{"If", "(<opaquelocktoken:foo>)"}},
		{"UNLOCK", "", nil},
		{"UNLOCK", "", []string{"Lock-Token", "<opaquelocktoken:foo>"}},
	} {
		req := httptest.NewRequest(tc.method, "/user/calendars/a/", strings.NewReader(tc.body))
		for i := 0; i+1 < len(tc.header); i += 2 {
			req.Header.Set(tc.header[i], tc.header[i+1])
		}
		w := httptest.NewRecorder()
		h.ServeHTTP(w, req)
		if w.Code != http.StatusMethodNotAllowed {
			t.Errorf("%v %v: status = %v, want 405", tc.method, tc.header, w.Code)
		}
	}
}
