package webdav_test

import (
	"context"
	"encoding/xml"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/emersion/go-webdav"
	"github.com/emersion/go-webdav/internal"
)

const testPrincipalPath = "/principal/"

// servePrincipal sends a request to ServePrincipal for testPrincipalPath. An
// empty body is sent without a Content-Type header, and an empty depth
// without a Depth header.
func servePrincipal(opts *webdav.ServePrincipalOptions, method, depth, body string) *httptest.ResponseRecorder {
	var r io.Reader
	if body != "" {
		r = strings.NewReader(body)
	}
	req := httptest.NewRequest(method, testPrincipalPath, r)
	if body != "" {
		req.Header.Set("Content-Type", "application/xml")
	}
	if depth != "" {
		req.Header.Set("Depth", depth)
	}
	w := httptest.NewRecorder()
	webdav.ServePrincipal(w, req, opts)
	return w
}

// decodePrincipalResponses decodes the responses of a multi-status response.
func decodePrincipalResponses(t *testing.T, w *httptest.ResponseRecorder) []internal.Response {
	t.Helper()
	if w.Code != http.StatusMultiStatus {
		t.Fatalf("status = %v, want 207; body:\n%s", w.Code, w.Body)
	}
	var ms internal.MultiStatus
	if err := xml.Unmarshal(w.Body.Bytes(), &ms); err != nil {
		t.Fatalf("failed to decode multistatus: %v\n%s", err, w.Body)
	}
	return ms.Responses
}

type testResourceType struct {
	XMLName   xml.Name  `xml:"DAV: resourcetype"`
	Principal *struct{} `xml:"DAV: principal"`
}

type testDisplayName struct {
	XMLName xml.Name `xml:"DAV: displayname"`
	Name    string   `xml:",chardata"`
}

// RFC 4918 section 9.1: an empty PROPFIND request body must be treated as an
// allprop request, whatever its Content-Type.
func TestServePrincipalEmptyBody(t *testing.T) {
	opts := &webdav.ServePrincipalOptions{CurrentUserPrincipalPath: testPrincipalPath}
	for name, contentType := range map[string]string{
		"no Content-Type":  "",
		"application/xml":  "application/xml",
		"text/xml charset": `text/xml; charset="utf-8"`,
	} {
		t.Run(name, func(t *testing.T) {
			req := httptest.NewRequest("PROPFIND", testPrincipalPath, nil)
			if contentType != "" {
				req.Header.Set("Content-Type", contentType)
			}
			req.Header.Set("Depth", "0")
			w := httptest.NewRecorder()
			webdav.ServePrincipal(w, req, opts)

			resps := decodePrincipalResponses(t, w)
			if len(resps) != 1 {
				t.Fatalf("got %d responses, want 1:\n%s", len(resps), w.Body)
			}
			// allprop returns the RFC 4918 live properties
			var rt testResourceType
			if err := resps[0].DecodeProp(&rt); err != nil {
				t.Fatalf("resourcetype: %v", err)
			} else if rt.Principal == nil {
				t.Errorf("resourcetype isn't DAV:principal:\n%s", w.Body)
			}
			var dn testDisplayName
			if err := resps[0].DecodeProp(&dn); err != nil {
				t.Fatalf("displayname: %v", err)
			} else if dn.Name != "principal" {
				t.Errorf("displayname = %q, want %q", dn.Name, "principal")
			}
		})
	}
}

// RFC 4918 section 9.1: servers must support Depth: 0 and 1, and should
// support infinity, which is also the default. The principal has no members,
// so each of them returns the principal alone. An invalid Depth value is a
// bad request, as for webdav.Handler.
func TestServePrincipalDepth(t *testing.T) {
	opts := &webdav.ServePrincipalOptions{CurrentUserPrincipalPath: testPrincipalPath}
	for _, depth := range []string{"", "0", "1", "infinity"} {
		w := servePrincipal(opts, "PROPFIND", depth, propFindCurrentUserPrincipal)
		if resps := decodePrincipalResponses(t, w); len(resps) != 1 {
			t.Errorf("Depth %q: got %d responses, want 1:\n%s", depth, len(resps), w.Body)
		}
	}
	for _, depth := range []string{"2", "-1", "bogus"} {
		w := servePrincipal(opts, "PROPFIND", depth, propFindCurrentUserPrincipal)
		if w.Code != http.StatusBadRequest {
			t.Errorf("Depth %q: status = %v, want 400; body:\n%s", depth, w.Code, w.Body)
		}
	}
}

// RFC 9110 section 10.2.1: the Allow header field lists the methods the
// resource supports, and a 405 response must carry it (section 15.5.6).
func TestServePrincipalAllow(t *testing.T) {
	opts := &webdav.ServePrincipalOptions{CurrentUserPrincipalPath: testPrincipalPath}
	w := servePrincipal(opts, http.MethodOptions, "", "")
	if w.Code != http.StatusNoContent {
		t.Fatalf("OPTIONS status = %v, want 204", w.Code)
	}
	allowHeader := w.Header().Get("Allow")
	allowed := make(map[string]bool)
	for _, m := range strings.Split(allowHeader, ",") {
		allowed[strings.TrimSpace(m)] = true
	}

	for _, method := range []string{"PROPFIND", "REPORT", "DELETE", "MKCOL", "PROPPATCH", http.MethodGet, http.MethodPut, http.MethodPost} {
		w := servePrincipal(opts, method, "0", propFindCurrentUserPrincipal)
		if allowed[method] && w.Code == http.StatusMethodNotAllowed {
			t.Errorf("Allow: %v, but %v returned 405", allowHeader, method)
		} else if !allowed[method] && w.Code != http.StatusMethodNotAllowed {
			t.Errorf("Allow: %v, but %v returned %v", allowHeader, method, w.Code)
		}
		if w.Code == http.StatusMethodNotAllowed {
			if got := w.Header().Get("Allow"); got != allowHeader {
				t.Errorf("%v: 405 response with Allow: %q, want %q", method, got, allowHeader)
			}
		}
	}
	if !allowed["PROPFIND"] || !allowed[http.MethodOptions] {
		t.Errorf("Allow: %v, want OPTIONS and PROPFIND", allowHeader)
	}
}

const propFindPrincipalIdentity = `<?xml version="1.0" encoding="utf-8"?>
<d:propfind xmlns:d="DAV:">
  <d:prop>
    <d:current-user-principal/>
    <d:principal-URL/>
    <d:owner/>
    <d:displayname/>
  </d:prop>
</d:propfind>`

type testCurrentUserPrincipal struct {
	XMLName         xml.Name  `xml:"DAV: current-user-principal"`
	Href            *string   `xml:"DAV: href"`
	Unauthenticated *struct{} `xml:"DAV: unauthenticated"`
}

type testPrincipalURL struct {
	XMLName xml.Name `xml:"DAV: principal-URL"`
	Href    *string  `xml:"DAV: href"`
}

type testOwner struct {
	XMLName xml.Name `xml:"DAV: owner"`
	Href    *string  `xml:"DAV: href"`
}

// RFC 5397 section 3: without an authenticated user, DAV:current-user-principal
// is DAV:unauthenticated, not an empty href. The principal served is the one
// at the request URL: it's its own DAV:principal-URL (RFC 3744 section 4.2)
// and DAV:owner. A nil options pointer is the same as empty options.
func TestServePrincipalUnauthenticated(t *testing.T) {
	for name, opts := range map[string]*webdav.ServePrincipalOptions{
		"empty CurrentUserPrincipalPath": {},
		"nil options":                    nil,
	} {
		t.Run(name, func(t *testing.T) {
			w := servePrincipal(opts, "PROPFIND", "0", propFindPrincipalIdentity)
			resps := decodePrincipalResponses(t, w)
			if len(resps) != 1 {
				t.Fatalf("got %d responses, want 1:\n%s", len(resps), w.Body)
			}
			resp := &resps[0]
			body := w.Body.String()
			if strings.Contains(body, "<href></href>") {
				t.Errorf("response has an empty href:\n%s", body)
			}

			var cup testCurrentUserPrincipal
			if err := resp.DecodeProp(&cup); err != nil {
				t.Fatalf("current-user-principal: %v\n%s", err, body)
			}
			if cup.Unauthenticated == nil || cup.Href != nil {
				t.Errorf("current-user-principal isn't DAV:unauthenticated:\n%s", body)
			}

			var principalURL testPrincipalURL
			if err := resp.DecodeProp(&principalURL); err != nil {
				t.Errorf("principal-URL: %v", err)
			} else if principalURL.Href == nil || *principalURL.Href != testPrincipalPath {
				t.Errorf("principal-URL isn't %v:\n%s", testPrincipalPath, body)
			}
			var owner testOwner
			if err := resp.DecodeProp(&owner); err != nil {
				t.Errorf("owner: %v", err)
			} else if owner.Href == nil || *owner.Href != testPrincipalPath {
				t.Errorf("owner isn't %v:\n%s", testPrincipalPath, body)
			}

			var dn testDisplayName
			if err := resp.DecodeProp(&dn); err != nil {
				t.Errorf("displayname: %v", err)
			} else if dn.Name != "principal" {
				t.Errorf("displayname = %q, want %q", dn.Name, "principal")
			}
		})
	}
}

// RFC 3744 section 4.2: DAV:principal-URL identifies the principal served,
// which owns itself (section 5.1). A principal other than the current
// user's, such as one whose DAV:displayname a client looks up (section 5.8),
// reports its own path, and DAV:current-user-principal still names the
// current user. The current user's principal requested without its trailing
// slash reports CurrentUserPrincipalPath, which RFC 5397 section 3 requires
// to be one of its URLs.
func TestServePrincipalOtherPrincipal(t *testing.T) {
	const alice = "/principals/alice/"
	opts := &webdav.ServePrincipalOptions{CurrentUserPrincipalPath: alice}
	for _, tc := range []struct {
		name, target, principal, displayName string
	}{
		{"other principal", "/principals/bob/", "/principals/bob/", "bob"},
		{"current user", alice, alice, "alice"},
		{"current user without trailing slash", "/principals/alice", alice, "alice"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			w := servePrincipalAt(opts, "PROPFIND", tc.target, propFindPrincipalIdentity)
			resps := decodePrincipalResponses(t, w)
			if len(resps) != 1 {
				t.Fatalf("got %d responses, want 1:\n%s", len(resps), w.Body)
			}
			resp := &resps[0]
			body := w.Body.String()
			if p := resp.Hrefs[0].Path; p != tc.target {
				t.Errorf("response is for %v, want %v", p, tc.target)
			}

			var cup testCurrentUserPrincipal
			if err := resp.DecodeProp(&cup); err != nil {
				t.Errorf("current-user-principal: %v", err)
			} else if cup.Href == nil || *cup.Href != alice {
				t.Errorf("current-user-principal isn't %v:\n%s", alice, body)
			}
			var principalURL testPrincipalURL
			if err := resp.DecodeProp(&principalURL); err != nil {
				t.Errorf("principal-URL: %v", err)
			} else if principalURL.Href == nil || *principalURL.Href != tc.principal {
				t.Errorf("principal-URL isn't %v:\n%s", tc.principal, body)
			}
			var owner testOwner
			if err := resp.DecodeProp(&owner); err != nil {
				t.Errorf("owner: %v", err)
			} else if owner.Href == nil || *owner.Href != tc.principal {
				t.Errorf("owner isn't %v:\n%s", tc.principal, body)
			}
			var dn testDisplayName
			if err := resp.DecodeProp(&dn); err != nil {
				t.Errorf("displayname: %v", err)
			} else if dn.Name != tc.displayName {
				t.Errorf("displayname = %q, want %q", dn.Name, tc.displayName)
			}
		})
	}
}

// A client asking ServePrincipal for the current user's principal without
// being authenticated is told so, rather than given an empty path.
func TestServePrincipalUnauthenticatedClient(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		webdav.ServePrincipal(w, r, &webdav.ServePrincipalOptions{})
	}))
	defer srv.Close()

	c, err := webdav.NewClient(nil, srv.URL+testPrincipalPath)
	if err != nil {
		t.Fatal(err)
	}
	p, err := c.FindCurrentUserPrincipal(context.Background())
	if err == nil {
		t.Errorf("FindCurrentUserPrincipal() = %q, want an error", p)
	}
}

// A nil options pointer is the same as empty options.
func TestServePrincipalOPTIONSNilOptions(t *testing.T) {
	w := servePrincipal(nil, http.MethodOptions, "", "")
	if w.Code != http.StatusNoContent {
		t.Fatalf("OPTIONS status = %v, want 204", w.Code)
	}
	if got, want := w.Header().Get("DAV"), "1, 3"; got != want {
		t.Errorf("DAV: %q, want %q", got, want)
	}
}
