package carddav

import (
	"context"
	"encoding/xml"
	"errors"
	"net/http"
	"net/http/httptest"
	"reflect"
	"sort"
	"strings"
	"testing"

	"github.com/emersion/go-vcard"
	"github.com/migadu/go-webdav"
	"github.com/migadu/go-webdav/internal"
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

		got := syncStatusCodes(decodeSyncResponses(t, w))
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

// TestSyncCollectionHrefErrors checks the responses for the members a
// SyncBackend reports with HrefErrors. RFC 6578 section 3.2 only allows a
// DAV:status, 404 Not Found, for removed members: a changed member is
// reported with propstats instead. A member URL must appear only once.
func TestSyncCollectionHrefErrors(t *testing.T) {
	const ab = "/test/contacts/private/"
	needPrivileges := xml.Name{Space: "DAV:", Local: "need-privileges"}
	ok := newSyncTestObject("ok", "OK", "e1")
	ok.Path = ab + "ok.vcf"
	b := &syncMultigetPartialBackend{
		syncRes: &SyncResponse{
			SyncToken: "urn:sync:2",
			Updated:   []AddressObject{ok},
			Deleted:   []string{ab + "gone.vcf"},
		},
		syncErr: errors.Join(
			webdav.NewHrefError(ab+"corrupt.vcf", webdav.NewHTTPError(http.StatusInternalServerError, errors.New("corrupt vcard"))),
			webdav.NewHrefError(ab+"denied.vcf", internal.NewConditionError(http.StatusForbidden, needPrivileges)),
			webdav.NewHrefError(ab+"vanished.vcf", webdav.NewHTTPError(http.StatusNotFound, nil)),
			// Members already listed, which mustn't be listed again
			webdav.NewHrefError(ab+"ok.vcf", webdav.NewHTTPError(http.StatusInternalServerError, nil)),
			webdav.NewHrefError(ab+"gone.vcf", webdav.NewHTTPError(http.StatusInternalServerError, nil)),
			webdav.NewHrefError(ab+"corrupt.vcf", webdav.NewHTTPError(http.StatusForbidden, nil)),
		),
	}

	w := serveCardDAV(b, "REPORT", ab, "", syncCollectionBody("urn:sync:1", "1", 0))
	resps := decodeSyncResponses(t, w)
	checkStatusCodes(t, syncStatusCodes(resps), map[string]int{
		ab + "ok.vcf":       http.StatusOK,
		ab + "gone.vcf":     http.StatusNotFound,
		ab + "corrupt.vcf":  http.StatusInternalServerError,
		ab + "denied.vcf":   http.StatusForbidden,
		ab + "vanished.vcf": http.StatusNotFound,
	})

	// The propstat of a changed member which failed lists the requested
	// properties, along with the description and the DAV:error of the error
	wantNames := []xml.Name{internal.GetETagName, addressDataName}
	if propstat := failedPropStat(t, resps[ab+"corrupt.vcf"], wantNames); propstat != nil {
		if propstat.ResponseDescription != "corrupt vcard" {
			t.Errorf("corrupt.vcf: propstat responsedescription = %q, want %q", propstat.ResponseDescription, "corrupt vcard")
		}
	}
	if propstat := failedPropStat(t, resps[ab+"denied.vcf"], wantNames); propstat != nil {
		if propstat.Error == nil || len(propstat.Error.Raw) != 1 {
			t.Errorf("denied.vcf: propstat error = %v, want %v", propstat.Error, needPrivileges)
		} else if name, _ := propstat.Error.Raw[0].XMLName(); name != needPrivileges {
			t.Errorf("denied.vcf: propstat error = %v, want %v", name, needPrivileges)
		}
	}

	t.Run("Client", func(t *testing.T) {
		srv := httptest.NewServer(&Handler{Backend: b})
		defer srv.Close()
		c, err := NewClient(srv.Client(), srv.URL)
		if err != nil {
			t.Fatal(err)
		}
		res, err := c.SyncCollection(context.Background(), ab, &SyncQuery{SyncToken: "urn:sync:1"})
		if res == nil {
			t.Fatalf("SyncCollection() = %v, want a partial result", err)
		}
		var updated []string
		for _, ao := range res.Updated {
			updated = append(updated, ao.Path)
		}
		if want := []string{ab + "ok.vcf"}; !reflect.DeepEqual(updated, want) {
			t.Errorf("Updated = %v, want %v", updated, want)
		}
		sort.Strings(res.Deleted)
		if want := []string{ab + "gone.vcf", ab + "vanished.vcf"}; !reflect.DeepEqual(res.Deleted, want) {
			t.Errorf("Deleted = %v, want %v", res.Deleted, want)
		}
		checkStatusCodes(t, hrefErrorCodes(err), map[string]int{
			ab + "corrupt.vcf": http.StatusInternalServerError,
			ab + "denied.vcf":  http.StatusForbidden,
		})
	})
}

// A sync-collection REPORT lists the members of the address book (RFC 6578
// section 3.2), and the only response for the request-URI is the 507
// Insufficient Storage of a truncated result (section 3.6). An HrefError
// about the address book itself, such as a 404 Not Found for an address book
// which doesn't exist, fails the whole request instead, with its status and
// description.
func TestSyncCollectionCollectionHrefError(t *testing.T) {
	const ab = "/test/contacts/private/"
	ok := newSyncTestObject("ok", "OK", "e1")
	ok.Path = ab + "ok.vcf"
	for _, tc := range []struct {
		name      string
		href      string
		code      int
		truncated bool
	}{
		{"error", ab, http.StatusInternalServerError, false},
		{"error in a truncated result", ab, http.StatusInternalServerError, true},
		{"error without the trailing slash", strings.TrimSuffix(ab, "/"), http.StatusForbidden, true},
		{"not found", ab, http.StatusNotFound, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			b := &syncMultigetPartialBackend{
				syncRes: &SyncResponse{
					SyncToken: "urn:sync:2",
					Updated:   []AddressObject{ok},
					Truncated: tc.truncated,
				},
				syncErr: errors.Join(
					webdav.NewHrefError(ab+"corrupt.vcf", webdav.NewHTTPError(http.StatusInternalServerError, nil)),
					webdav.NewHrefError(tc.href, webdav.NewHTTPError(tc.code, errors.New("address book unavailable"))),
				),
			}
			w := serveCardDAV(b, "REPORT", ab, "", syncCollectionBody("urn:sync:1", "1", 0))
			if w.Code != tc.code || !strings.Contains(w.Body.String(), "address book unavailable") {
				t.Errorf("got status %v and body:\n%s\nwant status %v with the error description", w.Code, w.Body.String(), tc.code)
			}
		})
	}
}

// decodeSyncResponses decodes the multistatus body of a sync-collection
// REPORT, checks that each response has a shape RFC 6578 section 3.2 allows,
// and maps each href to its response.
func decodeSyncResponses(t *testing.T, w *httptest.ResponseRecorder) map[string]*internal.Response {
	t.Helper()
	if w.Code != http.StatusMultiStatus {
		t.Fatalf("status = %d, want %d; body:\n%s", w.Code, http.StatusMultiStatus, w.Body.String())
	}
	var ms internal.MultiStatus
	if err := xml.NewDecoder(w.Body).Decode(&ms); err != nil {
		t.Fatalf("failed to decode multistatus: %v", err)
	}
	resps := make(map[string]*internal.Response)
	for i := range ms.Responses {
		resp := &ms.Responses[i]
		if len(resp.Hrefs) != 1 {
			t.Fatalf("response has %d hrefs, want 1", len(resp.Hrefs))
		}
		href := resp.Hrefs[0].Path
		if _, dup := resps[href]; dup {
			t.Errorf("%v: listed twice, a member URL must appear only once", href)
		}
		resps[href] = resp
		switch {
		case resp.Status != nil:
			// 507 Insufficient Storage is for the request-URI of a
			// truncated response (section 3.6)
			if code := resp.Status.Code; code != http.StatusNotFound && code != http.StatusInsufficientStorage {
				t.Errorf("%v: DAV:status %v, only removed members have one, 404 Not Found", href, code)
			}
			if len(resp.PropStats) > 0 {
				t.Errorf("%v: removed member with a DAV:propstat", href)
			}
		case len(resp.PropStats) == 0:
			t.Errorf("%v: changed member without a DAV:propstat", href)
		}
	}
	return resps
}

// syncStatusCodes maps each href of resps, as returned by
// decodeSyncResponses, to its status: the DAV:status of a removed member,
// or, for a changed member, the first status of its propstats other than
// 200 OK, if any.
func syncStatusCodes(resps map[string]*internal.Response) map[string]int {
	codes := make(map[string]int, len(resps))
	for href, resp := range resps {
		code := http.StatusOK
		if resp.Status != nil {
			code = resp.Status.Code
		}
		for _, propstat := range resp.PropStats {
			if propstat.Status.Code != http.StatusOK {
				code = propstat.Status.Code
				break
			}
		}
		codes[href] = code
	}
	return codes
}

// failedPropStat checks that resp, the response for a changed member which
// failed, has a single propstat listing the properties names, and returns
// it.
func failedPropStat(t *testing.T, resp *internal.Response, names []xml.Name) *internal.PropStat {
	t.Helper()
	if resp == nil {
		t.Errorf("missing response")
		return nil
	}
	if resp.Status != nil || len(resp.PropStats) != 1 {
		t.Errorf("%v: got a DAV:status %v and %d propstats, want a single propstat", resp.Hrefs[0].Path, resp.Status, len(resp.PropStats))
		return nil
	}
	propstat := &resp.PropStats[0]
	var got []xml.Name
	for _, raw := range propstat.Prop.Raw {
		if name, ok := raw.XMLName(); ok {
			got = append(got, name)
		}
	}
	if !reflect.DeepEqual(got, names) {
		t.Errorf("%v: propstat lists %v, want %v", resp.Hrefs[0].Path, got, names)
	}
	return propstat
}

// hrefErrorCodes maps the href of each HrefError err is made of to the
// status of its HTTPError.
func hrefErrorCodes(err error) map[string]int {
	codes := make(map[string]int)
	var walk func(err error)
	walk = func(err error) {
		var hrefErr *webdav.HrefError
		if multi, ok := err.(interface{ Unwrap() []error }); ok {
			for _, err := range multi.Unwrap() {
				walk(err)
			}
		} else if errors.As(err, &hrefErr) {
			code := 0
			var httpErr *webdav.HTTPError
			if errors.As(hrefErr.Err, &httpErr) {
				code = httpErr.Code
			}
			codes[hrefErr.Href.Path] = code
		}
	}
	walk(err)
	return codes
}
