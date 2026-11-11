package carddav

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// noCallBackend is a Backend none of whose methods can be called: they all
// panic.
type noCallBackend struct {
	Backend
}

// serveNoCall serves req with a Handler whose backend can't be called, and
// fails the test if the handler calls it.
func serveNoCall(t *testing.T, req *http.Request) *httptest.ResponseRecorder {
	t.Helper()
	w := httptest.NewRecorder()
	func() {
		defer func() {
			if v := recover(); v != nil {
				t.Errorf("%v %v called the backend: %v", req.Method, req.URL.Path, v)
			}
		}()
		h := Handler{Backend: noCallBackend{}}
		h.ServeHTTP(w, req)
	}()
	return w
}

// A PROPFIND request asking for no properties (no propname, allprop or prop
// element, RFC 4918 section 14.20) is refused before the backend is called,
// whatever the resource.
func TestPropFindWithoutProp(t *testing.T) {
	for _, p := range []string{
		"/",
		"/user/",
		"/user/contacts/",
		"/user/contacts/default/",
		"/user/contacts/default/alice.vcf",
	} {
		body := `<?xml version="1.0" encoding="UTF-8"?><D:propfind xmlns:D="DAV:"/>`
		req := httptest.NewRequest("PROPFIND", p, strings.NewReader(body))
		req.Header.Set("Content-Type", "application/xml")
		req.Header.Set("Depth", "1")
		if w := serveNoCall(t, req); w.Code != http.StatusBadRequest {
			t.Errorf("PROPFIND %v returned %v, want 400: %s", p, w.Code, w.Body.String())
		}
	}
}

const addressBookQueryWithoutProp = `<?xml version="1.0" encoding="UTF-8"?>
<C:addressbook-query xmlns:D="DAV:" xmlns:C="urn:ietf:params:xml:ns:carddav">
  <C:filter/>
</C:addressbook-query>`

// noMatchBackend is a testBackend whose queries match no address object.
type noMatchBackend struct {
	testBackend
}

func (*noMatchBackend) QueryAddressObjects(ctx context.Context, path string, query *AddressBookQuery) ([]AddressObject, error) {
	return nil, nil
}

// An addressbook-query asking for no properties is refused before the
// backend is queried, like an addressbook-multiget: the request fails the
// same way whether or not objects match.
func TestQueryWithoutProp(t *testing.T) {
	const abPath = "/user/contacts/default/"

	t.Run("no backend call", func(t *testing.T) {
		req := httptest.NewRequest("REPORT", abPath, strings.NewReader(addressBookQueryWithoutProp))
		req.Header.Set("Content-Type", "application/xml")
		if w := serveNoCall(t, req); w.Code != http.StatusBadRequest {
			t.Errorf("REPORT returned %v, want 400: %s", w.Code, w.Body.String())
		}
	})

	t.Run("no match", func(t *testing.T) {
		handler := Handler{Backend: &noMatchBackend{}}
		req := httptest.NewRequest("REPORT", abPath, strings.NewReader(addressBookQueryWithoutProp))
		req.Header.Set("Content-Type", "application/xml")
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, req)
		if w.Code != http.StatusBadRequest {
			t.Errorf("REPORT returned %v, want 400: %s", w.Code, w.Body.String())
		}
	})

	// A limit of 0 matches nothing without asking the backend
	t.Run("limit 0", func(t *testing.T) {
		body := `<?xml version="1.0" encoding="UTF-8"?>
<C:addressbook-query xmlns:D="DAV:" xmlns:C="urn:ietf:params:xml:ns:carddav">
  <C:filter/>
  <C:limit><C:nresults>0</C:nresults></C:limit>
</C:addressbook-query>`
		req := httptest.NewRequest("REPORT", abPath, strings.NewReader(body))
		req.Header.Set("Content-Type", "application/xml")
		if w := serveNoCall(t, req); w.Code != http.StatusBadRequest {
			t.Errorf("REPORT returned %v, want 400: %s", w.Code, w.Body.String())
		}
	})
}
