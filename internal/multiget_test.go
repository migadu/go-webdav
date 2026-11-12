package internal

import (
	"encoding/xml"
	"errors"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
)

func TestMultigetPaths(t *testing.T) {
	req := httptest.NewRequest("REPORT", "/dav/cal/", nil)
	got, err := MultigetPaths(req, []Href{
		{Path: "/dav/cal/a.ics"},
		{Path: "b.ics"},
		{Path: "./sub/../c.ics"},
		{Path: "../other/d.ics"},
		{Path: "/dav/cal/../cal/e.ics"},
		{Scheme: "https", Host: "dav.example.com", Path: "/dav/cal/f.ics"},
	})
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"/dav/cal/a.ics", "/dav/cal/b.ics", "/dav/cal/c.ics", "/dav/other/d.ics", "/dav/cal/e.ics", "/dav/cal/f.ics"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("MultigetPaths() = %q, want %q", got, want)
	}

	var httpErr *HTTPError
	if _, err := MultigetPaths(req, nil); !errors.As(err, &httpErr) || httpErr.Code != http.StatusBadRequest {
		t.Errorf("MultigetPaths(no href) = %v, want 400", err)
	}
}

// parseHrefs decodes hrefs as an XML request body does.
func parseHrefs(t *testing.T, l ...string) []Href {
	t.Helper()
	hrefs := make([]Href, len(l))
	for i, s := range l {
		if err := hrefs[i].UnmarshalText([]byte(s)); err != nil {
			t.Fatalf("UnmarshalText(%q) = %v", s, err)
		}
	}
	return hrefs
}

func TestMultigetPathsAbsoluteURIs(t *testing.T) {
	req := httptest.NewRequest("REPORT", "/dav/cal/event.ics", nil)
	for _, tc := range []struct{ href, want string }{
		// An http(s) URL with an empty path names "/" (RFC 3986 section
		// 6.2.3), as does a network-path reference (section 5.2.2): not the
		// Request-URI, as an empty reference would
		{"https://dav.example.com", "/"},
		{"HTTPS://dav.example.com", "/"},
		{"//dav.example.com", "/"},
		{"https://dav.example.com?q", "/"},
		{"https://dav.example.com/dav/cal/./event.ics", "/dav/cal/event.ics"},
		// Other URIs don't name a resource of this server: kept whole, they
		// are in the scope of no request
		{"urn:uuid:1234", "urn:uuid:1234"},
		{"mailto:alice@example.com", "mailto:alice@example.com"},
		{"https:event.ics", "https:event.ics"},
		{"ftp://dav.example.com/dav/cal/event.ics", "ftp://dav.example.com/dav/cal/event.ics"},
	} {
		got, err := MultigetPaths(req, parseHrefs(t, tc.href))
		if err != nil {
			t.Fatalf("MultigetPaths(%q) = %v", tc.href, err)
		}
		if got[0] != tc.want {
			t.Errorf("MultigetPaths(%q) = %q, want %q", tc.href, got[0], tc.want)
		}
		if IsInScope(req.URL.Path, got[0]) && tc.want != "/dav/cal/event.ics" {
			t.Errorf("MultigetPaths(%q) = %q is in the scope of %v", tc.href, got[0], req.URL.Path)
		}
	}
}

// A reference with an empty path resolves to the Request-URI, which is used
// without its dot-segments too (RFC 3986 section 5.2.4).
func TestMultigetPathsRequestURIDotSegments(t *testing.T) {
	req := httptest.NewRequest("REPORT", "/dav/cal/../cal/", nil)
	got, err := MultigetPaths(req, parseHrefs(t, "", "?q", "event.ics"))
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"/dav/cal/", "/dav/cal/", "/dav/cal/event.ics"}; !reflect.DeepEqual(got, want) {
		t.Errorf("MultigetPaths() = %q, want %q", got, want)
	}
}

// A response for a URI that isn't a path, such as a multiget href naming no
// resource of the server, carries that URI as is: RFC 4918 section 8.3
// allows an absolute URI in an href, but not a relative path such as
// "./urn:uuid:1234".
func TestNewErrorResponseURI(t *testing.T) {
	for _, tc := range []struct{ path, want string }{
		{"urn:uuid:1234", "<href>urn:uuid:1234</href>"},
		{"mailto:alice@example.com", "<href>mailto:alice@example.com</href>"},
		{"ftp://h/a", "<href>ftp://h/a</href>"},
		{"/a b/c:d", "<href>/a%20b/c:d</href>"},
	} {
		b, err := xml.Marshal(NewErrorResponse(tc.path, HTTPErrorf(http.StatusNotFound, "not found")))
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(string(b), tc.want) {
			t.Errorf("NewErrorResponse(%q) = %s, want %s", tc.path, b, tc.want)
		}
	}
}
