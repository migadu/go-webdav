package carddav

import (
	"context"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"

	"github.com/emersion/go-vcard"
	"github.com/migadu/go-webdav"
)

// objectBackend holds address objects at the paths in objects.
type objectBackend struct {
	testBackend
	objects map[string]bool
}

func (b *objectBackend) GetAddressObject(ctx context.Context, path string, req *AddressDataRequest) (*AddressObject, error) {
	if !b.objects[path] {
		return nil, webdav.NewHTTPError(http.StatusNotFound, nil)
	}
	return &AddressObject{Path: path, Card: vcard.Card{}}, nil
}

// copyMoveCall is a call to CopyAddressObject or MoveAddressObject.
type copyMoveCall struct {
	method      string
	src, dst    string
	noOverwrite bool
}

// copyMoveBackend records the address objects it is asked to copy and move.
type copyMoveBackend struct {
	objectBackend
	calls []copyMoveCall
}

func (b *copyMoveBackend) CopyAddressObject(ctx context.Context, src, dst string, opts *CopyAddressObjectOptions) (created bool, err error) {
	return b.copyMove("COPY", src, dst, opts.NoOverwrite)
}

func (b *copyMoveBackend) MoveAddressObject(ctx context.Context, src, dst string, opts *MoveAddressObjectOptions) (created bool, err error) {
	return b.copyMove("MOVE", src, dst, opts.NoOverwrite)
}

func (b *copyMoveBackend) copyMove(method, src, dst string, noOverwrite bool) (created bool, err error) {
	b.calls = append(b.calls, copyMoveCall{method, src, dst, noOverwrite})
	if !b.objects[src] {
		return false, webdav.NewHTTPError(http.StatusNotFound, nil)
	}
	if b.objects[dst] {
		if noOverwrite {
			return false, webdav.NewHTTPError(http.StatusPreconditionFailed, nil)
		}
		return false, nil
	}
	return true, nil
}

const (
	copyMoveAlice = "/test/contacts/private/alice.vcf"
	copyMoveBob   = "/test/contacts/work/bob.vcf"
)

func newCopyMoveBackend() *copyMoveBackend {
	return &copyMoveBackend{objectBackend: objectBackend{objects: map[string]bool{
		copyMoveAlice:          true,
		copyMoveBob:            true,
		"/dav" + copyMoveAlice: true,
		"/dav" + copyMoveBob:   true,
	}}}
}

func serveCopyMove(h *Handler, method, src, dest, overwrite string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, src, nil)
	req.Header.Set("Destination", dest)
	if overwrite != "" {
		req.Header.Set("Overwrite", overwrite)
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	return w
}

// RFC 6352 section 6.3.2.1: address objects can be copied or moved into an
// address book, for instance to move a contact from an address book to
// another.
func TestCopyMoveAddressObject(t *testing.T) {
	for _, tc := range []struct {
		name              string
		prefix            string
		method, src, dest string
		overwrite         string
		wantCode          int
		wantCall          *copyMoveCall
	}{
		{
			name:     "move to another address book",
			method:   "MOVE",
			src:      copyMoveAlice,
			dest:     "http://example.com/test/contacts/work/alice.vcf",
			wantCode: http.StatusCreated,
			wantCall: &copyMoveCall{"MOVE", copyMoveAlice, "/test/contacts/work/alice.vcf", false},
		},
		{
			name:     "copy to another address book",
			method:   "COPY",
			src:      copyMoveAlice,
			dest:     "/test/contacts/work/alice.vcf",
			wantCode: http.StatusCreated,
			wantCall: &copyMoveCall{"COPY", copyMoveAlice, "/test/contacts/work/alice.vcf", false},
		},
		{
			name:     "move over an existing object",
			method:   "MOVE",
			src:      copyMoveAlice,
			dest:     copyMoveBob,
			wantCode: http.StatusNoContent,
			wantCall: &copyMoveCall{"MOVE", copyMoveAlice, copyMoveBob, false},
		},
		{
			name:      "copy over an existing object",
			method:    "COPY",
			src:       copyMoveAlice,
			dest:      copyMoveBob,
			overwrite: "T",
			wantCode:  http.StatusNoContent,
			wantCall:  &copyMoveCall{"COPY", copyMoveAlice, copyMoveBob, false},
		},
		{
			name:      "copy without overwriting an existing object",
			method:    "COPY",
			src:       copyMoveAlice,
			dest:      copyMoveBob,
			overwrite: "F",
			wantCode:  http.StatusPreconditionFailed,
			wantCall:  &copyMoveCall{"COPY", copyMoveAlice, copyMoveBob, true},
		},
		{
			name:      "move without overwriting to a new object",
			method:    "MOVE",
			src:       copyMoveAlice,
			dest:      "/test/contacts/work/alice.vcf",
			overwrite: "F",
			wantCode:  http.StatusCreated,
			wantCall:  &copyMoveCall{"MOVE", copyMoveAlice, "/test/contacts/work/alice.vcf", true},
		},
		{
			name:     "move with a prefix",
			prefix:   "/dav/",
			method:   "MOVE",
			src:      "/dav" + copyMoveAlice,
			dest:     "/dav/test/contacts/work/alice.vcf",
			wantCode: http.StatusCreated,
			wantCall: &copyMoveCall{"MOVE", "/dav" + copyMoveAlice, "/dav/test/contacts/work/alice.vcf", false},
		},
		{
			name:     "destination is an address book",
			method:   "MOVE",
			src:      copyMoveAlice,
			dest:     "/test/contacts/work/",
			wantCode: http.StatusForbidden,
		},
		{
			name:     "destination is outside of an address book",
			method:   "MOVE",
			src:      copyMoveAlice,
			dest:     "/test/contacts/alice.vcf",
			wantCode: http.StatusForbidden,
		},
		{
			name:     "destination is in a collection in an address book",
			method:   "COPY",
			src:      copyMoveAlice,
			dest:     "/test/contacts/work/sub/alice.vcf",
			wantCode: http.StatusForbidden,
		},
		{
			name:     "destination is outside of the prefix",
			prefix:   "/dav/",
			method:   "MOVE",
			src:      "/dav" + copyMoveAlice,
			dest:     "/test/contacts/work/alice.vcf",
			wantCode: http.StatusBadGateway,
		},
		{
			name:     "source is an address book",
			method:   "MOVE",
			src:      "/test/contacts/private/",
			dest:     "/test/contacts/other/",
			wantCode: http.StatusForbidden,
		},
		{
			name:     "source is an address book, destination an object",
			method:   "COPY",
			src:      "/test/contacts/private/",
			dest:     "/test/contacts/work/private.vcf",
			wantCode: http.StatusForbidden,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			backend := newCopyMoveBackend()
			h := &Handler{Backend: backend, Prefix: tc.prefix}

			w := serveCopyMove(h, tc.method, tc.src, tc.dest, tc.overwrite)
			if w.Code != tc.wantCode {
				t.Errorf("%s returned %v, want %v: %s", tc.method, w.Code, tc.wantCode, w.Body.String())
			}

			var wantCalls []copyMoveCall
			if tc.wantCall != nil {
				wantCalls = []copyMoveCall{*tc.wantCall}
			}
			if !reflect.DeepEqual(backend.calls, wantCalls) {
				t.Errorf("backend calls = %+v, want %+v", backend.calls, wantCalls)
			}
		})
	}
}

// Backends which don't implement AddressObjectCopyMover keep answering COPY
// and MOVE with 501 Not Implemented.
func TestCopyMoveAddressObjectNotImplemented(t *testing.T) {
	h := &Handler{Backend: &objectBackend{objects: map[string]bool{copyMoveAlice: true}}}
	for _, method := range []string{"COPY", "MOVE"} {
		w := serveCopyMove(h, method, copyMoveAlice, "/test/contacts/work/alice.vcf", "")
		if w.Code != http.StatusNotImplemented {
			t.Errorf("%s returned %v, want 501: %s", method, w.Code, w.Body.String())
		}
	}
}

func allowedMethods(t *testing.T, h *Handler, path string) map[string]bool {
	t.Helper()

	req := httptest.NewRequest(http.MethodOptions, path, nil)
	ctx := context.WithValue(req.Context(), currentUserPrincipalKey, "/test/")
	ctx = context.WithValue(ctx, homeSetPathKey, "/test/contacts/")
	ctx = context.WithValue(ctx, addressBookPathKey, "/test/contacts/private")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req.WithContext(ctx))
	if w.Code != http.StatusNoContent {
		t.Fatalf("OPTIONS %s returned %v: %s", path, w.Code, w.Body.String())
	}

	allowed := make(map[string]bool)
	for _, method := range strings.Split(w.Header().Get("Allow"), ",") {
		allowed[strings.TrimSpace(method)] = true
	}
	return allowed
}

// COPY and MOVE are listed in the Allow header of address objects only when
// the backend implements AddressObjectCopyMover.
func TestOptionsAllowsCopyMoveAddressObject(t *testing.T) {
	for _, tc := range []struct {
		name    string
		backend Backend
		path    string
		want    bool
	}{
		{"address object", newCopyMoveBackend(), copyMoveAlice, true},
		{"address book", newCopyMoveBackend(), "/test/contacts/private/", false},
		{"missing address object", newCopyMoveBackend(), "/test/contacts/private/missing.vcf", false},
		{"backend without copy and move", &objectBackend{objects: map[string]bool{copyMoveAlice: true}}, copyMoveAlice, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			allowed := allowedMethods(t, &Handler{Backend: tc.backend}, tc.path)
			for _, method := range []string{"COPY", "MOVE"} {
				if allowed[method] != tc.want {
					t.Errorf("Allow lists %s: %v, want %v (Allow: %v)", method, allowed[method], tc.want, allowed)
				}
			}
		})
	}
}
