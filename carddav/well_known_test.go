package carddav

import (
	"context"
	"encoding/xml"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/emersion/go-webdav"
	"github.com/emersion/go-webdav/internal"
)

// wellKnownBackend is a testBackend whose CurrentUserPrincipal returns
// principal and err.
type wellKnownBackend struct {
	*testBackend
	principal string
	err       error
}

func (b wellKnownBackend) CurrentUserPrincipal(ctx context.Context) (string, error) {
	return b.principal, b.err
}

// RFC 6764 section 5: the .well-known URL redirects to the context path of
// the service. Without a current user there's no principal to redirect to,
// so it's the root of the handler, which reports that the user isn't
// authenticated, rather than "/.well-known/", which is no CardDAV resource.
func TestWellKnownUnauthenticated(t *testing.T) {
	for _, tc := range []struct{ name, prefix, location string }{
		{"no prefix", "", "/"},
		{"prefix", "/dav", "/dav/"},
		{"prefix with trailing slash", "/dav/", "/dav/"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := &Handler{Backend: wellKnownBackend{testBackend: &testBackend{}}, Prefix: tc.prefix}
			req := httptest.NewRequest("PROPFIND", "/.well-known/carddav", nil)
			req.Header.Set("Depth", "0")
			w := httptest.NewRecorder()
			h.ServeHTTP(w, req)
			if w.Code != http.StatusPermanentRedirect {
				t.Fatalf("status = %v, want 308; body:\n%s", w.Code, w.Body)
			}
			loc := w.Header().Get("Location")
			if loc != tc.location {
				t.Fatalf("Location = %q, want %q", loc, tc.location)
			}

			req = httptest.NewRequest("PROPFIND", loc, strings.NewReader(propFindUnauthenticated))
			req.Header.Set("Content-Type", "application/xml")
			req.Header.Set("Depth", "0")
			w = httptest.NewRecorder()
			h.ServeHTTP(w, req)
			if w.Code != http.StatusMultiStatus {
				t.Fatalf("PROPFIND %v: status = %v, want 207; body:\n%s", loc, w.Code, w.Body)
			}
			var ms internal.MultiStatus
			if err := xml.Unmarshal(w.Body.Bytes(), &ms); err != nil {
				t.Fatalf("failed to decode multistatus: %v\n%s", err, w.Body)
			}
			if len(ms.Responses) != 1 {
				t.Fatalf("got %d responses, want 1:\n%s", len(ms.Responses), w.Body)
			}
			var cup unauthenticatedCurrentUserPrincipal
			if err := ms.Responses[0].DecodeProp(&cup); err != nil {
				t.Errorf("current-user-principal: %v", err)
			} else if cup.Unauthenticated == nil || cup.Href != nil {
				t.Errorf("current-user-principal at %v isn't DAV:unauthenticated:\n%s", loc, w.Body)
			}
		})
	}
}

// RFC 6764 section 5 lets the server require authentication at the
// .well-known URL: an HTTPError from CurrentUserPrincipal, such as 401
// Unauthorized, keeps its status rather than becoming 500 Internal Server
// Error.
func TestWellKnownCurrentUserPrincipalError(t *testing.T) {
	for _, tc := range []struct {
		name string
		err  error
		code int
	}{
		{"unauthorized", webdav.NewHTTPError(http.StatusUnauthorized, nil), http.StatusUnauthorized},
		{"forbidden", webdav.NewHTTPError(http.StatusForbidden, errors.New("no access")), http.StatusForbidden},
		{"wrapped", fmt.Errorf("auth: %w", webdav.NewHTTPError(http.StatusUnauthorized, nil)), http.StatusUnauthorized},
		{"other error", errors.New("database unavailable"), http.StatusInternalServerError},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := &Handler{Backend: wellKnownBackend{testBackend: &testBackend{}, err: tc.err}}
			w := httptest.NewRecorder()
			h.ServeHTTP(w, httptest.NewRequest("PROPFIND", "/.well-known/carddav", nil))
			if w.Code != tc.code {
				t.Errorf("status = %v, want %v; body:\n%s", w.Code, tc.code, w.Body)
			}
			if loc := w.Header().Get("Location"); loc != "" {
				t.Errorf("Location = %q, want none", loc)
			}
		})
	}
}

// The redirect depends on the current user, so it mustn't be reused for
// another one: RFC 6764 section 5 asks for a Cache-Control value such as
// "no-cache" when the location may change, and a 308 response is cacheable
// by default (RFC 9110 section 15.4.9).
func TestWellKnownNoCache(t *testing.T) {
	for _, principal := range []string{"/principals/alice/", ""} {
		h := &Handler{Backend: wellKnownBackend{principal: principal}}
		w := httptest.NewRecorder()
		h.ServeHTTP(w, httptest.NewRequest("PROPFIND", "/.well-known/carddav", nil))
		if w.Code != http.StatusPermanentRedirect {
			t.Fatalf("principal %q: status = %v, want 308", principal, w.Code)
		}
		if cc := w.Header().Get("Cache-Control"); cc != "no-cache" {
			t.Errorf("principal %q: Cache-Control = %q, want %q", principal, cc, "no-cache")
		}
	}
}
