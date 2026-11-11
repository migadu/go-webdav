package carddav

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// readOnlyObjectBackend serves alice, read-only or not, below the address
// book.
type readOnlyObjectBackend struct {
	testBackend
	readOnly bool
}

func (b *readOnlyObjectBackend) GetAddressObject(ctx context.Context, path string, req *AddressDataRequest) (*AddressObject, error) {
	ao, err := b.testBackend.GetAddressObject(ctx, alicePath, req)
	if err != nil {
		return nil, err
	}
	ao.Path = path
	ao.ReadOnly = b.readOnly
	return ao, nil
}

// RFC 3744 section 5.4: clients such as Thunderbird read the privileges of
// each address object to decide whether it can be edited.
func TestPropFindAddressObjectPrivileges(t *testing.T) {
	for _, tc := range []struct {
		readOnly bool
		want     string
	}{
		{false, `<current-user-privilege-set xmlns="DAV:"><privilege xmlns="DAV:"><read xmlns="DAV:"></read><write xmlns="DAV:"></write></privilege></current-user-privilege-set>`},
		{true, `<current-user-privilege-set xmlns="DAV:"><privilege xmlns="DAV:"><read xmlns="DAV:"></read></privilege></current-user-privilege-set>`},
	} {
		h := Handler{Backend: &readOnlyObjectBackend{readOnly: tc.readOnly}}
		req := httptest.NewRequest("PROPFIND", "/test/contacts/private/alice.vcf", strings.NewReader(`<?xml version="1.0" encoding="utf-8"?>
<D:propfind xmlns:D="DAV:"><D:prop><D:current-user-privilege-set/></D:prop></D:propfind>`))
		req.Header.Set("Content-Type", "application/xml")
		req.Header.Set("Depth", "0")
		ctx := context.WithValue(req.Context(), currentUserPrincipalKey, "/test/")
		ctx = context.WithValue(ctx, homeSetPathKey, "/test/contacts/")
		ctx = context.WithValue(ctx, addressBookPathKey, "/test/contacts/private")
		w := httptest.NewRecorder()
		h.ServeHTTP(w, req.WithContext(ctx))

		if w.Code != http.StatusMultiStatus {
			t.Fatalf("PROPFIND returned %v", w.Code)
		}
		b, _ := io.ReadAll(w.Body)
		if !strings.Contains(string(b), tc.want) {
			t.Errorf("readOnly=%v: want %s in:\n%s", tc.readOnly, tc.want, b)
		}
	}
}
