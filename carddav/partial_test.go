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
	"github.com/migadu/go-webdav"
	"github.com/migadu/go-webdav/internal"
)

// partialBackend is a Backend whose collection listings, queries and
// deletions return canned results.
type partialBackend struct {
	addressBooks        []AddressBook
	objects             []AddressObject
	listAddressBooksErr error
	listObjectsErr      error
	queryObjects        []AddressObject
	queryErr            error
	deleteErr           error
}

var _ Backend = (*partialBackend)(nil)

func (b *partialBackend) CurrentUserPrincipal(ctx context.Context) (string, error) {
	return "/user/", nil
}

func (b *partialBackend) AddressBookHomeSetPath(ctx context.Context) (string, error) {
	return "/user/contacts/", nil
}

func (b *partialBackend) ListAddressBooks(ctx context.Context) ([]AddressBook, error) {
	return b.addressBooks, b.listAddressBooksErr
}

func (b *partialBackend) GetAddressBook(ctx context.Context, path string) (*AddressBook, error) {
	for _, ab := range b.addressBooks {
		if ab.Path == path {
			return &ab, nil
		}
	}
	return nil, webdav.NewHTTPError(http.StatusNotFound, nil)
}

func (b *partialBackend) CreateAddressBook(ctx context.Context, ab *AddressBook) error {
	return webdav.NewHTTPError(http.StatusForbidden, nil)
}

func (b *partialBackend) DeleteAddressBook(ctx context.Context, path string) error {
	return b.deleteErr
}

func (b *partialBackend) GetAddressObject(ctx context.Context, path string, req *AddressDataRequest) (*AddressObject, error) {
	for _, ao := range b.objects {
		if ao.Path == path {
			return &ao, nil
		}
	}
	return nil, webdav.NewHTTPError(http.StatusNotFound, nil)
}

func (b *partialBackend) ListAddressObjects(ctx context.Context, path string, req *AddressDataRequest) ([]AddressObject, error) {
	return b.objects, b.listObjectsErr
}

func (b *partialBackend) QueryAddressObjects(ctx context.Context, path string, query *AddressBookQuery) ([]AddressObject, error) {
	return b.queryObjects, b.queryErr
}

func (b *partialBackend) PutAddressObject(ctx context.Context, path string, card vcard.Card, opts *PutAddressObjectOptions) (*AddressObject, error) {
	return nil, webdav.NewHTTPError(http.StatusForbidden, nil)
}

func (b *partialBackend) DeleteAddressObject(ctx context.Context, path string) error {
	return b.deleteErr
}

const propFindETag = `<?xml version="1.0" encoding="UTF-8"?>
<d:propfind xmlns:d="DAV:">
  <d:prop>
    <d:resourcetype/>
    <d:getetag/>
  </d:prop>
</d:propfind>`

const addressBookQueryETag = `<?xml version="1.0" encoding="UTF-8"?>
<card:addressbook-query xmlns:d="DAV:" xmlns:card="urn:ietf:params:xml:ns:carddav">
  <d:prop>
    <d:getetag/>
  </d:prop>
  <card:filter/>
</card:addressbook-query>`

const partialAddressBookPath = "/user/contacts/a/"

func newPartialBackend() *partialBackend {
	return &partialBackend{
		addressBooks: []AddressBook{{Path: partialAddressBookPath}},
		objects: []AddressObject{
			{Path: "/user/contacts/a/1.vcf", ETag: "1"},
			{Path: "/user/contacts/a/2.vcf", ETag: "2"},
		},
	}
}

func serveCardDAV(b Backend, method, target, depth, body string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, target, strings.NewReader(body))
	if body != "" {
		req.Header.Set("Content-Type", "application/xml")
	}
	if depth != "" {
		req.Header.Set("Depth", depth)
	}
	w := httptest.NewRecorder()
	(&Handler{Backend: b}).ServeHTTP(w, req)
	return w
}

// multiStatusCodes decodes a multistatus body and maps each href to the
// status of its response; a response carrying propstats is reported as 200.
func multiStatusCodes(t *testing.T, w *httptest.ResponseRecorder) map[string]int {
	t.Helper()
	if w.Code != http.StatusMultiStatus {
		t.Fatalf("status = %d, want %d; body:\n%s", w.Code, http.StatusMultiStatus, w.Body.String())
	}
	var ms internal.MultiStatus
	if err := xml.NewDecoder(w.Body).Decode(&ms); err != nil {
		t.Fatalf("failed to decode multistatus: %v", err)
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

func checkStatusCodes(t *testing.T, got, want map[string]int) {
	t.Helper()
	if !reflect.DeepEqual(got, want) {
		t.Errorf("response statuses = %v, want %v", got, want)
	}
}

func TestPropFindAddressBookPartialFailure(t *testing.T) {
	b := newPartialBackend()
	b.listObjectsErr = webdav.NewHrefError("/user/contacts/a/3.vcf", webdav.NewHTTPError(http.StatusForbidden, nil))

	w := serveCardDAV(b, "PROPFIND", partialAddressBookPath, "1", propFindETag)
	checkStatusCodes(t, multiStatusCodes(t, w), map[string]int{
		"/user/contacts/a/":      http.StatusOK,
		"/user/contacts/a/1.vcf": http.StatusOK,
		"/user/contacts/a/2.vcf": http.StatusOK,
		"/user/contacts/a/3.vcf": http.StatusForbidden,
	})
}

func TestPropFindAddressBookHomeSetPartialFailure(t *testing.T) {
	b := newPartialBackend()
	b.listAddressBooksErr = webdav.NewHrefError("/user/contacts/b/", webdav.NewHTTPError(http.StatusForbidden, nil))

	w := serveCardDAV(b, "PROPFIND", "/user/contacts/", "1", propFindETag)
	checkStatusCodes(t, multiStatusCodes(t, w), map[string]int{
		"/user/contacts/":   http.StatusOK,
		"/user/contacts/a/": http.StatusOK,
		"/user/contacts/b/": http.StatusForbidden,
	})
}

func TestPropFindAddressBookPlainError(t *testing.T) {
	b := newPartialBackend()
	b.listObjectsErr = webdav.NewHTTPError(http.StatusForbidden, nil)

	w := serveCardDAV(b, "PROPFIND", partialAddressBookPath, "1", propFindETag)
	if w.Code != http.StatusForbidden {
		t.Errorf("status = %d, want %d; body:\n%s", w.Code, http.StatusForbidden, w.Body.String())
	}
}

func TestAddressBookQueryPartialFailure(t *testing.T) {
	b := newPartialBackend()
	b.queryObjects = b.objects[:1]
	b.queryErr = errors.Join(
		webdav.NewHrefError("/user/contacts/a/2.vcf", webdav.NewHTTPError(http.StatusForbidden, nil)),
		webdav.NewHrefError("/user/contacts/a/3.vcf", errors.New("corrupt data")),
	)

	w := serveCardDAV(b, "REPORT", partialAddressBookPath, "1", addressBookQueryETag)
	checkStatusCodes(t, multiStatusCodes(t, w), map[string]int{
		"/user/contacts/a/1.vcf": http.StatusOK,
		"/user/contacts/a/2.vcf": http.StatusForbidden,
		"/user/contacts/a/3.vcf": http.StatusInternalServerError,
	})
}

func TestAddressBookQueryPlainError(t *testing.T) {
	b := newPartialBackend()
	b.queryObjects = b.objects[:1]
	b.queryErr = webdav.NewHTTPError(http.StatusForbidden, nil)

	w := serveCardDAV(b, "REPORT", partialAddressBookPath, "1", addressBookQueryETag)
	if w.Code != http.StatusForbidden {
		t.Errorf("status = %d, want %d; body:\n%s", w.Code, http.StatusForbidden, w.Body.String())
	}
}

func TestDeleteAddressBookPartialFailure(t *testing.T) {
	b := newPartialBackend()
	b.deleteErr = webdav.NewHrefError("/user/contacts/a/1.vcf", webdav.NewHTTPError(http.StatusLocked, nil))

	w := serveCardDAV(b, http.MethodDelete, partialAddressBookPath, "", "")
	checkStatusCodes(t, multiStatusCodes(t, w), map[string]int{
		"/user/contacts/a/1.vcf": http.StatusLocked,
	})
}

func TestDeleteAddressBookPlainError(t *testing.T) {
	b := newPartialBackend()
	b.deleteErr = webdav.NewHTTPError(http.StatusLocked, nil)

	w := serveCardDAV(b, http.MethodDelete, partialAddressBookPath, "", "")
	if w.Code != http.StatusLocked {
		t.Errorf("status = %d, want %d; body:\n%s", w.Code, http.StatusLocked, w.Body.String())
	}
}
