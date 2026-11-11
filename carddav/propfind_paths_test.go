package carddav

import (
	"encoding/xml"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/emersion/go-webdav/internal"
)

func TestPropFindRootAndUnknownPaths(t *testing.T) {
	h := &Handler{Backend: newSyncTestBackend()}

	t.Run("PROPFIND / reports root href instead of principal href", func(t *testing.T) {
		req := httptest.NewRequest("PROPFIND", "/", strings.NewReader(`<?xml version="1.0" encoding="utf-8"?>
<D:propfind xmlns:D="DAV:"><D:prop><D:current-user-principal/></D:prop></D:propfind>`))
		req.Header.Set("Content-Type", "application/xml")
		req.Header.Set("Depth", "0")
		w := httptest.NewRecorder()
		h.ServeHTTP(w, req)

		if w.Code != http.StatusMultiStatus {
			t.Fatalf("status = %d, want 207", w.Code)
		}
		var ms internal.MultiStatus
		if err := xml.NewDecoder(w.Body).Decode(&ms); err != nil {
			t.Fatalf("Decode: %v", err)
		}
		if len(ms.Responses) != 1 || len(ms.Responses[0].Hrefs) != 1 {
			t.Fatalf("unexpected responses: %+v", ms.Responses)
		}
		if got := ms.Responses[0].Hrefs[0].Path; got != "/" {
			t.Errorf("root PROPFIND href = %q, want %q", got, "/")
		}
	})

	t.Run("PROPFIND on unknown principal or home-set returns 404 Not Found", func(t *testing.T) {
		for _, target := range []string{"/other-user/", "/user/other-homeset/"} {
			req := httptest.NewRequest("PROPFIND", target, nil)
			req.Header.Set("Depth", "0")
			w := httptest.NewRecorder()
			h.ServeHTTP(w, req)
			if w.Code != http.StatusNotFound {
				t.Errorf("PROPFIND %s: status = %d, want 404 (body: %s)", target, w.Code, w.Body.String())
			}
		}
	})

	t.Run("PROPFIND on principal or home-set without trailing slash returns response", func(t *testing.T) {
		for _, target := range []string{"/user", "/user/contacts"} {
			req := httptest.NewRequest("PROPFIND", target, nil)
			req.Header.Set("Depth", "0")
			w := httptest.NewRecorder()
			h.ServeHTTP(w, req)
			if w.Code != http.StatusMultiStatus {
				t.Fatalf("PROPFIND %s: status = %d, want 207 (body: %s)", target, w.Code, w.Body.String())
			}
			var ms internal.MultiStatus
			if err := xml.NewDecoder(w.Body).Decode(&ms); err != nil {
				t.Fatalf("PROPFIND %s Decode: %v", target, err)
			}
			if len(ms.Responses) != 1 {
				t.Errorf("PROPFIND %s: got %d responses, want 1", target, len(ms.Responses))
			}
		}
	})
}
