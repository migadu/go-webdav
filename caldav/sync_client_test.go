package caldav

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"

	"github.com/emersion/go-webdav"
)

func TestClientSyncCollectionEdgeCases(t *testing.T) {
	t.Run("requests and decodes getcontentlength, multi-href 404, and self-href without trailing slash", func(t *testing.T) {
		var reqBody string
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			b, _ := io.ReadAll(r.Body)
			reqBody = string(b)

			w.Header().Set("Content-Type", "application/xml; charset=utf-8")
			w.WriteHeader(http.StatusMultiStatus)
			w.Write([]byte(`<?xml version="1.0" encoding="utf-8"?>
<D:multistatus xmlns:D="DAV:" xmlns:C="urn:ietf:params:xml:ns:caldav">
  <D:response>
    <D:href>/cal/</D:href>
    <D:propstat>
      <D:prop><D:getetag>"coll"</D:getetag></D:prop>
      <D:status>HTTP/1.1 200 OK</D:status>
    </D:propstat>
  </D:response>
  <D:response>
    <D:href>/cal/ev1.ics</D:href>
    <D:propstat>
      <D:prop>
        <D:getetag>"e1"</D:getetag>
        <D:getcontentlength>128</D:getcontentlength>
        <C:calendar-data>BEGIN:VCALENDAR&#13;
VERSION:2.0&#13;
PRODID:-//Test//EN&#13;
BEGIN:VEVENT&#13;
UID:ev1&#13;
DTSTAMP:20260101T000000Z&#13;
DTSTART:20260102T100000Z&#13;
SUMMARY:Meeting&#13;
END:VEVENT&#13;
END:VCALENDAR&#13;
</C:calendar-data>
      </D:prop>
      <D:status>HTTP/1.1 200 OK</D:status>
    </D:propstat>
  </D:response>
  <D:response>
    <D:href>/cal/del1.ics</D:href>
    <D:href>/cal/del2.ics</D:href>
    <D:status>HTTP/1.1 404 Not Found</D:status>
  </D:response>
  <D:sync-token>urn:sync:cal2</D:sync-token>
</D:multistatus>`))
		}))
		defer srv.Close()

		c, err := NewClient(srv.Client(), srv.URL)
		if err != nil {
			t.Fatalf("NewClient: %v", err)
		}

		res, err := c.SyncCollection(context.Background(), "/cal", &SyncQuery{
			CompRequest: CalendarCompRequest{Name: "VCALENDAR", AllProps: true, AllComps: true},
		})
		if err != nil {
			t.Fatalf("SyncCollection: %v", err)
		}
		if !strings.Contains(reqBody, "getcontentlength") {
			t.Errorf("request body did not ask for getcontentlength:\n%s", reqBody)
		}
		if len(res.Updated) != 1 {
			t.Fatalf("got %d updated objects (want 1, skipping /cal/): %+v", len(res.Updated), res.Updated)
		}
		if res.Updated[0].ContentLength != 128 {
			t.Errorf("got ContentLength %d, want 128", res.Updated[0].ContentLength)
		}
		wantDeleted := []string{"/cal/del1.ics", "/cal/del2.ics"}
		if !reflect.DeepEqual(res.Deleted, wantDeleted) {
			t.Errorf("got Deleted=%q, want %q", res.Deleted, wantDeleted)
		}
	})

	t.Run("507 Insufficient Storage distinguishes collection truncation from member error", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/xml; charset=utf-8")
			w.WriteHeader(http.StatusMultiStatus)
			w.Write([]byte(`<?xml version="1.0" encoding="utf-8"?>
<D:multistatus xmlns:D="DAV:">
  <D:response>
    <D:href>/cal/bad.ics</D:href>
    <D:status>HTTP/1.1 507 Insufficient Storage</D:status>
  </D:response>
  <D:sync-token>urn:sync:cal3</D:sync-token>
</D:multistatus>`))
		}))
		defer srv.Close()

		c, err := NewClient(srv.Client(), srv.URL)
		if err != nil {
			t.Fatalf("NewClient: %v", err)
		}

		res, err := c.SyncCollection(context.Background(), "/cal/", &SyncQuery{})
		if err == nil {
			t.Fatalf("expected error for member 507 response, got nil")
		}
		var hrefErr *webdav.HrefError
		if !errors.As(err, &hrefErr) || hrefErr.Href.Path != "/cal/bad.ics" {
			t.Errorf("expected HrefError for /cal/bad.ics, got %v", err)
		}
		if res.Truncated {
			t.Errorf("expected Truncated=false on member 507 response, got true")
		}
	})

	// RFC 6578 section 3.2 doesn't allow a DAV:status for changed members:
	// a server reports one it failed to retrieve with failed propstats.
	t.Run("changed member with failed propstats is an HrefError", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/xml; charset=utf-8")
			w.WriteHeader(http.StatusMultiStatus)
			w.Write([]byte(`<?xml version="1.0" encoding="utf-8"?>
<D:multistatus xmlns:D="DAV:" xmlns:C="urn:ietf:params:xml:ns:caldav">
  <D:response>
    <D:href>/cal/ok.ics</D:href>
    <D:propstat>
      <D:prop><D:getetag>"e1"</D:getetag></D:prop>
      <D:status>HTTP/1.1 200 OK</D:status>
    </D:propstat>
    <D:propstat>
      <D:prop><C:calendar-data/></D:prop>
      <D:status>HTTP/1.1 404 Not Found</D:status>
    </D:propstat>
  </D:response>
  <D:response>
    <D:href>/cal/corrupt.ics</D:href>
    <D:propstat>
      <D:prop><D:getetag/><C:calendar-data/></D:prop>
      <D:status>HTTP/1.1 500 Internal Server Error</D:status>
    </D:propstat>
  </D:response>
  <D:response>
    <D:href>/cal/denied.ics</D:href>
    <D:propstat>
      <D:prop><D:getetag>"e2"</D:getetag></D:prop>
      <D:status>HTTP/1.1 200 OK</D:status>
    </D:propstat>
    <D:propstat>
      <D:prop><C:calendar-data/></D:prop>
      <D:status>HTTP/1.1 403 Forbidden</D:status>
    </D:propstat>
  </D:response>
  <D:sync-token>urn:sync:cal4</D:sync-token>
</D:multistatus>`))
		}))
		defer srv.Close()

		c, err := NewClient(srv.Client(), srv.URL)
		if err != nil {
			t.Fatalf("NewClient: %v", err)
		}

		res, err := c.SyncCollection(context.Background(), "/cal/", &SyncQuery{
			CompRequest: CalendarCompRequest{Name: "VCALENDAR", AllProps: true, AllComps: true},
		})
		if res == nil {
			t.Fatalf("SyncCollection() = %v, want a partial result", err)
		}
		if res.SyncToken != "urn:sync:cal4" {
			t.Errorf("got SyncToken %q, want %q", res.SyncToken, "urn:sync:cal4")
		}
		if len(res.Updated) != 1 || res.Updated[0].Path != "/cal/ok.ics" || res.Updated[0].ETag != "e1" {
			t.Errorf("got Updated %+v, want only /cal/ok.ics", res.Updated)
		}
		checkStatusCodes(t, hrefErrorCodes(err), map[string]int{
			"/cal/corrupt.ics": http.StatusInternalServerError,
			"/cal/denied.ics":  http.StatusForbidden,
		})
	})
}
