package internal

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// propFindRecorder is a Backend recording the PROPFIND requests it's given.
// Its other methods aren't implemented.
type propFindRecorder struct {
	Backend
	propfinds []*PropFind
}

func (b *propFindRecorder) PropFind(r *http.Request, pf *PropFind, depth Depth, mw *MultiStatusWriter) error {
	b.propfinds = append(b.propfinds, pf)
	return nil
}

// A PROPFIND request body with neither a propname, an allprop nor a prop
// element doesn't match the DTD of RFC 4918 section 14.20: it's refused
// before the backend is asked for any resource, so that the result doesn't
// depend on what the backend finds.
func TestHandlerPropFindWithoutProp(t *testing.T) {
	for _, tc := range []struct {
		name string
		body string
	}{
		{"empty propfind", `<D:propfind xmlns:D="DAV:"/>`},
		{"include only", `<D:propfind xmlns:D="DAV:"><D:include><D:getetag/></D:include></D:propfind>`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			b := &propFindRecorder{}
			h := Handler{Backend: b}
			req := httptest.NewRequest("PROPFIND", "/dir/", strings.NewReader(tc.body))
			req.Header.Set("Content-Type", "application/xml")
			req.Header.Set("Depth", "1")
			w := httptest.NewRecorder()
			h.ServeHTTP(w, req)

			if w.Code != http.StatusBadRequest {
				t.Errorf("PROPFIND returned %v, want 400: %s", w.Code, w.Body.String())
			}
			if len(b.propfinds) != 0 {
				t.Errorf("Backend.PropFind called %d times, want 0", len(b.propfinds))
			}
		})
	}

	// RFC 4918 section 9.1: an empty body is an allprop request
	t.Run("no body", func(t *testing.T) {
		b := &propFindRecorder{}
		h := Handler{Backend: b}
		req := httptest.NewRequest("PROPFIND", "/dir/", nil)
		req.Header.Set("Depth", "1")
		w := httptest.NewRecorder()
		h.ServeHTTP(w, req)

		if w.Code != http.StatusMultiStatus {
			t.Errorf("PROPFIND returned %v, want 207: %s", w.Code, w.Body.String())
		}
		if len(b.propfinds) != 1 || b.propfinds[0].AllProp == nil {
			t.Errorf("Backend.PropFind calls = %v, want one allprop request", b.propfinds)
		}
	})
}
