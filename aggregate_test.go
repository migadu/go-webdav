package webdav_test

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/emersion/go-ical"
	"github.com/emersion/go-vcard"
	"github.com/emersion/go-webdav"
	"github.com/emersion/go-webdav/caldav"
	"github.com/emersion/go-webdav/carddav"
)

const (
	aggPrincipal    = "/user/"
	aggCalendarHome = "/user/calendars/"
	aggContactsHome = "/user/contacts/"
)

var errAggNotFound = webdav.NewHTTPError(http.StatusNotFound, errors.New("not found"))

// aggCalBackend is a minimal in-memory caldav.Backend with one calendar.
type aggCalBackend struct{}

func (aggCalBackend) CurrentUserPrincipal(ctx context.Context) (string, error) {
	return aggPrincipal, nil
}
func (aggCalBackend) CalendarHomeSetPath(ctx context.Context) (string, error) {
	return aggCalendarHome, nil
}
func (aggCalBackend) CreateCalendar(ctx context.Context, cal *caldav.Calendar) error { return nil }
func (aggCalBackend) ListCalendars(ctx context.Context) ([]caldav.Calendar, error) {
	return []caldav.Calendar{{Path: aggCalendarHome + "work/", Name: "Work"}}, nil
}
func (b aggCalBackend) GetCalendar(ctx context.Context, path string) (*caldav.Calendar, error) {
	cals, _ := b.ListCalendars(ctx)
	for _, cal := range cals {
		if cal.Path == path {
			return &cal, nil
		}
	}
	return nil, errAggNotFound
}
func (aggCalBackend) GetCalendarObject(ctx context.Context, path string, req *caldav.CalendarCompRequest) (*caldav.CalendarObject, error) {
	return nil, errAggNotFound
}
func (aggCalBackend) ListCalendarObjects(ctx context.Context, path string, req *caldav.CalendarCompRequest) ([]caldav.CalendarObject, error) {
	return nil, nil
}
func (aggCalBackend) QueryCalendarObjects(ctx context.Context, path string, query *caldav.CalendarQuery) ([]caldav.CalendarObject, error) {
	return nil, nil
}
func (aggCalBackend) PutCalendarObject(ctx context.Context, path string, cal *ical.Calendar, opts *caldav.PutCalendarObjectOptions) (*caldav.CalendarObject, error) {
	return nil, webdav.NewHTTPError(http.StatusForbidden, errors.New("read-only"))
}
func (aggCalBackend) DeleteCalendarObject(ctx context.Context, path string) error {
	return errAggNotFound
}

// aggCardBackend is a minimal in-memory carddav.Backend with one address
// book.
type aggCardBackend struct{}

func (aggCardBackend) CurrentUserPrincipal(ctx context.Context) (string, error) {
	return aggPrincipal, nil
}
func (aggCardBackend) AddressBookHomeSetPath(ctx context.Context) (string, error) {
	return aggContactsHome, nil
}
func (aggCardBackend) ListAddressBooks(ctx context.Context) ([]carddav.AddressBook, error) {
	return []carddav.AddressBook{{Path: aggContactsHome + "friends/", Name: "Friends"}}, nil
}
func (b aggCardBackend) GetAddressBook(ctx context.Context, path string) (*carddav.AddressBook, error) {
	abs, _ := b.ListAddressBooks(ctx)
	for _, ab := range abs {
		if ab.Path == path {
			return &ab, nil
		}
	}
	return nil, errAggNotFound
}
func (aggCardBackend) CreateAddressBook(ctx context.Context, ab *carddav.AddressBook) error {
	return nil
}
func (aggCardBackend) DeleteAddressBook(ctx context.Context, path string) error { return nil }
func (aggCardBackend) GetAddressObject(ctx context.Context, path string, req *carddav.AddressDataRequest) (*carddav.AddressObject, error) {
	return nil, errAggNotFound
}
func (aggCardBackend) ListAddressObjects(ctx context.Context, path string, req *carddav.AddressDataRequest) ([]carddav.AddressObject, error) {
	return nil, nil
}
func (aggCardBackend) QueryAddressObjects(ctx context.Context, path string, query *carddav.AddressBookQuery) ([]carddav.AddressObject, error) {
	return nil, nil
}
func (aggCardBackend) PutAddressObject(ctx context.Context, path string, card vcard.Card, opts *carddav.PutAddressObjectOptions) (*carddav.AddressObject, error) {
	return nil, webdav.NewHTTPError(http.StatusForbidden, errors.New("read-only"))
}
func (aggCardBackend) DeleteAddressObject(ctx context.Context, path string) error {
	return errAggNotFound
}

// newAggregateServer serves a WebDAV file tree, a CalDAV server and a CardDAV
// server side by side, sharing one principal (upstream issue 27).
func newAggregateServer(t *testing.T) *httptest.Server {
	t.Helper()

	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "notes.txt"), []byte("hello"), 0644); err != nil {
		t.Fatal(err)
	}

	calHandler := &caldav.Handler{Backend: aggCalBackend{}}
	cardHandler := &carddav.Handler{Backend: aggCardBackend{}}
	principal := &webdav.ServePrincipalOptions{
		CurrentUserPrincipalPath: aggPrincipal,
		HomeSets: []webdav.BackendSuppliedHomeSet{
			caldav.NewCalendarHomeSet(aggCalendarHome),
			carddav.NewAddressBookHomeSet(aggContactsHome),
		},
		Capabilities: []webdav.Capability{
			caldav.CapabilityCalendar,
			carddav.CapabilityAddressBook,
		},
	}

	mux := http.NewServeMux()
	mux.Handle("/files/", http.StripPrefix("/files", &webdav.Handler{FileSystem: webdav.LocalFileSystem(dir)}))
	mux.Handle("/.well-known/caldav", calHandler)
	mux.Handle("/.well-known/carddav", cardHandler)
	mux.Handle(aggCalendarHome, calHandler)
	mux.Handle(aggContactsHome, cardHandler)
	mux.HandleFunc(aggPrincipal, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != aggPrincipal {
			http.NotFound(w, r)
			return
		}
		webdav.ServePrincipal(w, r, principal)
	})

	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

func TestAggregateServers(t *testing.T) {
	srv := newAggregateServer(t)
	ctx := context.Background()

	// CalDAV discovery from the well-known URL, through the shared principal
	cc, err := caldav.NewClient(nil, srv.URL+"/.well-known/caldav")
	if err != nil {
		t.Fatal(err)
	}
	principal, err := cc.FindCurrentUserPrincipal(ctx)
	if err != nil {
		t.Fatalf("caldav: FindCurrentUserPrincipal() = %v", err)
	}
	if principal != aggPrincipal {
		t.Errorf("caldav: principal = %q, want %q", principal, aggPrincipal)
	}
	calHome, err := cc.FindCalendarHomeSet(ctx, principal)
	if err != nil {
		t.Fatalf("FindCalendarHomeSet() = %v", err)
	}
	cals, err := cc.FindCalendars(ctx, calHome)
	if err != nil {
		t.Fatalf("FindCalendars() = %v", err)
	}
	if len(cals) != 1 || cals[0].Name != "Work" {
		t.Errorf("FindCalendars() = %+v, want the Work calendar", cals)
	}

	// CardDAV discovery, through the same principal
	ac, err := carddav.NewClient(nil, srv.URL+"/.well-known/carddav")
	if err != nil {
		t.Fatal(err)
	}
	principal, err = ac.FindCurrentUserPrincipal(ctx)
	if err != nil {
		t.Fatalf("carddav: FindCurrentUserPrincipal() = %v", err)
	}
	abHome, err := ac.FindAddressBookHomeSet(ctx, principal)
	if err != nil {
		t.Fatalf("FindAddressBookHomeSet() = %v", err)
	}
	abs, err := ac.FindAddressBooks(ctx, abHome)
	if err != nil {
		t.Fatalf("FindAddressBooks() = %v", err)
	}
	if len(abs) != 1 || abs[0].Name != "Friends" {
		t.Errorf("FindAddressBooks() = %+v, want the Friends address book", abs)
	}

	// The principal advertises both services
	req, _ := http.NewRequest(http.MethodOptions, srv.URL+aggPrincipal, nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if dav := resp.Header.Get("DAV"); !strings.Contains(dav, "calendar-access") || !strings.Contains(dav, "addressbook") {
		t.Errorf("principal DAV header = %q, want calendar-access and addressbook", dav)
	}

	// The file tree is served alongside
	wc, err := webdav.NewClient(nil, srv.URL+"/files/")
	if err != nil {
		t.Fatal(err)
	}
	f, err := wc.Open(ctx, "notes.txt")
	if err != nil {
		t.Fatalf("Open() = %v", err)
	}
	b, _ := io.ReadAll(f)
	f.Close()
	if string(b) != "hello" {
		t.Errorf("notes.txt = %q, want hello", b)
	}
}

// RFC 3744 section 4: a principal has a non-empty DAV:displayname and reports
// DAV:principal-URL, DAV:alternate-URI-set and DAV:group-membership. Apple
// clients ask for principal-URL and displayname when discovering a principal.
func TestPrincipalProperties(t *testing.T) {
	srv := newAggregateServer(t)

	calSrv := httptest.NewServer(&caldav.Handler{Backend: aggCalBackend{}})
	defer calSrv.Close()
	cardSrv := httptest.NewServer(&carddav.Handler{Backend: aggCardBackend{}})
	defer cardSrv.Close()

	const body = `<?xml version="1.0" encoding="UTF-8"?>
<A:propfind xmlns:A="DAV:">
  <A:prop>
    <A:principal-URL/>
    <A:displayname/>
    <A:alternate-URI-set/>
    <A:group-membership/>
  </A:prop>
</A:propfind>`

	for name, base := range map[string]string{
		"ServePrincipal": srv.URL,
		"caldav":         calSrv.URL,
		"carddav":        cardSrv.URL,
	} {
		req, err := http.NewRequest("PROPFIND", base+aggPrincipal, strings.NewReader(body))
		if err != nil {
			t.Fatal(err)
		}
		req.Header.Set("Content-Type", "application/xml")
		req.Header.Set("Depth", "0")
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		b, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		s := string(b)

		if strings.Contains(s, "404 Not Found") {
			t.Errorf("%s: principal properties reported as not found:\n%s", name, s)
		}
		for _, want := range []string{
			`<principal-URL xmlns="DAV:"><href>` + aggPrincipal + `</href></principal-URL>`,
			`<displayname xmlns="DAV:">user</displayname>`,
		} {
			if !strings.Contains(s, want) {
				t.Errorf("%s: missing %s in:\n%s", name, want, s)
			}
		}
	}
}
