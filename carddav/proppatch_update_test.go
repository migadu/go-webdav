package carddav

import (
	"context"
	"encoding/xml"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"

	"github.com/emersion/go-webdav/internal"
)

// updateBackend records the address book updates it is asked for.
type updateBackend struct {
	testBackend
	updates []AddressBookUpdate
}

func (b *updateBackend) UpdateAddressBook(ctx context.Context, path string, update *AddressBookUpdate) error {
	if path != "/test/contacts/private" {
		return internal.HTTPErrorf(http.StatusNotFound, "not found")
	}
	b.updates = append(b.updates, *update)
	return nil
}

func propPatchAddressBook(t *testing.T, h *Handler, body string) map[xml.Name]int {
	t.Helper()

	req := httptest.NewRequest("PROPPATCH", "/test/contacts/private", strings.NewReader(body))
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
	codes := make(map[xml.Name]int)
	for _, propstat := range ms.Responses[0].PropStats {
		for _, raw := range propstat.Prop.Raw {
			name, _ := raw.XMLName()
			codes[name] = propstat.Status.Code
		}
	}
	return codes
}

func TestPropPatchUpdateAddressBook(t *testing.T) {
	backend := &updateBackend{}
	h := &Handler{Backend: backend}

	codes := propPatchAddressBook(t, h, `<?xml version="1.0" encoding="UTF-8"?>
<D:propertyupdate xmlns:D="DAV:" xmlns:C="urn:ietf:params:xml:ns:carddav">
  <D:set><D:prop><D:displayname>Friends</D:displayname></D:prop></D:set>
  <D:remove><D:prop><C:addressbook-description/></D:prop></D:remove>
</D:propertyupdate>`)

	want := map[xml.Name]int{
		internal.DisplayNameName:   http.StatusOK,
		addressBookDescriptionName: http.StatusOK,
	}
	if !reflect.DeepEqual(codes, want) {
		t.Errorf("propstat codes = %v, want %v", codes, want)
	}
	name, desc := "Friends", ""
	wantUpdates := []AddressBookUpdate{{Name: &name, Description: &desc}}
	if !reflect.DeepEqual(backend.updates, wantUpdates) {
		t.Errorf("updates = %+v, want %+v", backend.updates, wantUpdates)
	}
}

// RFC 4918 section 9.2: a PROPPATCH is applied in full or not at all.
func TestPropPatchUpdateAddressBookAtomic(t *testing.T) {
	backend := &updateBackend{}
	h := &Handler{Backend: backend}

	unknown := xml.Name{Space: "http://example.com/ns/", Local: "color"}
	codes := propPatchAddressBook(t, h, `<?xml version="1.0" encoding="UTF-8"?>
<D:propertyupdate xmlns:D="DAV:" xmlns:X="http://example.com/ns/">
  <D:set><D:prop>
    <D:displayname>Friends</D:displayname>
    <X:color>red</X:color>
  </D:prop></D:set>
</D:propertyupdate>`)

	want := map[xml.Name]int{
		internal.DisplayNameName: http.StatusFailedDependency,
		unknown:                  http.StatusForbidden,
	}
	if !reflect.DeepEqual(codes, want) {
		t.Errorf("propstat codes = %v, want %v", codes, want)
	}
	if len(backend.updates) != 0 {
		t.Errorf("address book updated with %+v, want no update", backend.updates)
	}
}

// RFC 4918 section 9.2: instructions are processed in document order, so the
// last one naming a property wins.
func TestPropPatchUpdateAddressBookOrder(t *testing.T) {
	for _, tc := range []struct {
		name         string
		instructions string
		want         string
	}{
		{
			name: "set then remove",
			instructions: `<D:set><D:prop><D:displayname>Friends</D:displayname></D:prop></D:set>
  <D:remove><D:prop><D:displayname/></D:prop></D:remove>`,
			want: "",
		},
		{
			name: "remove then set",
			instructions: `<D:remove><D:prop><D:displayname/></D:prop></D:remove>
  <D:set><D:prop><D:displayname>Friends</D:displayname></D:prop></D:set>`,
			want: "Friends",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			backend := &updateBackend{}
			h := &Handler{Backend: backend}

			codes := propPatchAddressBook(t, h, `<?xml version="1.0" encoding="UTF-8"?>
<D:propertyupdate xmlns:D="DAV:">
  `+tc.instructions+`
</D:propertyupdate>`)

			want := map[xml.Name]int{internal.DisplayNameName: http.StatusOK}
			if !reflect.DeepEqual(codes, want) {
				t.Errorf("propstat codes = %v, want %v", codes, want)
			}
			if len(backend.updates) != 1 || backend.updates[0].Description != nil {
				t.Fatalf("updates = %+v, want a single name update", backend.updates)
			}
			if name := backend.updates[0].Name; name == nil {
				t.Errorf("name not updated, want %q", tc.want)
			} else if *name != tc.want {
				t.Errorf("name updated to %q, want %q", *name, tc.want)
			}
		})
	}
}
