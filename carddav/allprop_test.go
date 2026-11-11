package carddav

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// allPropBackend serves alice at a path below the address book.
type allPropBackend struct {
	testBackend
}

func (b *allPropBackend) GetAddressObject(ctx context.Context, path string, req *AddressDataRequest) (*AddressObject, error) {
	ao, err := b.testBackend.GetAddressObject(ctx, alicePath, req)
	if err != nil {
		return nil, err
	}
	ao.Path = path
	return ao, nil
}

func TestPropFindAllProp(t *testing.T) {
	h := Handler{Backend: &allPropBackend{}}
	for _, tc := range []struct {
		path      string
		want, not []string
	}{
		{
			path: "/test/contacts/private",
			want: []string{`<displayname xmlns="DAV:">My contacts</displayname>`},
			// RFC 6352 section 6.2: SHOULD NOT be returned by allprop
			not: []string{"addressbook-description", "supported-address-data", "max-resource-size", "current-user-principal"},
		},
		{
			path: "/test/contacts/private/alice.vcf",
			want: []string{"getcontenttype"},
			// RFC 6352 section 10.4: address-data is not a property
			not: []string{"address-data", "BEGIN:VCARD"},
		},
	} {
		req := httptest.NewRequest("PROPFIND", tc.path, strings.NewReader(`<?xml version="1.0" encoding="utf-8"?>
<D:propfind xmlns:D="DAV:"><D:allprop/></D:propfind>`))
		req.Header.Set("Content-Type", "application/xml")
		req.Header.Set("Depth", "0")
		ctx := context.WithValue(req.Context(), currentUserPrincipalKey, "/test/")
		ctx = context.WithValue(ctx, homeSetPathKey, "/test/contacts/")
		ctx = context.WithValue(ctx, addressBookPathKey, "/test/contacts/private")
		w := httptest.NewRecorder()
		h.ServeHTTP(w, req.WithContext(ctx))

		if w.Code != http.StatusMultiStatus {
			t.Fatalf("PROPFIND %s returned %v", tc.path, w.Code)
		}
		b, _ := io.ReadAll(w.Body)
		body := string(b)
		for _, s := range tc.want {
			if !strings.Contains(body, s) {
				t.Errorf("PROPFIND %s: missing %s:\n%s", tc.path, s, body)
			}
		}
		for _, s := range tc.not {
			if strings.Contains(body, s) {
				t.Errorf("PROPFIND %s: unexpected %s:\n%s", tc.path, s, body)
			}
		}
	}
}
