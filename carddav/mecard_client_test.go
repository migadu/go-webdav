package carddav

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// TestClientFindMeCard checks that FindMeCard reads the me-card property of
// the address book home set, as servers such as SabreDAV and CalendarServer
// return it.
func TestClientFindMeCard(t *testing.T) {
	for _, tc := range []struct {
		name string
		// The propstat elements of the response, with {url} replaced by the
		// URL of the server
		propstats string
		want      string
	}{
		{
			name:      "path",
			propstats: `<D:propstat><D:prop><CS:me-card><D:href>/user/contacts/default/me.vcf</D:href></CS:me-card></D:prop><D:status>HTTP/1.1 200 OK</D:status></D:propstat>`,
			want:      "/user/contacts/default/me.vcf",
		},
		{
			name:      "URL",
			propstats: `<D:propstat><D:prop><CS:me-card><D:href>{url}/user/contacts/default/me.vcf</D:href></CS:me-card></D:prop><D:status>HTTP/1.1 200 OK</D:status></D:propstat>`,
			want:      "/user/contacts/default/me.vcf",
		},
		{
			// SabreDAV documents the href as relative to the home set
			name:      "relative",
			propstats: `<D:propstat><D:prop><CS:me-card><D:href>default/me.vcf</D:href></CS:me-card></D:prop><D:status>HTTP/1.1 200 OK</D:status></D:propstat>`,
			want:      "/user/contacts/default/me.vcf",
		},
		{
			name:      "other host",
			propstats: `<D:propstat><D:prop><CS:me-card><D:href>https://contacts.example.com/1234/card/me.vcf</D:href></CS:me-card></D:prop><D:status>HTTP/1.1 200 OK</D:status></D:propstat>`,
			want:      "https://contacts.example.com/1234/card/me.vcf",
		},
		{
			name:      "not set",
			propstats: `<D:propstat><D:prop><CS:me-card/></D:prop><D:status>HTTP/1.1 404 Not Found</D:status></D:propstat>`,
			want:      "",
		},
		{
			name:      "missing",
			propstats: `<D:propstat><D:prop><D:resourcetype><D:collection/></D:resourcetype></D:prop><D:status>HTTP/1.1 200 OK</D:status></D:propstat>`,
			want:      "",
		},
		{
			name:      "empty",
			propstats: `<D:propstat><D:prop><CS:me-card/></D:prop><D:status>HTTP/1.1 200 OK</D:status></D:propstat>`,
			want:      "",
		},
		{
			// A reference to the request URL (RFC 3986 section 4.4), unlike
			// a me-card without an href
			name:      "empty href",
			propstats: `<D:propstat><D:prop><CS:me-card><D:href></D:href></CS:me-card></D:prop><D:status>HTTP/1.1 200 OK</D:status></D:propstat>`,
			want:      meCardHomeSet,
		},
		{
			// No spec limits me-card to one href. SabreDAV decodes it as
			// any number of hrefs and takes the first one.
			name:      "two hrefs",
			propstats: `<D:propstat><D:prop><CS:me-card><D:href>/user/contacts/default/me.vcf</D:href><D:href>/user/contacts/default/other.vcf</D:href></CS:me-card></D:prop><D:status>HTTP/1.1 200 OK</D:status></D:propstat>`,
			want:      "/user/contacts/default/me.vcf",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var ts *httptest.Server
			ts = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				body, _ := io.ReadAll(r.Body)
				if r.Method != "PROPFIND" || r.URL.Path != meCardHomeSet || r.Header.Get("Depth") != "0" {
					t.Errorf("got %v %v with Depth %q, want PROPFIND %v with Depth 0", r.Method, r.URL.Path, r.Header.Get("Depth"), meCardHomeSet)
				}
				if !strings.Contains(string(body), `<me-card xmlns="http://calendarserver.org/ns/">`) {
					t.Errorf("me-card not requested:\n%s", body)
				}
				w.Header().Set("Content-Type", "application/xml; charset=utf-8")
				w.WriteHeader(http.StatusMultiStatus)
				io.WriteString(w, `<?xml version="1.0" encoding="utf-8"?>
<D:multistatus xmlns:D="DAV:" xmlns:CS="http://calendarserver.org/ns/">
  <D:response>
    <D:href>`+meCardHomeSet+`</D:href>
    `+strings.ReplaceAll(tc.propstats, "{url}", ts.URL)+`
  </D:response>
</D:multistatus>`)
			}))
			defer ts.Close()

			c, err := NewClient(ts.Client(), ts.URL)
			if err != nil {
				t.Fatal(err)
			}
			got, err := c.FindMeCard(context.Background(), meCardHomeSet)
			if err != nil {
				t.Fatalf("FindMeCard: %v", err)
			}
			if got != tc.want {
				t.Errorf("got %q, want %q", got, tc.want)
			}
		})
	}

	t.Run("no home set", func(t *testing.T) {
		ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/xml; charset=utf-8")
			w.WriteHeader(http.StatusMultiStatus)
			io.WriteString(w, `<?xml version="1.0" encoding="utf-8"?>
<D:multistatus xmlns:D="DAV:">
  <D:response>
    <D:href>`+meCardHomeSet+`</D:href>
    <D:status>HTTP/1.1 404 Not Found</D:status>
  </D:response>
</D:multistatus>`)
		}))
		defer ts.Close()

		c, err := NewClient(ts.Client(), ts.URL)
		if err != nil {
			t.Fatal(err)
		}
		if got, err := c.FindMeCard(context.Background(), meCardHomeSet); err == nil {
			t.Errorf("got %q, want an error", got)
		}
	})
}

// TestClientServerMeCard finds the me card with the client, from the server.
func TestClientServerMeCard(t *testing.T) {
	for _, tc := range []struct {
		name, meCard string
	}{
		{"set", meCardObject},
		{"not set", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ts := httptest.NewServer(&Handler{Backend: &meCardTestBackend{meCard: tc.meCard}})
			defer ts.Close()

			ctx := context.Background()
			c, err := NewClient(ts.Client(), ts.URL)
			if err != nil {
				t.Fatal(err)
			}
			homeSet, err := c.FindAddressBookHomeSet(ctx, meCardPrincipal)
			if err != nil {
				t.Fatalf("FindAddressBookHomeSet: %v", err)
			}
			got, err := c.FindMeCard(ctx, homeSet)
			if err != nil {
				t.Fatalf("FindMeCard: %v", err)
			}
			if got != tc.meCard {
				t.Fatalf("got %q, want %q", got, tc.meCard)
			}
			if got == "" {
				return
			}
			ao, err := c.GetAddressObject(ctx, got)
			if err != nil {
				t.Fatalf("GetAddressObject: %v", err)
			}
			if fn := ao.Card.Value("FN"); fn != "Me" {
				t.Errorf("got FN %q, want Me", fn)
			}
		})
	}
}
