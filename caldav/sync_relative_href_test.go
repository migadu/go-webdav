package caldav

import (
	"context"
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"
)

// The hrefs of a multistatus response are relative to the URL of the request
// (RFC 3986 section 5.2): a relative href names a member of the collection,
// and an empty one the collection itself (section 4.4).
func TestClientSyncCollectionRelativeHrefs(t *testing.T) {
	for _, collection := range []string{"/cal/", "/"} {
		t.Run(collection, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path != collection {
					t.Errorf("got request for %v, want %v", r.URL.Path, collection)
				}
				w.Header().Set("Content-Type", "application/xml; charset=utf-8")
				w.WriteHeader(http.StatusMultiStatus)
				w.Write([]byte(`<?xml version="1.0" encoding="utf-8"?>
<D:multistatus xmlns:D="DAV:">
  <D:response>
    <D:href></D:href>
    <D:propstat>
      <D:prop><D:getetag>"coll"</D:getetag></D:prop>
      <D:status>HTTP/1.1 200 OK</D:status>
    </D:propstat>
  </D:response>
  <D:response>
    <D:href>ev1.ics</D:href>
    <D:propstat>
      <D:prop><D:getetag>"e1"</D:getetag></D:prop>
      <D:status>HTTP/1.1 200 OK</D:status>
    </D:propstat>
  </D:response>
  <D:response>
    <D:href>./del1.ics</D:href>
    <D:status>HTTP/1.1 404 Not Found</D:status>
  </D:response>
  <D:sync-token>urn:sync:1</D:sync-token>
</D:multistatus>`))
			}))
			defer srv.Close()

			c, err := NewClient(srv.Client(), srv.URL)
			if err != nil {
				t.Fatal(err)
			}
			res, err := c.SyncCollection(context.Background(), collection, &SyncQuery{})
			if err != nil {
				t.Fatalf("SyncCollection: %v", err)
			}

			var updated []string
			for _, o := range res.Updated {
				updated = append(updated, o.Path)
			}
			if want := []string{collection + "ev1.ics"}; !reflect.DeepEqual(updated, want) {
				t.Errorf("got updated %q, want %q", updated, want)
			}
			if want := []string{collection + "del1.ics"}; !reflect.DeepEqual(res.Deleted, want) {
				t.Errorf("got deleted %q, want %q", res.Deleted, want)
			}
		})
	}
}
