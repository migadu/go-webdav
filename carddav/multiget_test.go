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

const multiGetAlicePath = "/test/contacts/private/alice.vcf"

// multiGetBackend fetches the objects of an addressbook-multiget at once.
type multiGetBackend struct {
	testBackend
	calls [][]string
}

func (b *multiGetBackend) GetAddressObject(ctx context.Context, path string, req *AddressDataRequest) (*AddressObject, error) {
	panic("GetAddressObject called for an addressbook-multiget")
}

func (b *multiGetBackend) GetAddressObjects(ctx context.Context, paths []string, req *AddressDataRequest) ([]AddressObject, error) {
	b.calls = append(b.calls, paths)
	var l []AddressObject
	for _, p := range paths {
		if p != multiGetAlicePath {
			continue
		}
		ao, err := b.testBackend.GetAddressObject(ctx, alicePath, req)
		if err != nil {
			return nil, err
		}
		ao.Path = p
		l = append(l, *ao)
	}
	return l, nil
}

func TestMultiGetBackend(t *testing.T) {
	backend := &multiGetBackend{}
	handler := Handler{Backend: backend}

	body := `<?xml version="1.0" encoding="UTF-8"?>
<C:addressbook-multiget xmlns:D="DAV:" xmlns:C="urn:ietf:params:xml:ns:carddav">
  <D:prop><D:getetag/><C:address-data/></D:prop>
  <D:href>/test/contacts/private/missing.vcf</D:href>
  <D:href>` + multiGetAlicePath + `</D:href>
</C:addressbook-multiget>`
	req := httptest.NewRequest("REPORT", "/test/contacts/private", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/xml")
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	if w.Code != http.StatusMultiStatus {
		t.Fatalf("REPORT returned %v, want 207: %s", w.Code, w.Body.String())
	}
	if want := [][]string{{"/test/contacts/private/missing.vcf", multiGetAlicePath}}; !reflect.DeepEqual(backend.calls, want) {
		t.Errorf("GetAddressObjects calls = %v, want %v", backend.calls, want)
	}

	var ms internal.MultiStatus
	if err := xml.NewDecoder(w.Body).Decode(&ms); err != nil {
		t.Fatal(err)
	}
	if len(ms.Responses) != 2 {
		t.Fatalf("got %d responses, want 2", len(ms.Responses))
	}
	if resp := ms.Responses[0]; resp.Status == nil || resp.Status.Code != http.StatusNotFound {
		t.Errorf("response for the missing object = %+v, want a 404", resp)
	}
	if resp := ms.Responses[1]; resp.Hrefs[0].Path != multiGetAlicePath || resp.Status != nil {
		t.Errorf("response for alice = %+v, want her properties", resp)
	}
}
