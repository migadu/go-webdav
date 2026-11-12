package carddav

import (
	"context"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"sort"
	"strings"
	"sync"
	"testing"

	"github.com/emersion/go-vcard"
	"github.com/emersion/go-webdav"
)

// memAddressBookBackend stores address books in memory, and lets clients
// create, update and delete them. They hold no address objects.
type memAddressBookBackend struct {
	mu           sync.Mutex
	addressBooks map[string]AddressBook
	updates      []AddressBookUpdate
	// updateErr, if set, is returned by UpdateAddressBook
	updateErr error
}

var _ AddressBookUpdater = (*memAddressBookBackend)(nil)

func newMemAddressBookBackend() *memAddressBookBackend {
	return &memAddressBookBackend{addressBooks: make(map[string]AddressBook)}
}

func errNoAddressBook(path string) error {
	return webdav.NewHTTPError(http.StatusNotFound, fmt.Errorf("no address book at %q", path))
}

func (b *memAddressBookBackend) CurrentUserPrincipal(ctx context.Context) (string, error) {
	return "/user/", nil
}

func (b *memAddressBookBackend) AddressBookHomeSetPath(ctx context.Context) (string, error) {
	return "/user/contacts/", nil
}

func (b *memAddressBookBackend) ListAddressBooks(ctx context.Context) ([]AddressBook, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	l := make([]AddressBook, 0, len(b.addressBooks))
	for _, ab := range b.addressBooks {
		l = append(l, ab)
	}
	sort.Slice(l, func(i, j int) bool { return l[i].Path < l[j].Path })
	return l, nil
}

func (b *memAddressBookBackend) GetAddressBook(ctx context.Context, path string) (*AddressBook, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	ab, ok := b.addressBooks[path]
	if !ok {
		return nil, errNoAddressBook(path)
	}
	return &ab, nil
}

func (b *memAddressBookBackend) CreateAddressBook(ctx context.Context, ab *AddressBook) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	if _, ok := b.addressBooks[ab.Path]; ok {
		return webdav.NewHTTPError(http.StatusMethodNotAllowed, fmt.Errorf("address book %q already exists", ab.Path))
	}
	b.addressBooks[ab.Path] = *ab
	return nil
}

func (b *memAddressBookBackend) DeleteAddressBook(ctx context.Context, path string) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	if _, ok := b.addressBooks[path]; !ok {
		return errNoAddressBook(path)
	}
	delete(b.addressBooks, path)
	return nil
}

func (b *memAddressBookBackend) UpdateAddressBook(ctx context.Context, path string, update *AddressBookUpdate) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.updateErr != nil {
		return b.updateErr
	}
	ab, ok := b.addressBooks[path]
	if !ok {
		return errNoAddressBook(path)
	}
	b.updates = append(b.updates, *update)
	if update.Name != nil {
		ab.Name = *update.Name
	}
	if update.Description != nil {
		ab.Description = *update.Description
	}
	b.addressBooks[path] = ab
	return nil
}

func (b *memAddressBookBackend) GetAddressObject(ctx context.Context, path string, req *AddressDataRequest) (*AddressObject, error) {
	return nil, webdav.NewHTTPError(http.StatusNotFound, fmt.Errorf("no address object at %q", path))
}

func (b *memAddressBookBackend) ListAddressObjects(ctx context.Context, path string, req *AddressDataRequest) ([]AddressObject, error) {
	return nil, nil
}

func (b *memAddressBookBackend) QueryAddressObjects(ctx context.Context, path string, query *AddressBookQuery) ([]AddressObject, error) {
	return nil, nil
}

func (b *memAddressBookBackend) PutAddressObject(ctx context.Context, path string, card vcard.Card, opts *PutAddressObjectOptions) (*AddressObject, error) {
	return nil, webdav.NewHTTPError(http.StatusForbidden, fmt.Errorf("read-only"))
}

func (b *memAddressBookBackend) DeleteAddressObject(ctx context.Context, path string) error {
	return webdav.NewHTTPError(http.StatusNotFound, fmt.Errorf("no address object at %q", path))
}

func newMemAddressBookClient(t *testing.T) (*Client, *memAddressBookBackend) {
	t.Helper()
	backend := newMemAddressBookBackend()
	ts := httptest.NewServer(&Handler{Backend: backend})
	t.Cleanup(ts.Close)
	c, err := NewClient(nil, ts.URL)
	if err != nil {
		t.Fatal(err)
	}
	return c, backend
}

func TestClientAddressBookLifecycle(t *testing.T) {
	c, backend := newMemAddressBookClient(t)
	ctx := context.Background()
	const abPath = "/user/contacts/work/"

	err := c.CreateAddressBook(ctx, &AddressBook{
		Path:        abPath,
		Name:        "Work",
		Description: "Colleagues",
		// Protected, so not sent
		MaxResourceSize: 1024,
	})
	if err != nil {
		t.Fatalf("CreateAddressBook() = %v", err)
	}

	ab, err := c.FindAddressBook(ctx, abPath)
	if err != nil {
		t.Fatalf("FindAddressBook() after CreateAddressBook = %v", err)
	}
	if ab.Path != abPath || ab.Name != "Work" || ab.Description != "Colleagues" || ab.MaxResourceSize != 0 {
		t.Errorf("FindAddressBook() = %+v, want the path, name and description it was created with", ab)
	}

	name, empty := "Office", ""
	if err := c.UpdateAddressBook(ctx, abPath, &AddressBookUpdate{Name: &name, Description: &empty}); err != nil {
		t.Fatalf("UpdateAddressBook() = %v", err)
	}
	wantUpdates := []AddressBookUpdate{{Name: &name, Description: &empty}}
	if !reflect.DeepEqual(backend.updates, wantUpdates) {
		t.Errorf("backend updates = %+v, want %+v", backend.updates, wantUpdates)
	}
	ab, err = c.FindAddressBook(ctx, abPath)
	if err != nil {
		t.Fatalf("FindAddressBook() after UpdateAddressBook = %v", err)
	}
	if ab.Name != "Office" || ab.Description != "" {
		t.Errorf("FindAddressBook() after UpdateAddressBook = %+v, want the new name and no description", ab)
	}

	// An update without changes sends no request
	if err := c.UpdateAddressBook(ctx, abPath, &AddressBookUpdate{}); err != nil {
		t.Errorf("UpdateAddressBook() without changes = %v", err)
	}
	if len(backend.updates) != 1 {
		t.Errorf("backend got %v updates, want 1", len(backend.updates))
	}

	abs, err := c.FindAddressBooks(ctx, "/user/contacts/")
	if err != nil {
		t.Fatalf("FindAddressBooks() = %v", err)
	}
	if len(abs) != 1 || abs[0].Path != abPath {
		t.Errorf("FindAddressBooks() = %+v, want only %v", abs, abPath)
	}

	if err := c.RemoveAll(ctx, abPath); err != nil {
		t.Fatalf("RemoveAll() = %v", err)
	}
	_, err = c.FindAddressBook(ctx, abPath)
	var httpErr *webdav.HTTPError
	if !errors.As(err, &httpErr) || httpErr.Code != http.StatusNotFound {
		t.Errorf("FindAddressBook() after RemoveAll = %v, want a 404 HTTPError", err)
	}
}

func TestClientAddressBookErrors(t *testing.T) {
	c, backend := newMemAddressBookClient(t)
	ctx := context.Background()
	const abPath = "/user/contacts/work/"

	if err := c.CreateAddressBook(ctx, &AddressBook{Path: abPath, Name: "Work"}); err != nil {
		t.Fatalf("CreateAddressBook() = %v", err)
	}

	// The backend refuses to replace the address book
	err := c.CreateAddressBook(ctx, &AddressBook{Path: abPath, Name: "Other"})
	var httpErr *webdav.HTTPError
	if !errors.As(err, &httpErr) || httpErr.Code != http.StatusMethodNotAllowed {
		t.Errorf("CreateAddressBook() on an existing address book = %v, want a 405 HTTPError", err)
	}

	// The server only creates address books in the home set
	err = c.CreateAddressBook(ctx, &AddressBook{Path: "/user/contacts/", Name: "Home"})
	if !errors.As(err, &httpErr) || httpErr.Code != http.StatusForbidden {
		t.Errorf("CreateAddressBook() on the home set = %v, want a 403 HTTPError", err)
	}

	if err := c.CreateAddressBook(ctx, &AddressBook{}); err == nil {
		t.Errorf("CreateAddressBook() without a path succeeded")
	}

	// The backend refuses the update as a whole
	name := "Office"
	backend.updateErr = webdav.NewHTTPError(http.StatusInsufficientStorage, errors.New("quota exceeded"))
	err = c.UpdateAddressBook(ctx, abPath, &AddressBookUpdate{Name: &name})
	if !errors.As(err, &httpErr) || httpErr.Code != http.StatusInsufficientStorage {
		t.Errorf("UpdateAddressBook() refused by the backend = %v, want a 507 HTTPError", err)
	}
	backend.updateErr = nil

	err = c.UpdateAddressBook(ctx, "/user/contacts/missing/", &AddressBookUpdate{Name: &name})
	if !errors.As(err, &httpErr) || httpErr.Code != http.StatusNotFound {
		t.Errorf("UpdateAddressBook() of a missing address book = %v, want a 404 HTTPError", err)
	}

	if ab := backend.addressBooks[abPath]; ab.Name != "Work" || len(backend.updates) != 0 {
		t.Errorf("address book = %+v after updates %+v, want it unchanged", ab, backend.updates)
	}
}

// The request body is an extended MKCOL one, as in the example of RFC 6352
// section 6.3.1.1.
func TestClientCreateAddressBookRequest(t *testing.T) {
	var method, path, contentType string
	var body []byte
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		method, path, contentType = r.Method, r.URL.Path, r.Header.Get("Content-Type")
		var err error
		if body, err = io.ReadAll(r.Body); err != nil {
			t.Errorf("failed to read request body: %v", err)
		}
		w.WriteHeader(http.StatusCreated)
	}))
	defer ts.Close()
	c, err := NewClient(nil, ts.URL)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()

	err = c.CreateAddressBook(ctx, &AddressBook{
		Path:        "/home/lisa/addressbook/",
		Name:        "Lisa's Contacts",
		Description: "My primary address book.",
	})
	if err != nil {
		t.Fatalf("CreateAddressBook() = %v", err)
	}
	if method != "MKCOL" || path != "/home/lisa/addressbook/" {
		t.Errorf("request = %v %v, want MKCOL /home/lisa/addressbook/", method, path)
	}
	if !strings.HasPrefix(contentType, "text/xml") && !strings.HasPrefix(contentType, "application/xml") {
		t.Errorf("Content-Type = %q, want an XML media type", contentType)
	}

	const prop = "{DAV:}mkcol/{DAV:}set/{DAV:}prop"
	wantPaths := []string{
		"{DAV:}mkcol",
		"{DAV:}mkcol/{DAV:}set",
		prop,
		prop + "/{DAV:}resourcetype",
		prop + "/{DAV:}resourcetype/{DAV:}collection",
		prop + "/{DAV:}resourcetype/{urn:ietf:params:xml:ns:carddav}addressbook",
		prop + "/{DAV:}displayname",
		prop + "/{urn:ietf:params:xml:ns:carddav}addressbook-description",
	}
	paths, text := xmlElementPaths(t, body)
	if !reflect.DeepEqual(paths, wantPaths) {
		t.Errorf("request elements = %q, want %q", paths, wantPaths)
	}
	for p, want := range map[string]string{
		prop + "/{DAV:}displayname":                                       "Lisa's Contacts",
		prop + "/{urn:ietf:params:xml:ns:carddav}addressbook-description": "My primary address book.",
	} {
		if text[p] != want {
			t.Errorf("text of %v = %q, want %q", p, text[p], want)
		}
	}

	// Even without properties, the resource type tells the server to create
	// an address book rather than a plain collection
	if err := c.CreateAddressBook(ctx, &AddressBook{Path: "/home/lisa/other/"}); err != nil {
		t.Fatalf("CreateAddressBook() without properties = %v", err)
	}
	paths, _ = xmlElementPaths(t, body)
	if !reflect.DeepEqual(paths, wantPaths[:6]) {
		t.Errorf("request elements = %q, want %q", paths, wantPaths[:6])
	}
}

// xmlElementPaths returns the elements of an XML document, each as the path
// of expanded names leading to it, along with the character data of each.
func xmlElementPaths(t *testing.T, body []byte) (paths []string, text map[string]string) {
	t.Helper()
	text = make(map[string]string)
	d := xml.NewDecoder(strings.NewReader(string(body)))
	var stack []string
	for {
		tok, err := d.Token()
		if err == io.EOF {
			break
		} else if err != nil {
			t.Fatalf("malformed XML %q: %v", body, err)
		}
		switch tok := tok.(type) {
		case xml.StartElement:
			stack = append(stack, "{"+tok.Name.Space+"}"+tok.Name.Local)
			paths = append(paths, strings.Join(stack, "/"))
		case xml.CharData:
			if len(stack) > 0 {
				text[strings.Join(stack, "/")] += string(tok)
			}
		case xml.EndElement:
			stack = stack[:len(stack)-1]
		}
	}
	return paths, text
}
