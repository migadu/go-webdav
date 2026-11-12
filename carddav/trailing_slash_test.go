package carddav

import (
	"context"
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"

	"github.com/migadu/go-webdav"
)

// slashBackend is a testBackend which doesn't need its paths in the request
// context.
type slashBackend struct {
	*testBackend
}

func (slashBackend) CurrentUserPrincipal(ctx context.Context) (string, error) {
	return "/user/", nil
}

func (slashBackend) AddressBookHomeSetPath(ctx context.Context) (string, error) {
	return "/user/contacts/", nil
}

func (slashBackend) ListAddressBooks(ctx context.Context) ([]AddressBook, error) {
	return []AddressBook{{Path: "/user/contacts/default/"}}, nil
}

func (b slashBackend) GetAddressBook(ctx context.Context, path string) (*AddressBook, error) {
	if path != "/user/contacts/default/" {
		return nil, webdav.NewHTTPError(http.StatusNotFound, nil)
	}
	return &AddressBook{Path: path}, nil
}

// TestWellKnownTrailingSlash checks that the .well-known URL redirects to the
// principal whether it's requested with a trailing slash or without, as
// upstream issue 41 asks of collection URLs, while the URLs below it aren't
// redirected.
func TestWellKnownTrailingSlash(t *testing.T) {
	ts := httptest.NewServer(&Handler{Backend: slashBackend{&testBackend{}}})
	defer ts.Close()

	noRedirect := *ts.Client()
	noRedirect.CheckRedirect = func(req *http.Request, via []*http.Request) error {
		return http.ErrUseLastResponse
	}

	for _, tc := range []struct {
		path     string
		redirect bool
	}{
		{"/.well-known/carddav", true},
		{"/.well-known/carddav/", true},
		{"/.well-known/carddav/x", false},
		{"/.well-known/carddavx", false},
	} {
		t.Run(tc.path, func(t *testing.T) {
			req, err := http.NewRequest("PROPFIND", ts.URL+tc.path, nil)
			if err != nil {
				t.Fatal(err)
			}
			req.Header.Set("Depth", "0")
			resp, err := noRedirect.Do(req)
			if err != nil {
				t.Fatal(err)
			}
			resp.Body.Close()
			redirected := resp.StatusCode == http.StatusPermanentRedirect
			if redirected != tc.redirect {
				t.Errorf("got status %v, want a redirect: %v", resp.Status, tc.redirect)
			} else if loc := resp.Header.Get("Location"); redirected && loc != "/user/" {
				t.Errorf("got Location %q, want %q", loc, "/user/")
			}
			if !tc.redirect {
				return
			}

			// A client pointed at the .well-known URL finds the principal
			c, err := NewClient(ts.Client(), ts.URL+tc.path)
			if err != nil {
				t.Fatal(err)
			}
			principal, err := c.FindCurrentUserPrincipal(context.Background())
			if err != nil {
				t.Fatalf("FindCurrentUserPrincipal: %v", err)
			}
			if principal != "/user/" {
				t.Errorf("got principal %q, want %q", principal, "/user/")
			}
		})
	}
}

// TestFindAddressBooksTrailingSlash checks that FindAddressBooks skips the
// response for the address book home set when its href doesn't have the
// trailing slash of the request, or the other way around, and returns the
// paths of the address books as the server writes them: it tells the home set
// apart by its resource type, not by its path.
func TestFindAddressBooksTrailingSlash(t *testing.T) {
	for _, tc := range []struct{ request, href string }{
		{"/user/contacts", "/user/contacts/"},
		{"/user/contacts/", "/user/contacts"},
	} {
		t.Run(tc.request, func(t *testing.T) {
			ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path != tc.request {
					http.NotFound(w, r)
					return
				}
				w.Header().Set("Content-Type", "application/xml; charset=utf-8")
				w.WriteHeader(http.StatusMultiStatus)
				w.Write([]byte(`<?xml version="1.0" encoding="utf-8"?>
<d:multistatus xmlns:d="DAV:" xmlns:card="urn:ietf:params:xml:ns:carddav">
  <d:response>
    <d:href>` + tc.href + `</d:href>
    <d:propstat>
      <d:prop><d:resourcetype><d:collection/></d:resourcetype></d:prop>
      <d:status>HTTP/1.1 200 OK</d:status>
    </d:propstat>
  </d:response>
  <d:response>
    <d:href>/user/contacts/work</d:href>
    <d:propstat>
      <d:prop><d:resourcetype><d:collection/><card:addressbook/></d:resourcetype></d:prop>
      <d:status>HTTP/1.1 200 OK</d:status>
    </d:propstat>
  </d:response>
  <d:response>
    <d:href>/user/contacts/home/</d:href>
    <d:propstat>
      <d:prop><d:resourcetype><d:collection/><card:addressbook/></d:resourcetype></d:prop>
      <d:status>HTTP/1.1 200 OK</d:status>
    </d:propstat>
  </d:response>
</d:multistatus>`))
			}))
			defer ts.Close()

			c, err := NewClient(ts.Client(), ts.URL)
			if err != nil {
				t.Fatal(err)
			}
			abs, err := c.FindAddressBooks(context.Background(), tc.request)
			if err != nil {
				t.Fatalf("FindAddressBooks: %v", err)
			}
			var paths []string
			for _, ab := range abs {
				paths = append(paths, ab.Path)
			}
			if want := []string{"/user/contacts/work", "/user/contacts/home/"}; !reflect.DeepEqual(paths, want) {
				t.Errorf("got address books %q, want %q", paths, want)
			}
		})
	}
}
