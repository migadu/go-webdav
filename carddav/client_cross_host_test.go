package carddav

import (
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync"
	"testing"
)

// fakeHost answers the requests for a host name with canned responses, by
// method and request URI, and records the other requests it gets.
type fakeHost struct {
	name   string                      // e.g. "dav.test"
	routes map[string]http.HandlerFunc // e.g. "GET /a.vcf"

	mu         sync.Mutex
	unexpected []string
}

func (h *fakeHost) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	key := r.Method + " " + r.URL.RequestURI()
	if f, ok := h.routes[key]; ok {
		f(w, r)
		return
	}
	h.mu.Lock()
	h.unexpected = append(h.unexpected, key)
	h.mu.Unlock()
	http.Error(w, "unexpected request to "+h.name, http.StatusNotFound)
}

// newFakeHostsClient starts a server for each host, and returns an HTTP client
// reaching them by their names on port 80. The test fails if a host gets a
// request it has no response for.
func newFakeHostsClient(t *testing.T, hosts ...*fakeHost) *http.Client {
	addrs := make(map[string]string)
	for _, h := range hosts {
		ts := httptest.NewServer(h)
		t.Cleanup(ts.Close)
		t.Cleanup(func() {
			h.mu.Lock()
			defer h.mu.Unlock()
			if len(h.unexpected) > 0 {
				t.Errorf("%v got unexpected requests: %q", h.name, h.unexpected)
			}
		})
		addrs[h.name+":80"] = ts.Listener.Addr().String()
	}

	var dialer net.Dialer
	transport := &http.Transport{
		DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
			a, ok := addrs[addr]
			if !ok {
				return nil, fmt.Errorf("unknown host %v", addr)
			}
			return dialer.DialContext(ctx, network, a)
		},
	}
	t.Cleanup(transport.CloseIdleConnections)
	return &http.Client{Transport: transport}
}

func multiStatusHandler(body string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", `application/xml; charset="utf-8"`)
		w.WriteHeader(http.StatusMultiStatus)
		io.WriteString(w, `<?xml version="1.0" encoding="utf-8"?>
<d:multistatus xmlns:d="DAV:" xmlns:card="urn:ietf:params:xml:ns:carddav">`+body+`</d:multistatus>`)
	}
}

// reportHandler answers REPORT requests with the multi-status body for the
// report's name, found in the request body.
func reportHandler(t *testing.T, bodies map[string]string, check func(report, reqBody string)) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		b, err := io.ReadAll(r.Body)
		if err != nil {
			t.Errorf("failed to read REPORT body: %v", err)
		}
		for report, body := range bodies {
			if strings.Contains(string(b), report) {
				if check != nil {
					check(report, string(b))
				}
				multiStatusHandler(body)(w, r)
				return
			}
		}
		t.Errorf("unexpected REPORT %v: %s", r.URL, b)
		http.Error(w, "unexpected REPORT", http.StatusBadRequest)
	}
}

func propStat(props string) string {
	return `<d:propstat><d:prop>` + props + `</d:prop><d:status>HTTP/1.1 200 OK</d:status></d:propstat>`
}

const crossHostCard = "BEGIN:VCARD\nVERSION:4.0\nUID:alice\nFN:Alice\nEND:VCARD\n"

func vcardHandler(etag string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/vcard")
		w.Header().Set("ETag", etag)
		io.WriteString(w, crossHostCard)
	}
}

// TestClientCrossHost reproduces iCloud, whose principal on contacts.icloud.com
// has its address book home set on another host, e.g. p37-contacts.icloud.com,
// itself listing its resources with relative hrefs. Every request for the home
// set and its resources must go to that other host.
func TestClientCrossHost(t *testing.T) {
	const (
		bookPath  = "/1234567/carddavhome/card/"
		alicePath = bookPath + "alice.vcf"
	)
	const (
		homeSet = "http://p37-contacts.test/1234567/carddavhome/"
		book    = "http://p37-contacts.test" + bookPath
		alice   = "http://p37-contacts.test" + alicePath
		bob     = "http://p37-contacts.test" + bookPath + "bob.vcf"
		gone    = "http://p37-contacts.test" + bookPath + "gone.vcf"
	)

	principalHost := &fakeHost{name: "contacts.test", routes: map[string]http.HandlerFunc{
		"PROPFIND /": multiStatusHandler(`<d:response><d:href>/</d:href>` +
			propStat(`<d:current-user-principal><d:href>/1234567/principal/</d:href></d:current-user-principal>`) +
			`</d:response>`),
		// The default port is explicit, as with iCloud
		"PROPFIND /1234567/principal/": multiStatusHandler(`<d:response><d:href>/1234567/principal/</d:href>` +
			propStat(`<card:addressbook-home-set><d:href>http://p37-contacts.test:80/1234567/carddavhome/</d:href></card:addressbook-home-set>`) +
			`</d:response>`),
	}}

	var multiGetBody string
	homeHost := &fakeHost{name: "p37-contacts.test", routes: map[string]http.HandlerFunc{
		"PROPFIND /1234567/carddavhome/": multiStatusHandler(`
			<d:response><d:href>/1234567/carddavhome/</d:href>` +
			propStat(`<d:resourcetype><d:collection/></d:resourcetype>`) + `</d:response>
			<d:response><d:href>/1234567/carddavhome/card/</d:href>` +
			propStat(`<d:resourcetype><d:collection/><card:addressbook/></d:resourcetype><d:displayname>Contacts</d:displayname>`) +
			`</d:response>`),
		"REPORT " + bookPath: reportHandler(t, map[string]string{
			// A reference relative to the request URI
			"addressbook-query": `<d:response><d:href>alice.vcf</d:href>` +
				propStat(`<d:getetag>"1"</d:getetag><card:address-data>`+crossHostCard+`</card:address-data>`) +
				`</d:response>`,
			"addressbook-multiget": `<d:response><d:href>` + alicePath + `</d:href>` +
				propStat(`<d:getetag>"1"</d:getetag><card:address-data>`+crossHostCard+`</card:address-data>`) +
				`</d:response>`,
			"sync-collection": `
				<d:response><d:href>` + bookPath + `</d:href>` + propStat(`<d:getetag>"0"</d:getetag>`) + `</d:response>
				<d:response><d:href>` + alicePath + `</d:href>` + propStat(`<d:getetag>"1"</d:getetag>`) + `</d:response>
				<d:response><d:href>` + bookPath + `gone.vcf</d:href><d:status>HTTP/1.1 404 Not Found</d:status></d:response>
				<d:sync-token>http://p37-contacts.test/sync/2</d:sync-token>`,
		}, func(report, reqBody string) {
			if report == "addressbook-multiget" {
				multiGetBody = reqBody
			}
		}),
		"GET " + alicePath: vcardHandler(`"1"`),
		"PUT " + bookPath + "bob.vcf": func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("ETag", `"2"`)
			w.Header().Set("Location", bookPath+"bob.vcf")
			w.WriteHeader(http.StatusCreated)
		},
	}}

	ctx := context.Background()
	c, err := NewClient(newFakeHostsClient(t, principalHost, homeHost), "http://contacts.test")
	if err != nil {
		t.Fatal(err)
	}

	principal, err := c.FindCurrentUserPrincipal(ctx)
	if err != nil {
		t.Fatalf("FindCurrentUserPrincipal() = %v", err)
	} else if principal != "/1234567/principal/" {
		t.Errorf("FindCurrentUserPrincipal() = %q, want %q", principal, "/1234567/principal/")
	}

	gotHomeSet, err := c.FindAddressBookHomeSet(ctx, principal)
	if err != nil {
		t.Fatalf("FindAddressBookHomeSet() = %v", err)
	} else if gotHomeSet != homeSet {
		t.Errorf("FindAddressBookHomeSet() = %q, want %q", gotHomeSet, homeSet)
	}

	abs, err := c.FindAddressBooks(ctx, gotHomeSet)
	if err != nil {
		t.Fatalf("FindAddressBooks() = %v", err)
	}
	if len(abs) != 1 || abs[0].Path != book || abs[0].Name != "Contacts" {
		t.Fatalf("FindAddressBooks() = %+v, want an address book at %q", abs, book)
	}

	objs, err := c.QueryAddressBook(ctx, abs[0].Path, &AddressBookQuery{})
	if err != nil {
		t.Fatalf("QueryAddressBook() = %v", err)
	}
	if len(objs) != 1 || objs[0].Path != alice || objs[0].ETag != "1" {
		t.Fatalf("QueryAddressBook() = %+v, want an address object at %q", objs, alice)
	}

	ao, err := c.GetAddressObject(ctx, objs[0].Path)
	if err != nil {
		t.Fatalf("GetAddressObject() = %v", err)
	}
	if ao.Path != alice || ao.ETag != "1" {
		t.Errorf("GetAddressObject() = %+v, want an address object at %q with ETag 1", ao, alice)
	}

	objs, err = c.MultiGetAddressBook(ctx, abs[0].Path, &AddressBookMultiGet{Paths: []string{alice}})
	if err != nil {
		t.Fatalf("MultiGetAddressBook() = %v", err)
	}
	if len(objs) != 1 || objs[0].Path != alice {
		t.Errorf("MultiGetAddressBook() = %+v, want an address object at %q", objs, alice)
	}
	// The resources are on the host the request is sent to: their paths are
	// enough
	if want := ">" + alicePath + "<"; !strings.Contains(multiGetBody, want) {
		t.Errorf("addressbook-multiget body = %s, want an href %q", multiGetBody, alicePath)
	}

	ao, err = c.PutAddressObject(ctx, abs[0].Path+"bob.vcf", ao.Card, nil)
	if err != nil {
		t.Fatalf("PutAddressObject() = %v", err)
	}
	if ao.Path != bob || ao.ETag != "2" {
		t.Errorf("PutAddressObject() = %+v, want an address object at %q with ETag 2", ao, bob)
	}

	syncRes, err := c.SyncCollection(ctx, abs[0].Path, &SyncQuery{})
	if err != nil {
		t.Fatalf("SyncCollection() = %v", err)
	}
	if len(syncRes.Updated) != 1 || syncRes.Updated[0].Path != alice {
		t.Errorf("SyncCollection() updated %+v, want an address object at %q", syncRes.Updated, alice)
	}
	if want := []string{gone}; !reflect.DeepEqual(syncRes.Deleted, want) {
		t.Errorf("SyncCollection() deleted %q, want %q", syncRes.Deleted, want)
	}
}

// TestClientSameHostAbsoluteHrefs checks that absolute hrefs on the endpoint's
// host are reduced to their paths, even with another scheme or port, as
// reported by some servers behind a reverse proxy.
func TestClientSameHostAbsoluteHrefs(t *testing.T) {
	const (
		bookPath  = "/1234567/carddavhome/card/"
		alicePath = bookPath + "alice.vcf"
	)

	host := &fakeHost{name: "dav.test", routes: map[string]http.HandlerFunc{
		"PROPFIND /": multiStatusHandler(`<d:response><d:href>http://dav.test/</d:href>` +
			propStat(`<d:current-user-principal><d:href>http://dav.test/1234567/principal/</d:href></d:current-user-principal>`) +
			`</d:response>`),
		"PROPFIND /1234567/principal/": multiStatusHandler(`<d:response><d:href>http://dav.test/1234567/principal/</d:href>` +
			propStat(`<card:addressbook-home-set><d:href>http://DAV.test:80/1234567/carddavhome/</d:href></card:addressbook-home-set>`) +
			`</d:response>`),
		"PROPFIND /1234567/carddavhome/": multiStatusHandler(`
			<d:response><d:href>http://dav.test/1234567/carddavhome/</d:href>` +
			propStat(`<d:resourcetype><d:collection/></d:resourcetype>`) + `</d:response>
			<d:response><d:href>https://dav.test:8443/1234567/carddavhome/card/</d:href>` +
			propStat(`<d:resourcetype><d:collection/><card:addressbook/></d:resourcetype>`) +
			`</d:response>`),
		"REPORT " + bookPath: reportHandler(t, map[string]string{
			"addressbook-query": `<d:response><d:href>http://dav.test` + alicePath + `</d:href>` +
				propStat(`<d:getetag>"1"</d:getetag><card:address-data>`+crossHostCard+`</card:address-data>`) +
				`</d:response>`,
		}, nil),
		"GET " + alicePath: vcardHandler(`"1"`),
	}}

	ctx := context.Background()
	c, err := NewClient(newFakeHostsClient(t, host), "http://dav.test/")
	if err != nil {
		t.Fatal(err)
	}

	principal, err := c.FindCurrentUserPrincipal(ctx)
	if err != nil {
		t.Fatalf("FindCurrentUserPrincipal() = %v", err)
	} else if principal != "/1234567/principal/" {
		t.Errorf("FindCurrentUserPrincipal() = %q, want %q", principal, "/1234567/principal/")
	}

	homeSet, err := c.FindAddressBookHomeSet(ctx, principal)
	if err != nil {
		t.Fatalf("FindAddressBookHomeSet() = %v", err)
	} else if homeSet != "/1234567/carddavhome/" {
		t.Errorf("FindAddressBookHomeSet() = %q, want %q", homeSet, "/1234567/carddavhome/")
	}

	abs, err := c.FindAddressBooks(ctx, homeSet)
	if err != nil {
		t.Fatalf("FindAddressBooks() = %v", err)
	}
	if len(abs) != 1 || abs[0].Path != bookPath {
		t.Fatalf("FindAddressBooks() = %+v, want an address book at %q", abs, bookPath)
	}

	objs, err := c.QueryAddressBook(ctx, abs[0].Path, &AddressBookQuery{})
	if err != nil {
		t.Fatalf("QueryAddressBook() = %v", err)
	}
	if len(objs) != 1 || objs[0].Path != alicePath {
		t.Fatalf("QueryAddressBook() = %+v, want an address object at %q", objs, alicePath)
	}

	ao, err := c.GetAddressObject(ctx, objs[0].Path)
	if err != nil {
		t.Fatalf("GetAddressObject() = %v", err)
	}
	if ao.Path != alicePath {
		t.Errorf("GetAddressObject() = %+v, want an address object at %q", ao, alicePath)
	}
}

// TestClientHrefQuery checks that the query of an href, which a path can't
// carry, is kept: the resource is then returned as an absolute URL.
func TestClientHrefQuery(t *testing.T) {
	const bookPath = "/card/"
	const alice = "http://dav.test/card/alice.vcf?rev=2"

	host := &fakeHost{name: "dav.test", routes: map[string]http.HandlerFunc{
		"REPORT " + bookPath: reportHandler(t, map[string]string{
			"addressbook-query": `<d:response><d:href>/card/alice.vcf?rev=2</d:href>` +
				propStat(`<d:getetag>"1"</d:getetag><card:address-data>`+crossHostCard+`</card:address-data>`) +
				`</d:response>`,
		}, nil),
		"GET /card/alice.vcf?rev=2": vcardHandler(`"1"`),
	}}

	ctx := context.Background()
	c, err := NewClient(newFakeHostsClient(t, host), "http://dav.test/")
	if err != nil {
		t.Fatal(err)
	}

	objs, err := c.QueryAddressBook(ctx, bookPath, &AddressBookQuery{})
	if err != nil {
		t.Fatalf("QueryAddressBook() = %v", err)
	}
	if len(objs) != 1 || objs[0].Path != alice {
		t.Fatalf("QueryAddressBook() = %+v, want an address object at %q", objs, alice)
	}

	ao, err := c.GetAddressObject(ctx, objs[0].Path)
	if err != nil {
		t.Fatalf("GetAddressObject() = %v", err)
	}
	if ao.Path != alice {
		t.Errorf("GetAddressObject() = %+v, want an address object at %q", ao, alice)
	}
}
