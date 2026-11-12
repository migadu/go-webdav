package caldav

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"

	"github.com/migadu/go-webdav"
)

const syncValidEvent = `<D:response>
    <D:href>/cal/ok.ics</D:href>
    <D:propstat>
      <D:prop>
        <D:getetag>"ok"</D:getetag>
        <C:calendar-data>BEGIN:VCALENDAR&#13;
VERSION:2.0&#13;
PRODID:-//Test//EN&#13;
BEGIN:VEVENT&#13;
UID:ok&#13;
DTSTAMP:20260101T000000Z&#13;
DTSTART:20260102T100000Z&#13;
END:VEVENT&#13;
END:VCALENDAR&#13;
</C:calendar-data>
      </D:prop>
      <D:status>HTTP/1.1 200 OK</D:status>
    </D:propstat>
  </D:response>`

// syncCollection runs a client SyncCollection of /cal/ against a server
// answering with a multistatus made of body.
func syncCollection(t *testing.T, body string) (*SyncResponse, error) {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/xml; charset=utf-8")
		w.WriteHeader(http.StatusMultiStatus)
		w.Write([]byte(`<?xml version="1.0" encoding="utf-8"?>
<D:multistatus xmlns:D="DAV:" xmlns:C="urn:ietf:params:xml:ns:caldav">
  ` + body + `
</D:multistatus>`))
	}))
	defer srv.Close()

	c, err := NewClient(srv.Client(), srv.URL)
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	return c.SyncCollection(context.Background(), "/cal/", &SyncQuery{
		CompRequest: CalendarCompRequest{Name: "VCALENDAR", AllProps: true, AllComps: true},
	})
}

// RFC 6578 section 3.2: the multistatus of a sync-collection report "MUST
// contain one DAV:sync-token element". Without one, a caller saving the new
// token would save an empty one.
func TestClientSyncCollectionMissingSyncToken(t *testing.T) {
	for name, token := range map[string]string{
		"absent": "",
		"empty":  "<D:sync-token>  </D:sync-token>",
	} {
		t.Run(name, func(t *testing.T) {
			res, err := syncCollection(t, syncValidEvent+token)
			if err == nil {
				t.Fatalf("SyncCollection() = %+v, want an error", res)
			}
			if res != nil {
				t.Errorf("SyncCollection() returned a response along with %v", err)
			}
		})
	}
}

// Only members can be removed (RFC 6578 section 3.2): a 404 Not Found
// response for the collection itself isn't a removed member.
func TestClientSyncCollectionCollectionNotFound(t *testing.T) {
	res, err := syncCollection(t, `<D:response>
    <D:href>/cal/</D:href>
    <D:href>/cal/gone.ics</D:href>
    <D:status>HTTP/1.1 404 Not Found</D:status>
  </D:response>
  <D:sync-token>urn:sync:2</D:sync-token>`)
	var httpErr *webdav.HTTPError
	if !errors.As(err, &httpErr) || httpErr.Code != http.StatusNotFound {
		t.Errorf("SyncCollection() error = %v, want a 404 error for the collection", err)
	}
	if res == nil {
		t.Fatal("SyncCollection() returned no response")
	}
	if want := []string{"/cal/gone.ics"}; !reflect.DeepEqual(res.Deleted, want) {
		t.Errorf("Deleted = %q, want %q", res.Deleted, want)
	}
}

// A member the client fails to decode is reported as an HrefError, as one the
// server failed to retrieve is, rather than failing the whole sync: the new
// sync token covers the other changes too.
func TestClientSyncCollectionUndecodableMember(t *testing.T) {
	for name, member := range map[string]string{
		"calendar data": `<D:response>
    <D:href>/cal/bad.ics</D:href>
    <D:propstat>
      <D:prop><C:calendar-data>BEGIN:VCALENDAR&#13;
this isn't iCalendar&#13;
</C:calendar-data></D:prop>
      <D:status>HTTP/1.1 200 OK</D:status>
    </D:propstat>
  </D:response>`,
		"getlastmodified": `<D:response>
    <D:href>/cal/bad.ics</D:href>
    <D:propstat>
      <D:prop><D:getlastmodified>yesterday</D:getlastmodified></D:prop>
      <D:status>HTTP/1.1 200 OK</D:status>
    </D:propstat>
  </D:response>`,
	} {
		t.Run(name, func(t *testing.T) {
			res, err := syncCollection(t, member+syncValidEvent+`<D:response>
    <D:href>/cal/gone.ics</D:href>
    <D:status>HTTP/1.1 404 Not Found</D:status>
  </D:response>
  <D:sync-token>urn:sync:3</D:sync-token>`)
			var hrefErr *webdav.HrefError
			if !errors.As(err, &hrefErr) || hrefErr.Href.Path != "/cal/bad.ics" {
				t.Errorf("SyncCollection() error = %v, want an HrefError for /cal/bad.ics", err)
			}
			if res == nil {
				t.Fatal("SyncCollection() returned no response")
			}
			if len(res.Updated) != 1 || res.Updated[0].Path != "/cal/ok.ics" || res.Updated[0].Data == nil {
				t.Errorf("Updated = %+v, want /cal/ok.ics with its data", res.Updated)
			}
			if want := []string{"/cal/gone.ics"}; !reflect.DeepEqual(res.Deleted, want) {
				t.Errorf("Deleted = %q, want %q", res.Deleted, want)
			}
			if res.SyncToken != "urn:sync:3" {
				t.Errorf("SyncToken = %q, want urn:sync:3", res.SyncToken)
			}
		})
	}
}
