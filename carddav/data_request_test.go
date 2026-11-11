package carddav

import (
	"context"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
)

type dataReqCaptureBackend struct {
	testBackend
	lastQueryReq    AddressDataRequest
	lastMultigetReq AddressDataRequest
}

func (b *dataReqCaptureBackend) QueryAddressObjects(ctx context.Context, path string, query *AddressBookQuery) ([]AddressObject, error) {
	b.lastQueryReq = query.DataRequest
	return nil, nil
}

func (b *dataReqCaptureBackend) GetAddressObject(ctx context.Context, path string, req *AddressDataRequest) (*AddressObject, error) {
	b.lastMultigetReq = *req
	return b.testBackend.GetAddressObject(ctx, alicePath, req)
}

func TestCardDAVQueryAndMultigetAddressDataReq(t *testing.T) {
	b := &dataReqCaptureBackend{}
	h := &Handler{Backend: b}

	t.Run("bare address-data sets AllProp=true in query and multiget", func(t *testing.T) {
		req := httptest.NewRequest("REPORT", "/test/contacts/private/", strings.NewReader(`<?xml version="1.0" encoding="utf-8"?>
<C:addressbook-query xmlns:D="DAV:" xmlns:C="urn:ietf:params:xml:ns:carddav">
  <D:prop><D:getetag/><C:address-data/></D:prop>
  <C:filter/>
</C:addressbook-query>`))
		req.Header.Set("Content-Type", "application/xml")
		req.Header.Set("Depth", "1")
		w := httptest.NewRecorder()
		h.ServeHTTP(w, req)
		if w.Code != http.StatusMultiStatus {
			t.Fatalf("query status = %d, want 207", w.Code)
		}
		if want := (AddressDataRequest{AllProp: true}); !reflect.DeepEqual(b.lastQueryReq, want) {
			t.Errorf("query DataRequest = %+v, want %+v", b.lastQueryReq, want)
		}

		req = httptest.NewRequest("REPORT", "/test/contacts/private/", strings.NewReader(`<?xml version="1.0" encoding="utf-8"?>
<C:addressbook-multiget xmlns:D="DAV:" xmlns:C="urn:ietf:params:xml:ns:carddav">
  <D:prop><D:getetag/><C:address-data/></D:prop>
  <D:href>/test/contacts/private/alice.vcf</D:href>
</C:addressbook-multiget>`))
		req.Header.Set("Content-Type", "application/xml")
		w = httptest.NewRecorder()
		h.ServeHTTP(w, req)
		if w.Code != http.StatusMultiStatus {
			t.Fatalf("multiget status = %d, want 207", w.Code)
		}
		if want := (AddressDataRequest{AllProp: true}); !reflect.DeepEqual(b.lastMultigetReq, want) {
			t.Errorf("multiget DataRequest = %+v, want %+v", b.lastMultigetReq, want)
		}
	})

	t.Run("prop without address-data leaves DataRequest zero", func(t *testing.T) {
		req := httptest.NewRequest("REPORT", "/test/contacts/private/", strings.NewReader(`<?xml version="1.0" encoding="utf-8"?>
<C:addressbook-query xmlns:D="DAV:" xmlns:C="urn:ietf:params:xml:ns:carddav">
  <D:prop><D:getetag/></D:prop>
  <C:filter/>
</C:addressbook-query>`))
		req.Header.Set("Content-Type", "application/xml")
		req.Header.Set("Depth", "1")
		w := httptest.NewRecorder()
		h.ServeHTTP(w, req)
		if w.Code != http.StatusMultiStatus {
			t.Fatalf("query status = %d, want 207", w.Code)
		}
		if want := (AddressDataRequest{}); !reflect.DeepEqual(b.lastQueryReq, want) {
			t.Errorf("query DataRequest without address-data = %+v, want %+v", b.lastQueryReq, want)
		}
	})
}
