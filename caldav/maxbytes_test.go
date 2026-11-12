package caldav

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// MKCALENDAR requests may have no body (RFC 4791 section 5.3.1), also when
// it's read through http.MaxBytesReader, which answers a read of zero bytes
// without reading anything. A MKCOL request without a body is found out the
// same way: it asks for an ordinary collection (RFC 4918 section 9.3), which
// is refused, rather than for a body which isn't XML, which would be a 415
// error. A body is read in full.
func TestMkcalendarMaxBytesReader(t *testing.T) {
	for _, tc := range []struct {
		method, body, name string
		code               int
	}{
		{"MKCALENDAR", "", "", http.StatusCreated},
		{"MKCOL", "", "", http.StatusForbidden},
		{"MKCALENDAR", mkcalendarRequest, "test calendar", http.StatusCreated},
		{"MKCOL", mkcolBody(mkcolCalendarType + `<D:displayname>test calendar</D:displayname>`), "test calendar", http.StatusCreated},
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

		if w.Code != tc.code {
			t.Errorf("%v (body: %v): status = %v, want %v\n%s", tc.method, tc.body != "", w.Code, tc.code, w.Body.String())
			continue
		}
		if tc.code != http.StatusCreated {
			if len(backend.created) > 0 {
				t.Errorf("%v (body: %v): created calendars %+v, want none", tc.method, tc.body != "", backend.created)
			}
		} else if len(backend.created) != 1 || backend.created[0].Name != tc.name {
			t.Errorf("%v (body: %v): created calendars %+v, want one named %q", tc.method, tc.body != "", backend.created, tc.name)
		}
	}
}
