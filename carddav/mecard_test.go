package carddav

import (
	"context"
	"encoding/xml"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/emersion/go-vcard"
	"github.com/emersion/go-webdav"
	"github.com/emersion/go-webdav/internal"
)

const (
	meCardPrincipal   = "/user/"
	meCardHomeSet     = "/user/contacts/"
	meCardAddressBook = "/user/contacts/default/"
	meCardObject      = "/user/contacts/default/me.vcf"
)

var meCardPropName = xml.Name{Space: "http://calendarserver.org/ns/", Local: "me-card"}

// meCardTestBackend is a Backend with a single address book, which reports
// the me card of upstream issue 38 with MeCardPath.
type meCardTestBackend struct {
	meCard    string
	meCardErr error
	calls     atomic.Int32
}

func (b *meCardTestBackend) MeCardPath(ctx context.Context) (string, error) {
	b.calls.Add(1)
	return b.meCard, b.meCardErr
}

func (b *meCardTestBackend) CurrentUserPrincipal(ctx context.Context) (string, error) {
	return meCardPrincipal, nil
}

func (b *meCardTestBackend) AddressBookHomeSetPath(ctx context.Context) (string, error) {
	return meCardHomeSet, nil
}

func (b *meCardTestBackend) ListAddressBooks(ctx context.Context) ([]AddressBook, error) {
	return []AddressBook{{Path: meCardAddressBook}}, nil
}

func (b *meCardTestBackend) GetAddressBook(ctx context.Context, path string) (*AddressBook, error) {
	if path != meCardAddressBook {
		return nil, webdav.NewHTTPError(http.StatusNotFound, nil)
	}
	return &AddressBook{Path: meCardAddressBook}, nil
}

func (b *meCardTestBackend) CreateAddressBook(ctx context.Context, addressBook *AddressBook) error {
	return webdav.NewHTTPError(http.StatusForbidden, nil)
}

func (b *meCardTestBackend) DeleteAddressBook(ctx context.Context, path string) error {
	return webdav.NewHTTPError(http.StatusForbidden, nil)
}

func (b *meCardTestBackend) GetAddressObject(ctx context.Context, path string, req *AddressDataRequest) (*AddressObject, error) {
	if path != meCardObject {
		return nil, webdav.NewHTTPError(http.StatusNotFound, nil)
	}
	card := vcard.Card{}
	card.SetValue(vcard.FieldVersion, "4.0")
	card.SetValue(vcard.FieldFormattedName, "Me")
	return &AddressObject{Path: meCardObject, Card: card}, nil
}

func (b *meCardTestBackend) ListAddressObjects(ctx context.Context, path string, req *AddressDataRequest) ([]AddressObject, error) {
	ao, err := b.GetAddressObject(ctx, meCardObject, req)
	if err != nil {
		return nil, err
	}
	return []AddressObject{*ao}, nil
}

func (b *meCardTestBackend) QueryAddressObjects(ctx context.Context, path string, query *AddressBookQuery) ([]AddressObject, error) {
	aos, err := b.ListAddressObjects(ctx, path, &query.DataRequest)
	if err != nil {
		return nil, err
	}
	return Filter(query, aos)
}

func (b *meCardTestBackend) PutAddressObject(ctx context.Context, path string, card vcard.Card, opts *PutAddressObjectOptions) (*AddressObject, error) {
	return nil, webdav.NewHTTPError(http.StatusForbidden, nil)
}

func (b *meCardTestBackend) DeleteAddressObject(ctx context.Context, path string) error {
	return webdav.NewHTTPError(http.StatusForbidden, nil)
}

// noMeCardBackend is a Backend which doesn't report a me card: it doesn't
// have a MeCardPath method.
type noMeCardBackend struct {
	Backend
}

const propFindMeCard = `<?xml version="1.0" encoding="utf-8"?>
<D:propfind xmlns:D="DAV:" xmlns:CS="http://calendarserver.org/ns/">
  <D:prop><D:resourcetype/><CS:me-card/></D:prop>
</D:propfind>`

// propFindMeCardResponse sends a PROPFIND request for path with body to h,
// and returns the response for path.
func propFindMeCardResponse(t *testing.T, h http.Handler, path, depth, body string) *internal.Response {
	t.Helper()
	return propFindMeCardMemberResponse(t, h, path, depth, body, path)
}

// propFindMeCardMemberResponse sends a PROPFIND request for path with body to
// h, and returns the response for member.
func propFindMeCardMemberResponse(t *testing.T, h http.Handler, path, depth, body, member string) *internal.Response {
	t.Helper()
	req := httptest.NewRequest("PROPFIND", path, strings.NewReader(body))
	if body != "" {
		req.Header.Set("Content-Type", "application/xml")
	}
	req.Header.Set("Depth", depth)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	if w.Code != http.StatusMultiStatus {
		t.Fatalf("PROPFIND %v: got status %v, want 207 Multi-Status: %v", path, w.Code, w.Body.String())
	}
	var ms internal.MultiStatus
	if err := xml.NewDecoder(w.Body).Decode(&ms); err != nil {
		t.Fatalf("PROPFIND %v: decoding the response: %v", path, err)
	}
	for i := range ms.Responses {
		if p, err := ms.Responses[i].Path(); err == nil && p == member {
			return &ms.Responses[i]
		}
	}
	t.Fatalf("PROPFIND %v: no response for %v in %+v", path, member, ms.Responses)
	return nil
}

// propStatus returns the status of the property name in resp, 0 if resp
// doesn't have it.
func propStatus(resp *internal.Response, name xml.Name) int {
	for _, propstat := range resp.PropStats {
		if propstat.Prop.Get(name) != nil {
			return propstat.Status.Code
		}
	}
	return 0
}

// meCardHref returns the href of the me-card property in resp.
func meCardHref(t *testing.T, resp *internal.Response) string {
	t.Helper()
	if code := propStatus(resp, meCardPropName); code != http.StatusOK {
		t.Fatalf("me-card: got status %v, want 200 OK", code)
	}
	var v struct {
		XMLName xml.Name `xml:"http://calendarserver.org/ns/ me-card"`
		Hrefs   []string `xml:"DAV: href"`
	}
	if err := resp.DecodeProp(&v); err != nil {
		t.Fatalf("me-card: %v", err)
	}
	if len(v.Hrefs) != 1 {
		t.Fatalf("me-card: got hrefs %q, want exactly one", v.Hrefs)
	}
	return v.Hrefs[0]
}

// TestServerMeCard checks that the server serves the me-card property of the
// address book home set when the backend reports a me card, and only then.
func TestServerMeCard(t *testing.T) {
	t.Run("set", func(t *testing.T) {
		b := &meCardTestBackend{meCard: meCardObject}
		h := &Handler{Backend: b}

		resp := propFindMeCardResponse(t, h, meCardHomeSet, "0", propFindMeCard)
		if got := meCardHref(t, resp); got != meCardObject {
			t.Errorf("got me-card href %q, want %q", got, meCardObject)
		}

		// Also in the response for the home set to a Depth: 1 PROPFIND
		// request on the principal, but not in the one for the principal
		resp = propFindMeCardMemberResponse(t, h, meCardPrincipal, "1", propFindMeCard, meCardHomeSet)
		if got := meCardHref(t, resp); got != meCardObject {
			t.Errorf("Depth: 1 on the principal: got me-card href %q, want %q", got, meCardObject)
		}
		resp = propFindMeCardResponse(t, h, meCardPrincipal, "1", propFindMeCard)
		if code := propStatus(resp, meCardPropName); code != http.StatusNotFound {
			t.Errorf("me-card of the principal: got status %v, want 404 Not Found", code)
		}

		// The address books listed by a Depth: 1 PROPFIND request on the
		// home set don't have it
		resp = propFindMeCardMemberResponse(t, h, meCardHomeSet, "1", propFindMeCard, meCardAddressBook)
		if code := propStatus(resp, meCardPropName); code != http.StatusNotFound {
			t.Errorf("me-card of the address book: got status %v, want 404 Not Found", code)
		}
	})

	t.Run("not set", func(t *testing.T) {
		b := &meCardTestBackend{}
		resp := propFindMeCardResponse(t, &Handler{Backend: b}, meCardHomeSet, "0", propFindMeCard)
		if code := propStatus(resp, meCardPropName); code != http.StatusNotFound {
			t.Errorf("got status %v, want 404 Not Found", code)
		}
	})

	t.Run("no MeCardPath", func(t *testing.T) {
		b := noMeCardBackend{&meCardTestBackend{meCard: meCardObject}}
		resp := propFindMeCardResponse(t, &Handler{Backend: b}, meCardHomeSet, "0", propFindMeCard)
		if code := propStatus(resp, meCardPropName); code != http.StatusNotFound {
			t.Errorf("got status %v, want 404 Not Found", code)
		}
	})

	t.Run("error", func(t *testing.T) {
		b := &meCardTestBackend{meCardErr: webdav.NewHTTPError(http.StatusServiceUnavailable, errors.New("unavailable"))}
		resp := propFindMeCardResponse(t, &Handler{Backend: b}, meCardHomeSet, "0", propFindMeCard)
		if code := propStatus(resp, meCardPropName); code != http.StatusServiceUnavailable {
			t.Errorf("got status %v, want 503 Service Unavailable", code)
		}
		if code := propStatus(resp, internal.ResourceTypeName); code != http.StatusOK {
			t.Errorf("resourcetype: got status %v, want 200 OK", code)
		}
	})

	t.Run("propname", func(t *testing.T) {
		const body = `<?xml version="1.0" encoding="utf-8"?><D:propfind xmlns:D="DAV:"><D:propname/></D:propfind>`
		for _, tc := range []struct {
			meCard string
			err    error
			want   bool
		}{
			{meCardObject, nil, true},
			{"", nil, false},
			// A propname response can't carry the error, and whether there's
			// a me card is unknown
			{"", webdav.NewHTTPError(http.StatusServiceUnavailable, errors.New("unavailable")), false},
		} {
			b := &meCardTestBackend{meCard: tc.meCard, meCardErr: tc.err}
			resp := propFindMeCardResponse(t, &Handler{Backend: b}, meCardHomeSet, "0", body)
			if got := propStatus(resp, meCardPropName) == http.StatusOK; got != tc.want {
				t.Errorf("me card %q, error %v: me-card listed: %v, want %v", tc.meCard, tc.err, got, tc.want)
			}
		}
	})

	t.Run("allprop", func(t *testing.T) {
		// Like the CalendarServer property, it isn't one of the properties
		// allprop returns (RFC 4918 section 9.1)
		b := &meCardTestBackend{meCard: meCardObject}
		resp := propFindMeCardResponse(t, &Handler{Backend: b}, meCardHomeSet, "0", "")
		if code := propStatus(resp, meCardPropName); code != 0 {
			t.Errorf("got me-card with status %v, want none", code)
		}
		if n := b.calls.Load(); n != 0 {
			t.Errorf("MeCardPath called %v times for an allprop request, want 0", n)
		}

		const include = `<?xml version="1.0" encoding="utf-8"?>
<D:propfind xmlns:D="DAV:" xmlns:CS="http://calendarserver.org/ns/">
  <D:allprop/><D:include><CS:me-card/></D:include>
</D:propfind>`
		resp = propFindMeCardResponse(t, &Handler{Backend: b}, meCardHomeSet, "0", include)
		if got := meCardHref(t, resp); got != meCardObject {
			t.Errorf("allprop with include: got me-card href %q, want %q", got, meCardObject)
		}
	})

	t.Run("not requested", func(t *testing.T) {
		const body = `<?xml version="1.0" encoding="utf-8"?><D:propfind xmlns:D="DAV:"><D:prop><D:resourcetype/></D:prop></D:propfind>`
		b := &meCardTestBackend{meCard: meCardObject}
		propFindMeCardResponse(t, &Handler{Backend: b}, meCardHomeSet, "0", body)
		if n := b.calls.Load(); n != 0 {
			t.Errorf("MeCardPath called %v times, want 0", n)
		}
	})

	t.Run("other resources", func(t *testing.T) {
		b := &meCardTestBackend{meCard: meCardObject}
		h := &Handler{Backend: b}
		for _, p := range []string{meCardPrincipal, meCardAddressBook, meCardObject} {
			resp := propFindMeCardResponse(t, h, p, "0", propFindMeCard)
			if code := propStatus(resp, meCardPropName); code != http.StatusNotFound {
				t.Errorf("%v: got status %v, want 404 Not Found", p, code)
			}
		}
	})
}
