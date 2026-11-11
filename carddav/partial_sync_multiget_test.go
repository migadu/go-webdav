package carddav

import (
	"context"
	"encoding/xml"
	"errors"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"

	"github.com/emersion/go-vcard"
	"github.com/emersion/go-webdav"
	"github.com/emersion/go-webdav/internal"
)

type syncMultigetPartialBackend struct {
	testBackend
	syncRes     *SyncResponse
	syncErr     error
	multigetRes []AddressObject
	multigetErr error
}

func (b *syncMultigetPartialBackend) SyncCollection(ctx context.Context, path string, query *SyncQuery) (*SyncResponse, error) {
	return b.syncRes, b.syncErr
}

func (b *syncMultigetPartialBackend) GetAddressObjects(ctx context.Context, paths []string, req *AddressDataRequest) ([]AddressObject, error) {
	return b.multigetRes, b.multigetErr
}

func cardMultiStatusCodes(t *testing.T, w *httptest.ResponseRecorder) map[string]int {
	t.Helper()
	if w.Code != http.StatusMultiStatus {
		t.Fatalf("status = %d, want 207; body:\n%s", w.Code, w.Body.String())
	}
	var ms internal.MultiStatus
	if err := xml.NewDecoder(w.Body).Decode(&ms); err != nil {
		t.Fatalf("Decode MultiStatus: %v", err)
	}
	codes := make(map[string]int)
	for _, resp := range ms.Responses {
		for _, href := range resp.Hrefs {
			code := http.StatusOK
			if resp.Status != nil {
				code = resp.Status.Code
			}
			codes[href.Path] = code
		}
	}
	return codes
}

func TestPartialHrefErrorsInSyncAndMultiget(t *testing.T) {
	card := make(vcard.Card)
	card.SetValue(vcard.FieldVersion, "3.0")
	card.SetValue(vcard.FieldUID, "ok-card-1")
	card.SetValue(vcard.FieldFormattedName, "Alice")

	const abPath = "/test/contacts/private/"
	b := &syncMultigetPartialBackend{
		syncRes: &SyncResponse{
			SyncToken: "urn:sync:2",
			Updated: []AddressObject{
				{Path: abPath + "ok.vcf", ETag: "e1", Card: card},
			},
			Deleted: []string{abPath + "gone.vcf"},
		},
		syncErr: webdav.NewHrefError(abPath+"corrupt.vcf", webdav.NewHTTPError(http.StatusInternalServerError, errors.New("corrupt vcard"))),
		multigetRes: []AddressObject{
			{Path: abPath + "ok.vcf", ETag: "e1", Card: card},
		},
		multigetErr: webdav.NewHrefError(abPath+"forbidden.vcf", webdav.NewHTTPError(http.StatusForbidden, errors.New("access denied"))),
	}
	h := &Handler{Backend: b}

	t.Run("SyncCollection includes HrefError responses in 207 Multi-Status", func(t *testing.T) {
		req := httptest.NewRequest("REPORT", abPath, strings.NewReader(syncCollectionBody("urn:sync:1", "1", 0)))
		req.Header.Set("Content-Type", "application/xml")
		w := httptest.NewRecorder()
		h.ServeHTTP(w, req)

		got := cardMultiStatusCodes(t, w)
		want := map[string]int{
			abPath + "ok.vcf":      http.StatusOK,
			abPath + "gone.vcf":    http.StatusNotFound,
			abPath + "corrupt.vcf": http.StatusInternalServerError,
		}
		if !reflect.DeepEqual(got, want) {
			t.Errorf("got codes %v, want %v", got, want)
		}
	})

	t.Run("AddressObjectsBackend.GetAddressObjects reports per-href errors", func(t *testing.T) {
		body := `<?xml version="1.0" encoding="utf-8"?>
<C:addressbook-multiget xmlns:D="DAV:" xmlns:C="urn:ietf:params:xml:ns:carddav">
  <D:prop><D:getetag/><C:address-data/></D:prop>
  <D:href>` + abPath + `ok.vcf</D:href>
  <D:href>` + abPath + `forbidden.vcf</D:href>
  <D:href>` + abPath + `missing.vcf</D:href>
</C:addressbook-multiget>`
		req := httptest.NewRequest("REPORT", abPath, strings.NewReader(body))
		req.Header.Set("Content-Type", "application/xml")
		w := httptest.NewRecorder()
		h.ServeHTTP(w, req)

		got := cardMultiStatusCodes(t, w)
		want := map[string]int{
			abPath + "ok.vcf":        http.StatusOK,
			abPath + "forbidden.vcf": http.StatusForbidden,
			abPath + "missing.vcf":   http.StatusNotFound,
		}
		if !reflect.DeepEqual(got, want) {
			t.Errorf("got codes %v, want %v", got, want)
		}
	})
}
