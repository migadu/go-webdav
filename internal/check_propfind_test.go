package internal

import (
	"encoding/xml"
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

// A propfind element holds either a propname, an allprop optionally followed
// by an include, or a prop element (RFC 4918 section 14.20). Any other
// combination is refused before the backend is asked for any resource,
// rather than served as whichever of them is checked first.
func TestHandlerPropFindExclusiveElements(t *testing.T) {
	for _, tc := range []struct {
		name string
		body string
		want int
	}{
		{"allprop and prop", `<D:allprop/><D:prop><D:getetag/></D:prop>`, http.StatusBadRequest},
		{"propname and allprop", `<D:propname/><D:allprop/>`, http.StatusBadRequest},
		{"propname and prop", `<D:propname/><D:prop><D:getetag/></D:prop>`, http.StatusBadRequest},
		{"all three", `<D:propname/><D:allprop/><D:prop><D:getetag/></D:prop>`, http.StatusBadRequest},
		{"prop and include", `<D:prop><D:getetag/></D:prop><D:include><D:getetag/></D:include>`, http.StatusBadRequest},
		{"propname and include", `<D:propname/><D:include><D:getetag/></D:include>`, http.StatusBadRequest},
		{"allprop and include", `<D:allprop/><D:include><D:getetag/></D:include>`, http.StatusMultiStatus},
	} {
		t.Run(tc.name, func(t *testing.T) {
			b := &propFindRecorder{}
			h := Handler{Backend: b}
			body := `<D:propfind xmlns:D="DAV:">` + tc.body + `</D:propfind>`
			req := httptest.NewRequest("PROPFIND", "/dir/", strings.NewReader(body))
			req.Header.Set("Content-Type", "application/xml")
			req.Header.Set("Depth", "1")
			w := httptest.NewRecorder()
			h.ServeHTTP(w, req)

			if w.Code != tc.want {
				t.Errorf("PROPFIND returned %v, want %v: %s", w.Code, tc.want, w.Body.String())
			}
			calls := 0
			if tc.want == http.StatusMultiStatus {
				calls = 1
			}
			if len(b.propfinds) != calls {
				t.Errorf("Backend.PropFind called %d times, want %d", len(b.propfinds), calls)
			}
		})
	}
}

// An empty prop element is valid (RFC 4918 section 14.18: <!ELEMENT prop
// ANY >) and asks for no property. Since a response holds either a status or
// at least one propstat (section 14.24), the resource gets a 200 OK status,
// as for a REPORT without a prop element.
func TestNewPropFindResponseEmptyProp(t *testing.T) {
	deadName := xml.Name{Space: "urn:example", Local: "dead"}
	for _, body := range []string{
		`<D:propfind xmlns:D="DAV:"><D:prop/></D:propfind>`,
		`<D:propfind xmlns:D="DAV:"><D:prop>
		</D:prop></D:propfind>`,
	} {
		var propfind PropFind
		if err := xml.Unmarshal([]byte(body), &propfind); err != nil {
			t.Fatal(err)
		}
		props := map[xml.Name]PropFindFunc{
			GetETagName: PropFindValue(&GetETag{ETag: "1"}),
		}
		deadProps := map[xml.Name]PropFindFunc{
			deadName: PropFindValue(NewRawXMLElement(deadName, nil, nil)),
		}
		resp, err := NewPropFindResponseWithDeadProps("/dir/a", &propfind, props, deadProps)
		if err != nil {
			t.Fatalf("%v: %v", body, err)
		}
		if resp.Status == nil || resp.Status.Code != http.StatusOK || len(resp.PropStats) != 0 {
			b, _ := xml.Marshal(resp)
			t.Errorf("%v: got response %s, want a 200 OK status without propstat", body, b)
		}
	}
}
