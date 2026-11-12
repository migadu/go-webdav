package carddav

import (
	"context"
	"encoding/xml"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/emersion/go-webdav/internal"
)

const propFindUnauthenticated = `<?xml version="1.0" encoding="utf-8"?>
<d:propfind xmlns:d="DAV:">
  <d:prop>
    <d:current-user-principal/>
    <d:owner/>
    <d:principal-collection-set/>
  </d:prop>
</d:propfind>`

type unauthenticatedCurrentUserPrincipal struct {
	XMLName         xml.Name  `xml:"DAV: current-user-principal"`
	Href            *string   `xml:"DAV: href"`
	Unauthenticated *struct{} `xml:"DAV: unauthenticated"`
}

// RFC 5397 section 3: when the backend has no current user, such as for a
// public address book, DAV:current-user-principal is DAV:unauthenticated,
// not an empty href. DAV:owner and DAV:principal-collection-set are empty
// rather than holding an empty href (RFC 3744 sections 5.1 and 5.8).
func TestPropFindUnauthenticated(t *testing.T) {
	const (
		homeSet = "/public/contacts/"
		abPath  = "/public/contacts/default/"
	)
	h := &Handler{Backend: &testBackend{}}

	for _, tc := range []struct {
		name, path, depth string
		n                 int // responses
	}{
		{"root", "/", "0", 1},
		{"home set", homeSet, "0", 1},
		{"address book and object", abPath, "1", 2},
	} {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest("PROPFIND", tc.path, strings.NewReader(propFindUnauthenticated))
			req.Header.Set("Content-Type", "application/xml")
			req.Header.Set("Depth", tc.depth)
			ctx := context.WithValue(req.Context(), currentUserPrincipalKey, "")
			ctx = context.WithValue(ctx, homeSetPathKey, homeSet)
			ctx = context.WithValue(ctx, addressBookPathKey, abPath)
			w := httptest.NewRecorder()
			h.ServeHTTP(w, req.WithContext(ctx))
			if w.Code != http.StatusMultiStatus {
				t.Fatalf("status = %v, want 207; body:\n%s", w.Code, w.Body)
			}
			body := w.Body.String()
			if strings.Contains(body, "<href></href>") {
				t.Errorf("response has an empty href:\n%s", body)
			}

			var ms internal.MultiStatus
			if err := xml.Unmarshal(w.Body.Bytes(), &ms); err != nil {
				t.Fatalf("failed to decode multistatus: %v\n%s", err, body)
			}
			if len(ms.Responses) != tc.n {
				t.Fatalf("got %d responses, want %d:\n%s", len(ms.Responses), tc.n, body)
			}
			for i := range ms.Responses {
				resp := &ms.Responses[i]
				p := resp.Hrefs[0].Path
				var cup unauthenticatedCurrentUserPrincipal
				if err := resp.DecodeProp(&cup); err != nil {
					t.Errorf("%v: current-user-principal: %v", p, err)
				} else if cup.Unauthenticated == nil || cup.Href != nil {
					t.Errorf("%v: current-user-principal isn't DAV:unauthenticated:\n%s", p, body)
				}
			}
		})
	}
}
