package carddav

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestResourceTypeAtPathWithPrefix(t *testing.T) {
	b := backend{
		Prefix: "/dav",
	}

	for _, p := range []string{
		"/",
		"/other",
		"/test/",
		"/test/contacts/work/",
		"/test/contacts/work/alice.vcf",
		"/dav2",
		"/dav2/test/contacts/work",
		"/dav2/test/contacts/work/alice.vcf",
	} {
		got := b.resourceTypeAtPath(p)
		if got >= resourceTypeRoot {
			t.Errorf("resourceTypeAtPath(%q) = %v, want out-of-prefix sentinel", p, got)
		}
	}

	inPrefixCases := []struct {
		path string
		want resourceType
	}{
		{"/dav", resourceTypeRoot},
		{"/dav/", resourceTypeRoot},
		{"/dav/test/", resourceTypeUserPrincipal},
		{"/dav/test/contacts/", resourceTypeAddressBookHomeSet},
		{"/dav/test/contacts/work/", resourceTypeAddressBook},
		{"/dav/test/contacts/work/alice.vcf", resourceTypeAddressObject},
	}
	for _, tc := range inPrefixCases {
		if got := b.resourceTypeAtPath(tc.path); got != tc.want {
			t.Errorf("resourceTypeAtPath(%q) = %v, want %v", tc.path, got, tc.want)
		}
	}
}

type prefixSpyCardBackend struct {
	testBackend
	calledPaths []string
}

func (b *prefixSpyCardBackend) CurrentUserPrincipal(ctx context.Context) (string, error) {
	return "/dav/test/", nil
}

func (b *prefixSpyCardBackend) AddressBookHomeSetPath(ctx context.Context) (string, error) {
	return "/dav/test/contacts/", nil
}

func (b *prefixSpyCardBackend) GetAddressObject(ctx context.Context, path string, req *AddressDataRequest) (*AddressObject, error) {
	b.calledPaths = append(b.calledPaths, path)
	return b.testBackend.GetAddressObject(ctx, path, req)
}

func TestHandlerRejectsOutOfPrefixRequests(t *testing.T) {
	spy := &prefixSpyCardBackend{}
	h := &Handler{Backend: spy, Prefix: "/dav/"}

	outOfPrefix := []struct {
		method string
		target string
		body   string
	}{
		{"PROPFIND", "/", ""},
		{"PROPFIND", "/test/", ""},
		{"PROPFIND", "/dav2/test/contacts/work/", ""},
		{"OPTIONS", "/", ""},
		{"OPTIONS", "/dav2/test/contacts/work/alice.vcf", ""},
		{"GET", "/test/contacts/work/alice.vcf", ""},
		{"PUT", "/test/contacts/work/alice.vcf", "BEGIN:VCARD\r\nVERSION:3.0\r\nFN:Alice\r\nEND:VCARD\r\n"},
		{"DELETE", "/test/contacts/work/alice.vcf", ""},
		{"MKCOL", "/test/contacts/new/", ""},
		{"MKCOL", "/dav2/test/contacts/new/", ""},
		{"REPORT", "/test/contacts/work/", `<C:addressbook-query xmlns:D="DAV:" xmlns:C="urn:ietf:params:xml:ns:carddav"><D:prop><D:getetag/></D:prop><C:filter/></C:addressbook-query>`},
	}

	for _, tc := range outOfPrefix {
		req := httptest.NewRequest(tc.method, tc.target, strings.NewReader(tc.body))
		if tc.body != "" {
			if tc.method == "PUT" {
				req.Header.Set("Content-Type", "text/vcard")
			} else {
				req.Header.Set("Content-Type", "application/xml")
			}
		}
		w := httptest.NewRecorder()
		h.ServeHTTP(w, req)
		if w.Code != http.StatusNotFound {
			t.Errorf("%s %s: got status %d, want 404", tc.method, tc.target, w.Code)
		}
	}

	// Multiget inside prefix with an out-of-prefix href must not pass out-of-prefix path to backend.
	spy.calledPaths = nil
	multigetBody := `<C:addressbook-multiget xmlns:D="DAV:" xmlns:C="urn:ietf:params:xml:ns:carddav"><D:prop><D:getetag/></D:prop><D:href>/other/alice.vcf</D:href></C:addressbook-multiget>`
	req := httptest.NewRequest("REPORT", "/dav/test/contacts/work/", strings.NewReader(multigetBody))
	req.Header.Set("Content-Type", "application/xml")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	if w.Code != http.StatusMultiStatus {
		t.Fatalf("multiget REPORT got %d, want 207: %s", w.Code, w.Body.String())
	}
	if len(spy.calledPaths) > 0 {
		t.Errorf("backend GetAddressObject called with out-of-prefix paths: %v", spy.calledPaths)
	}
	if !strings.Contains(w.Body.String(), "404") {
		t.Errorf("expected 404 in multiget response for out-of-prefix href, got:\n%s", w.Body.String())
	}

	// /.well-known/carddav must still redirect even when Prefix is configured.
	wkReq := httptest.NewRequest("GET", "/.well-known/carddav", nil)
	wkW := httptest.NewRecorder()
	h.ServeHTTP(wkW, wkReq)
	if wkW.Code != http.StatusPermanentRedirect {
		t.Errorf("GET /.well-known/carddav: got %d, want 308", wkW.Code)
	}

	// In-prefix root PROPFIND must succeed with 207.
	inReq := httptest.NewRequest("PROPFIND", "/dav/", nil)
	inReq.Header.Set("Depth", "0")
	inW := httptest.NewRecorder()
	h.ServeHTTP(inW, inReq)
	if inW.Code != http.StatusMultiStatus {
		t.Errorf("PROPFIND /dav/: got %d, want 207: %s", inW.Code, inW.Body.String())
	}
}
