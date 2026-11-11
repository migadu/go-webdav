package carddav

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"

	"github.com/emersion/go-vcard"
	"github.com/emersion/go-webdav"
)

func TestClientSyncCollectionInlineDataAndEdgeCases(t *testing.T) {
	t.Run("decodes inline address-data and getcontentlength", func(t *testing.T) {
		var reqBody string
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			b, _ := io.ReadAll(r.Body)
			reqBody = string(b)

			w.Header().Set("Content-Type", "application/xml; charset=utf-8")
			w.WriteHeader(http.StatusMultiStatus)
			w.Write([]byte(`<?xml version="1.0" encoding="utf-8"?>
<D:multistatus xmlns:D="DAV:" xmlns:C="urn:ietf:params:xml:ns:carddav">
  <D:response>
    <D:href>/ab/alice.vcf</D:href>
    <D:propstat>
      <D:prop>
        <D:getetag>"etag-alice"</D:getetag>
        <D:getcontentlength>94</D:getcontentlength>
        <C:address-data>BEGIN:VCARD&#13;
VERSION:3.0&#13;
UID:alice-1&#13;
FN:Alice Wonder&#13;
END:VCARD&#13;
</C:address-data>
      </D:prop>
      <D:status>HTTP/1.1 200 OK</D:status>
    </D:propstat>
  </D:response>
  <D:response>
    <D:href>/ab/noinline.vcf</D:href>
    <D:propstat>
      <D:prop>
        <D:getetag>"etag-noinline"</D:getetag>
      </D:prop>
      <D:status>HTTP/1.1 200 OK</D:status>
    </D:propstat>
  </D:response>
  <D:sync-token>urn:sync:2</D:sync-token>
</D:multistatus>`))
		}))
		defer srv.Close()

		c, err := NewClient(srv.Client(), srv.URL)
		if err != nil {
			t.Fatalf("NewClient: %v", err)
		}

		res, err := c.SyncCollection(context.Background(), "/ab/", &SyncQuery{
			DataRequest: AddressDataRequest{AllProp: true},
		})
		if err != nil {
			t.Fatalf("SyncCollection: %v", err)
		}
		if !strings.Contains(reqBody, "getcontentlength") {
			t.Errorf("request body did not ask for getcontentlength:\n%s", reqBody)
		}
		if len(res.Updated) != 2 {
			t.Fatalf("got %d updated objects, want 2", len(res.Updated))
		}
		got := res.Updated[0]
		if got.ETag != "etag-alice" {
			t.Errorf("got ETag %q, want %q", got.ETag, "etag-alice")
		}
		if got.ContentLength != 94 {
			t.Errorf("got ContentLength %d, want 94", got.ContentLength)
		}
		if got.Card == nil {
			t.Fatalf("expected inline Card to be decoded, got nil")
		}
		if fn := got.Card.PreferredValue(vcard.FieldFormattedName); fn != "Alice Wonder" {
			t.Errorf("got FN %q, want %q", fn, "Alice Wonder")
		}
		if res.Updated[1].Card != nil {
			t.Errorf("expected omitted Card to stay nil, got %+v", res.Updated[1].Card)
		}
	})

	t.Run("multi-href 404 Not Found and collection self-href without trailing slash", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/xml; charset=utf-8")
			w.WriteHeader(http.StatusMultiStatus)
			w.Write([]byte(`<?xml version="1.0" encoding="utf-8"?>
<D:multistatus xmlns:D="DAV:">
  <D:response>
    <D:href>/ab/</D:href>
    <D:propstat>
      <D:prop><D:getetag>"coll"</D:getetag></D:prop>
      <D:status>HTTP/1.1 200 OK</D:status>
    </D:propstat>
  </D:response>
  <D:response>
    <D:href>/ab/gone1.vcf</D:href>
    <D:href>/ab/gone2.vcf</D:href>
    <D:status>HTTP/1.1 404 Not Found</D:status>
  </D:response>
  <D:sync-token>urn:sync:3</D:sync-token>
</D:multistatus>`))
		}))
		defer srv.Close()

		c, err := NewClient(srv.Client(), srv.URL)
		if err != nil {
			t.Fatalf("NewClient: %v", err)
		}

		// Pass path WITHOUT trailing slash while server returns /ab/
		res, err := c.SyncCollection(context.Background(), "/ab", &SyncQuery{})
		if err != nil {
			t.Fatalf("SyncCollection: %v", err)
		}
		if len(res.Updated) != 0 {
			t.Errorf("expected collection self-href /ab/ to be skipped, got Updated=%+v", res.Updated)
		}
		wantDeleted := []string{"/ab/gone1.vcf", "/ab/gone2.vcf"}
		if !reflect.DeepEqual(res.Deleted, wantDeleted) {
			t.Errorf("got Deleted=%q, want %q", res.Deleted, wantDeleted)
		}
	})

	t.Run("507 Insufficient Storage distinguishes collection truncation from member error", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/xml; charset=utf-8")
			w.WriteHeader(http.StatusMultiStatus)
			if r.URL.Path == "/ab-member-err/" {
				w.Write([]byte(`<?xml version="1.0" encoding="utf-8"?>
<D:multistatus xmlns:D="DAV:">
  <D:response>
    <D:href>/ab-member-err/bad.vcf</D:href>
    <D:status>HTTP/1.1 507 Insufficient Storage</D:status>
  </D:response>
  <D:sync-token>urn:sync:4</D:sync-token>
</D:multistatus>`))
				return
			}
			w.Write([]byte(`<?xml version="1.0" encoding="utf-8"?>
<D:multistatus xmlns:D="DAV:">
  <D:response>
    <D:href>/ab-trunc/</D:href>
    <D:status>HTTP/1.1 507 Insufficient Storage</D:status>
  </D:response>
  <D:sync-token>urn:sync:5</D:sync-token>
</D:multistatus>`))
		}))
		defer srv.Close()

		c, err := NewClient(srv.Client(), srv.URL)
		if err != nil {
			t.Fatalf("NewClient: %v", err)
		}

		// Member 507 should surface as an HrefError, NOT Truncated=true.
		res, err := c.SyncCollection(context.Background(), "/ab-member-err/", &SyncQuery{})
		if err == nil {
			t.Fatalf("expected error for member 507 response, got nil")
		}
		var hrefErr *webdav.HrefError
		if !errors.As(err, &hrefErr) || hrefErr.Href.Path != "/ab-member-err/bad.vcf" {
			t.Errorf("expected HrefError for /ab-member-err/bad.vcf, got %v", err)
		}
		if res.Truncated {
			t.Errorf("expected Truncated=false on member 507 response, got true")
		}

		// Collection 507 (even when caller omits trailing slash) sets Truncated=true without error.
		res2, err := c.SyncCollection(context.Background(), "/ab-trunc", &SyncQuery{})
		if err != nil {
			t.Fatalf("SyncCollection(/ab-trunc): unexpected error: %v", err)
		}
		if !res2.Truncated {
			t.Errorf("expected Truncated=true on collection 507 response, got false")
		}
	})
}
