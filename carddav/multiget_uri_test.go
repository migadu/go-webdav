package carddav

import (
	"encoding/xml"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"

	"github.com/emersion/go-webdav/internal"
)

// An http(s) URL with an empty path names "/", and a URI of another scheme
// no resource of the server (RFC 3986 sections 5.2.2 and 6.2.3): neither is
// a member of the address book the REPORT is for (RFC 6352 section 8.7).
func TestMultiGetAbsoluteURIs(t *testing.T) {
	const book = "/test/contacts/private/"
	backend := &scopeBackend{}
	handler := Handler{Backend: backend}
	body := `<?xml version="1.0" encoding="UTF-8"?>
<C:addressbook-multiget xmlns:D="DAV:" xmlns:C="urn:ietf:params:xml:ns:carddav">
  <D:prop><D:getetag/></D:prop>
  <D:href>https://dav.example.com</D:href>
  <D:href>//dav.example.com</D:href>
  <D:href>urn:uuid:1234</D:href>
  <D:href>alice.vcf</D:href>
</C:addressbook-multiget>`
	req := httptest.NewRequest("REPORT", book, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/xml")
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	if w.Code != http.StatusMultiStatus {
		t.Fatalf("status = %d, want 207; body:\n%s", w.Code, w.Body)
	}
	var ms internal.MultiStatus
	if err := xml.NewDecoder(w.Body).Decode(&ms); err != nil {
		t.Fatal(err)
	}
	got := make(map[string]int)
	for _, resp := range ms.Responses {
		code := http.StatusOK
		if resp.Status != nil {
			code = resp.Status.Code
		}
		got[resp.Hrefs[0].String()] = code
	}
	want := map[string]int{
		"/":                http.StatusNotFound,
		"urn:uuid:1234":    http.StatusNotFound,
		book + "alice.vcf": http.StatusOK,
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("response statuses = %v, want %v", got, want)
	}
	if want := []string{book + "alice.vcf"}; !reflect.DeepEqual(backend.paths, want) {
		t.Errorf("GetAddressObject paths = %v, want %v", backend.paths, want)
	}
}
