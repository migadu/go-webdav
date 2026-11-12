package carddav

import (
	"context"
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"
)

type customAddressBooksBackend struct {
	testBackend
	books []AddressBook
}

func (b *customAddressBooksBackend) CurrentUserPrincipal(ctx context.Context) (string, error) {
	return "/test/", nil
}

func (b *customAddressBooksBackend) AddressBookHomeSetPath(ctx context.Context) (string, error) {
	return "/test/contacts/", nil
}

func (b *customAddressBooksBackend) ListAddressBooks(ctx context.Context) ([]AddressBook, error) {
	return b.books, nil
}

func (b *customAddressBooksBackend) GetAddressBook(ctx context.Context, path string) (*AddressBook, error) {
	for i := range b.books {
		if b.books[i].Path == path {
			return &b.books[i], nil
		}
	}
	return b.testBackend.GetAddressBook(ctx, path)
}

func TestClientFindAddressBookCollectionProps(t *testing.T) {
	b := &customAddressBooksBackend{
		books: []AddressBook{
			{
				Path:        "/test/contacts/shared/",
				Name:        "Shared",
				Description: "Company directory",
				SyncToken:   "urn:sync:ab-1",
				CTag:        "ctag-ab-1",
				ReadOnly:    true,
			},
			{
				Path:      "/test/contacts/personal/",
				Name:      "Personal",
				SyncToken: "urn:sync:ab-2",
				CTag:      "ctag-ab-2",
				ReadOnly:  false,
			},
		},
	}
	srv := httptest.NewServer(&Handler{Backend: b})
	defer srv.Close()

	c, err := NewClient(srv.Client(), srv.URL)
	if err != nil {
		t.Fatal(err)
	}

	abs, err := c.FindAddressBooks(context.Background(), "/test/contacts/")
	if err != nil {
		t.Fatalf("FindAddressBooks: %v", err)
	}
	if len(abs) != 2 {
		t.Fatalf("got %d address books, want 2", len(abs))
	}

	shared := abs[0]
	if shared.SyncToken != "urn:sync:ab-1" {
		t.Errorf("shared.SyncToken = %q, want %q", shared.SyncToken, "urn:sync:ab-1")
	}
	if shared.CTag != "ctag-ab-1" {
		t.Errorf("shared.CTag = %q, want %q", shared.CTag, "ctag-ab-1")
	}
	if !shared.ReadOnly {
		t.Errorf("shared.ReadOnly = false, want true")
	}
	if abs[1].ReadOnly {
		t.Errorf("personal.ReadOnly = true, want false")
	}

	single, err := c.FindAddressBook(context.Background(), "/test/contacts/shared/")
	if err != nil {
		t.Fatalf("FindAddressBook: %v", err)
	}
	if single.Name != "Shared" || single.SyncToken != "urn:sync:ab-1" || single.CTag != "ctag-ab-1" || !single.ReadOnly {
		t.Errorf("FindAddressBook returned incomplete properties: %+v", single)
	}
}

// TestClientFindAddressBooksOptionalPropErrors checks that a property which
// only fills in an optional field of AddressBook is left out if the server
// fails to return it, rather than failing the request. RFC 4918 section
// 9.1.2: "clients should be prepared to handle other 2/3/4/5xx series status
// codes as well". RFC 3744 section 3.7 has DAV:current-user-privilege-set
// need its own privilege, so a server can refuse it with 403 Forbidden.
func TestClientFindAddressBooksOptionalPropErrors(t *testing.T) {
	for _, tc := range []struct {
		name, prop, status string
	}{
		{"current-user-privilege-set 403", `<D:current-user-privilege-set/>`, "403 Forbidden"},
		{"sync-token 403", `<D:sync-token/>`, "403 Forbidden"},
		{"getctag 500", `<CS:getctag/>`, "500 Internal Server Error"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/xml; charset=utf-8")
				w.WriteHeader(http.StatusMultiStatus)
				w.Write([]byte(`<?xml version="1.0" encoding="utf-8"?>
<D:multistatus xmlns:D="DAV:" xmlns:C="urn:ietf:params:xml:ns:carddav" xmlns:CS="http://calendarserver.org/ns/">
  <D:response>
    <D:href>/card/main/</D:href>
    <D:propstat>
      <D:prop>
        <D:resourcetype><D:collection/><C:addressbook/></D:resourcetype>
        <D:displayname>Main</D:displayname>
      </D:prop>
      <D:status>HTTP/1.1 200 OK</D:status>
    </D:propstat>
    <D:propstat>
      <D:prop>` + tc.prop + `</D:prop>
      <D:status>HTTP/1.1 ` + tc.status + `</D:status>
    </D:propstat>
  </D:response>
</D:multistatus>`))
			}))
			defer srv.Close()

			c, err := NewClient(srv.Client(), srv.URL)
			if err != nil {
				t.Fatal(err)
			}

			want := AddressBook{Path: "/card/main/", Name: "Main", SupportedAddressData: []AddressDataType{}}
			abs, err := c.FindAddressBooks(context.Background(), "/card/")
			if err != nil {
				t.Errorf("FindAddressBooks() = %v", err)
			} else if !reflect.DeepEqual(abs, []AddressBook{want}) {
				t.Errorf("FindAddressBooks() = %+v, want %+v", abs, []AddressBook{want})
			}
			ab, err := c.FindAddressBook(context.Background(), "/card/main/")
			if err != nil {
				t.Errorf("FindAddressBook() = %v", err)
			} else if !reflect.DeepEqual(*ab, want) {
				t.Errorf("FindAddressBook() = %+v, want %+v", *ab, want)
			}
		})
	}
}
