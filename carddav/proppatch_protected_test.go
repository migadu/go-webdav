package carddav

import (
	"context"
	"encoding/xml"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"

	"github.com/migadu/go-webdav/internal"
)

// RFC 4918 section 9.2.1: "403 (Forbidden): The client has attempted to set
// a protected property, such as DAV:getetag. If returning this error, the
// server SHOULD use the precondition code 'cannot-modify-protected-property'
// inside the response body." The other properties get 424 Failed
// Dependency, and nothing is changed (section 9.2).
func TestPropPatchAddressBookProtectedProps(t *testing.T) {
	const protected = "403 {DAV:}cannot-modify-protected-property"
	for _, tc := range []struct {
		name, prop string
		want       string
	}{
		{name: "getetag", prop: `<D:getetag>"1"</D:getetag>`, want: protected},
		{name: "sync-token", prop: `<D:sync-token>http://example.com/ns/sync/1</D:sync-token>`, want: protected},
		// RFC 4918 section 15.9: "Resource type is generally decided
		// through the operation creating the resource (MKCOL vs PUT),
		// not by PROPPATCH."
		{name: "resourcetype", prop: `<D:resourcetype><D:collection/></D:resourcetype>`, want: protected},
		// RFC 6352 section 6.2.2: "MUST be protected"
		{name: "supported-address-data", prop: `<CR:supported-address-data><CR:address-data-type content-type="text/vcard" version="3.0"/></CR:supported-address-data>`, want: protected},
		{name: "max-resource-size", prop: `<CR:max-resource-size>1024</CR:max-resource-size>`, want: protected},
		// The server doesn't store dead properties: refused, for reasons
		// it doesn't specify
		{name: "dead property", prop: `<X:color>red</X:color>`, want: "403"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			backend := &updateBackend{}
			h := &Handler{Backend: backend}

			req := httptest.NewRequest("PROPPATCH", "/test/contacts/private", strings.NewReader(`<?xml version="1.0" encoding="UTF-8"?>
<D:propertyupdate xmlns:D="DAV:" xmlns:CR="urn:ietf:params:xml:ns:carddav" xmlns:X="http://example.com/ns/">
  <D:set><D:prop><D:displayname>Private</D:displayname>`+tc.prop+`</D:prop></D:set>
</D:propertyupdate>`))
			req.Header.Set("Content-Type", "application/xml")
			ctx := context.WithValue(req.Context(), currentUserPrincipalKey, "/test/")
			ctx = context.WithValue(ctx, homeSetPathKey, "/test/contacts/")
			ctx = context.WithValue(ctx, addressBookPathKey, "/test/contacts/private")
			w := httptest.NewRecorder()
			h.ServeHTTP(w, req.WithContext(ctx))

			if w.Code != http.StatusMultiStatus {
				t.Fatalf("PROPPATCH returned %v, want 207: %s", w.Code, w.Body.String())
			}
			var ms internal.MultiStatus
			if err := xml.NewDecoder(w.Body).Decode(&ms); err != nil {
				t.Fatal(err)
			}
			errs := make(map[string]string)
			for _, propstat := range ms.Responses[0].PropStats {
				s := fmt.Sprint(propstat.Status.Code)
				if propstat.Error != nil {
					for _, raw := range propstat.Error.Raw {
						name, _ := raw.XMLName()
						s += " {" + name.Space + "}" + name.Local
					}
				}
				for _, raw := range propstat.Prop.Raw {
					name, _ := raw.XMLName()
					errs["{"+name.Space+"}"+name.Local] = s
				}
			}

			if got := errs["{DAV:}displayname"]; got != "424" {
				t.Errorf("displayname propstat = %q, want 424", got)
			}
			delete(errs, "{DAV:}displayname")
			if len(errs) != 1 {
				t.Fatalf("propstats = %v, want displayname and one other property", errs)
			}
			for name, got := range errs {
				if got != tc.want {
					t.Errorf("%v: propstat = %q, want %q", name, got, tc.want)
				}
			}
			if len(backend.updates) != 0 {
				t.Errorf("updates = %+v, want none", backend.updates)
			}
		})
	}
}

// Where no property can be changed, on an address object or on the address
// book of a backend which isn't an AddressBookUpdater, the protected
// properties are still reported with DAV:cannot-modify-protected-property
// (RFC 4918 section 9.2.1), and the others with a plain 403.
func TestPropPatchReadOnlyProtectedProps(t *testing.T) {
	const protected = "403 {DAV:}cannot-modify-protected-property"
	b, objPath := newAddressDataBackend(t)
	h := &Handler{Backend: b}

	for _, tc := range []struct {
		name, path string
		want       map[string]string
	}{
		{
			name: "address book",
			path: syncAddressBookPath,
			want: map[string]string{
				"{DAV:}getetag":      protected,
				"{DAV:}resourcetype": protected,
				"{urn:ietf:params:xml:ns:carddav}supported-address-data": protected,
				"{DAV:}getcontenttype":          "403",
				"{DAV:}displayname":             "403",
				"{http://example.com/ns/}color": "403",
			},
		},
		{
			name: "address object",
			path: objPath,
			want: map[string]string{
				"{DAV:}getetag":      protected,
				"{DAV:}resourcetype": protected,
				// The server sets it, to text/vcard
				"{DAV:}getcontenttype": protected,
				// Not a property of address objects
				"{urn:ietf:params:xml:ns:carddav}supported-address-data": "403",
				"{DAV:}displayname":             "403",
				"{http://example.com/ns/}color": "403",
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			res := serveTestRequest(t, h, "PROPPATCH", tc.path, `<?xml version="1.0" encoding="UTF-8"?>
<D:propertyupdate xmlns:D="DAV:" xmlns:CR="urn:ietf:params:xml:ns:carddav" xmlns:X="http://example.com/ns/">
  <D:set><D:prop>
    <D:displayname>Work</D:displayname>
    <D:getetag>"1"</D:getetag>
    <D:getcontenttype>text/plain</D:getcontenttype>
    <X:color>red</X:color>
    <CR:supported-address-data/>
  </D:prop></D:set>
  <D:remove><D:prop><D:resourcetype/></D:prop></D:remove>
</D:propertyupdate>`)
			errs := make(map[string]string)
			for _, propstat := range decodeTestMultiStatus(t, res).Responses[0].PropStats {
				s := fmt.Sprint(propstat.Status.Code)
				if propstat.Error != nil {
					for _, raw := range propstat.Error.Raw {
						name, _ := raw.XMLName()
						s += " {" + name.Space + "}" + name.Local
					}
				}
				for _, raw := range propstat.Prop.Raw {
					name, _ := raw.XMLName()
					errs["{"+name.Space+"}"+name.Local] = s
				}
			}
			if !reflect.DeepEqual(errs, tc.want) {
				t.Errorf("propstats = %v, want %v", errs, tc.want)
			}
		})
	}
}
