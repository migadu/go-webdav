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

func TestPropPatchAddressBook(t *testing.T) {
	const body = `<?xml version="1.0" encoding="UTF-8"?>
<A:propertyupdate xmlns:A="DAV:" xmlns:C="urn:ietf:params:xml:ns:carddav">
  <A:set>
    <A:prop>
      <A:displayname>Friends</A:displayname>
      <C:addressbook-description>People I like</C:addressbook-description>
    </A:prop>
  </A:set>
</A:propertyupdate>`

	h := Handler{Backend: &testBackend{}}
	req := httptest.NewRequest("PROPPATCH", "/test/contacts/private", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/xml")
	ctx := context.WithValue(req.Context(), currentUserPrincipalKey, "/test/")
	ctx = context.WithValue(ctx, homeSetPathKey, "/test/contacts/")
	ctx = context.WithValue(ctx, addressBookPathKey, "/test/contacts/private")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req.WithContext(ctx))

	res := w.Result()
	defer res.Body.Close()
	if res.StatusCode != http.StatusMultiStatus {
		t.Fatalf("PROPPATCH returned %v, want 207", res.Status)
	}

	var ms internal.MultiStatus
	if err := xml.NewDecoder(res.Body).Decode(&ms); err != nil {
		t.Fatalf("failed to decode multistatus: %v", err)
	}
	if len(ms.Responses) != 1 {
		t.Fatalf("got %d responses, want 1", len(ms.Responses))
	}
	resp := ms.Responses[0]
	if resp.Status != nil {
		t.Errorf("response has a top-level status %v alongside its propstats", resp.Status.Code)
	}

	got := make(map[xml.Name]bool)
	for _, propstat := range resp.PropStats {
		for _, raw := range propstat.Prop.Raw {
			name, _ := raw.XMLName()
			got[name] = true
		}
	}
	for _, name := range []xml.Name{internal.DisplayNameName, addressBookDescriptionName} {
		if !got[name] {
			t.Errorf("property %v missing from the response, got %v", name, got)
		}
	}
}
