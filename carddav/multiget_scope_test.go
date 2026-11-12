package carddav

import (
	"context"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
)

// scopeBackend serves an address object at any path asked for, and records
// the paths.
type scopeBackend struct {
	testBackend
	paths []string
}

func (b *scopeBackend) GetAddressObject(ctx context.Context, path string, req *AddressDataRequest) (*AddressObject, error) {
	b.paths = append(b.paths, path)
	ao, err := b.testBackend.GetAddressObject(ctx, alicePath, req)
	if err != nil {
		return nil, err
	}
	ao.Path = path
	return ao, nil
}

// RFC 6352 section 8.7: the hrefs "all need to be members (not necessarily
// internal members) of the resource identified by the Request-URI". The
// others are answered like missing objects, without asking the backend for
// them, which may serve them to another address book's users.
func TestMultiGetOutOfScope(t *testing.T) {
	const (
		private = "/test/contacts/private/"
		shared  = "/test/contacts/shared/"
	)
	body := `<?xml version="1.0" encoding="UTF-8"?>
<C:addressbook-multiget xmlns:D="DAV:" xmlns:C="urn:ietf:params:xml:ns:carddav">
  <D:prop><D:getetag/></D:prop>
  <D:href>` + private + `alice.vcf</D:href>
  <D:href>` + shared + `alice.vcf</D:href>
  <D:href>` + private + `../shared/bob.vcf</D:href>
  <D:href>carol.vcf</D:href>
</C:addressbook-multiget>`

	t.Run("GetAddressObject", func(t *testing.T) {
		backend := &scopeBackend{}
		handler := Handler{Backend: backend}
		req := httptest.NewRequest("REPORT", private, strings.NewReader(body))
		req.Header.Set("Content-Type", "application/xml")
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, req)

		// Responses are for the hrefs resolved against the Request-URI,
		// with dot-segments removed (RFC 4918 section 8.3)
		checkStatusCodes(t, multiStatusCodes(t, w), map[string]int{
			private + "alice.vcf": http.StatusOK,
			shared + "alice.vcf":  http.StatusNotFound,
			shared + "bob.vcf":    http.StatusNotFound,
			private + "carol.vcf": http.StatusOK,
		})
		if want := []string{private + "alice.vcf", private + "carol.vcf"}; !reflect.DeepEqual(backend.paths, want) {
			t.Errorf("GetAddressObject paths = %v, want %v", backend.paths, want)
		}
	})

	t.Run("GetAddressObjects", func(t *testing.T) {
		backend := &multiGetBackend{}
		handler := Handler{Backend: backend}
		req := httptest.NewRequest("REPORT", shared, strings.NewReader(strings.ReplaceAll(body, private+"alice.vcf", multiGetAlicePath)))
		req.Header.Set("Content-Type", "application/xml")
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, req)

		// multiGetAlicePath is in private, not shared
		codes := multiStatusCodes(t, w)
		if code := codes[multiGetAlicePath]; code != http.StatusNotFound {
			t.Errorf("status of %v = %v, want 404", multiGetAlicePath, code)
		}
		for _, call := range backend.calls {
			for _, p := range call {
				if p == multiGetAlicePath {
					t.Errorf("GetAddressObjects called with %v, outside of %v", p, shared)
				}
			}
		}
	})
}

func TestMultiGetCollectionWithoutSlash(t *testing.T) {
	// A collection is often requested without its trailing slash
	handler := Handler{Backend: &scopeBackend{}}
	body := `<?xml version="1.0" encoding="UTF-8"?>
<C:addressbook-multiget xmlns:D="DAV:" xmlns:C="urn:ietf:params:xml:ns:carddav">
  <D:prop><D:getetag/></D:prop>
  <D:href>/test/contacts/private/alice.vcf</D:href>
</C:addressbook-multiget>`
	req := httptest.NewRequest("REPORT", "/test/contacts/private", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/xml")
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)
	checkStatusCodes(t, multiStatusCodes(t, w), map[string]int{"/test/contacts/private/alice.vcf": http.StatusOK})
}

// RFC 6352 section 10.7: <!ELEMENT addressbook-multiget ((DAV:allprop |
// DAV:propname | DAV:prop)?, DAV:href+)>
func TestMultiGetWithoutHref(t *testing.T) {
	handler := Handler{Backend: &scopeBackend{}}
	body := `<?xml version="1.0" encoding="UTF-8"?>
<C:addressbook-multiget xmlns:D="DAV:" xmlns:C="urn:ietf:params:xml:ns:carddav">
  <D:prop><D:getetag/></D:prop>
</C:addressbook-multiget>`
	req := httptest.NewRequest("REPORT", "/test/contacts/private/", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/xml")
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)
	if w.Code != http.StatusBadRequest {
		t.Errorf("status = %v, want 400; body:\n%s", w.Code, w.Body)
	}
}
