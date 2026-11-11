package carddav

import (
	"context"
	"net/http/httptest"
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
