package caldav

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// MKCALENDAR and MKCOL requests may have no body (RFC 4791 section 5.3.1 and
// RFC 5689 section 3), also when it's read through http.MaxBytesReader, which
// answers a read of zero bytes without reading anything. A body is read in
// full.
func TestMkcalendarMaxBytesReader(t *testing.T) {
	for _, tc := range []struct {
		method, body, name string
	}{
		{"MKCALENDAR", "", ""},
		{"MKCOL", "", ""},
		{"MKCALENDAR", mkcalendarRequest, "test calendar"},
	} {
		backend := &createBackend{}
		handler := Handler{Backend: backend}

		req := httptest.NewRequest(tc.method, "/user/calendars/default/", strings.NewReader(tc.body))
		if tc.body != "" {
			req.Header.Set("Content-Type", "application/xml")
		}
		w := httptest.NewRecorder()
		req.Body = http.MaxBytesReader(w, req.Body, 1<<20)
		handler.ServeHTTP(w, req)

		if w.Code != http.StatusCreated {
			t.Errorf("%v (body: %v): status = %v, want 201\n%s", tc.method, tc.body != "", w.Code, w.Body.String())
			continue
		}
		if len(backend.created) != 1 || backend.created[0].Name != tc.name {
			t.Errorf("%v (body: %v): created calendars %+v, want one named %q", tc.method, tc.body != "", backend.created, tc.name)
		}
	}
}
