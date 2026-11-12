package internal

import (
	"net/http"
	"net/url"
	"testing"
)

func mustParseURL(t *testing.T, s string) *url.URL {
	t.Helper()
	u, err := url.Parse(s)
	if err != nil {
		t.Fatal(err)
	}
	return u
}

func TestClientHrefPath(t *testing.T) {
	c, err := NewClient(nil, "https://contacts.icloud.com/")
	if err != nil {
		t.Fatal(err)
	}

	const (
		endpoint = "https://contacts.icloud.com/1234567/principal/"
		other    = "https://p37-contacts.icloud.com/1234567/carddavhome/"
	)
	for _, tc := range []struct {
		base, href, want string
	}{
		// On the endpoint's host: as before
		{endpoint, "/1234567/carddavhome/", "/1234567/carddavhome/"},
		// A relative reference is relative to the URL of the request, not
		// to the endpoint (RFC 3986 section 5.2), and an empty one refers
		// to that URL (section 4.4)
		{endpoint, "card/", "/1234567/principal/card/"},
		{endpoint, "card/a%20b.vcf", "/1234567/principal/card/a b.vcf"},
		{endpoint, "../carddavhome/a.vcf", "/1234567/carddavhome/a.vcf"},
		{endpoint, "./a.vcf", "/1234567/principal/a.vcf"},
		{endpoint, "", "/1234567/principal/"},
		{endpoint, "#frag", "/1234567/principal/"},
		{endpoint, "https://contacts.icloud.com/a%20b/", "/a b/"},
		{endpoint, "https://contacts.icloud.com:443/a", "/a"},
		{endpoint, "https://Contacts.iCloud.com/a", "/a"},
		{endpoint, "http://contacts.icloud.com:8080/a", "/a"},
		{other, "https://contacts.icloud.com/a", "/a"},
		{endpoint, "mailto:alice@example.org", ""},
		// A network-path reference names its host (RFC 3986 section 4.2),
		// and takes the scheme of the request URL
		{other, "//contacts.icloud.com/1234567/principal/", "/1234567/principal/"},
		{endpoint, "//Contacts.iCloud.com:443/a%20b", "/a b"},
		// An empty path with a host is "/" (RFC 3986 section 6.2.3), not an
		// empty reference
		{endpoint, "https://contacts.icloud.com", "/"},
		{endpoint, "https://contacts.icloud.com:443", "/"},
		{other, "//contacts.icloud.com", "/"},
		{endpoint, "https://contacts.icloud.com?x=1", "https://contacts.icloud.com/?x=1"},

		// On another host
		{endpoint, "https://p37-contacts.icloud.com:443/1234567/carddavhome/", other},
		{endpoint, "https://user:pass@p37-contacts.icloud.com/a#frag", "https://p37-contacts.icloud.com/a"},
		{endpoint, "http://p37-contacts.icloud.com:80/a", "http://p37-contacts.icloud.com/a"},
		{endpoint, "https://p37-contacts.icloud.com:8443/a", "https://p37-contacts.icloud.com:8443/a"},
		{other, "/1234567/carddavhome/card/", other + "card/"},
		{other, "card/a%20b.vcf", other + "card/a%20b.vcf"},
		{other, "", other},
		{endpoint, "//p37-contacts.icloud.com/1234567/carddavhome/", other},
		{endpoint, "//user:pass@p37-contacts.icloud.com:443/a#frag", "https://p37-contacts.icloud.com/a"},
		{"http://contacts.icloud.com/", "//p37-contacts.icloud.com/a", "http://p37-contacts.icloud.com/a"},
		{endpoint, "https://p37-contacts.icloud.com", "https://p37-contacts.icloud.com/"},
		{endpoint, "//p37-contacts.icloud.com:443", "https://p37-contacts.icloud.com/"},
		{endpoint, "https://p37-contacts.icloud.com:/a", "https://p37-contacts.icloud.com/a"},

		// With a query
		{endpoint, "/a?x=1", "https://contacts.icloud.com/a?x=1"},
		{endpoint, "http://contacts.icloud.com/a?x=1", "https://contacts.icloud.com/a?x=1"},
		{other, "card/?x=1", other + "card/?x=1"},
		{other, "//contacts.icloud.com/a?x=1", "https://contacts.icloud.com/a?x=1"},
	} {
		got := c.HrefPath(mustParseURL(t, tc.base), mustParseURL(t, tc.href))
		if got != tc.want {
			t.Errorf("HrefPath(%q, %q) = %q, want %q", tc.base, tc.href, got, tc.want)
		}
	}

	for _, tc := range []struct {
		href, want string
	}{
		{"card/", "card/"},
		{"//p37-contacts.icloud.com/1234567/carddavhome/", other},
		{"https://contacts.icloud.com", "/"},
		{"//contacts.icloud.com", "/"},
	} {
		if got := c.HrefPath(nil, mustParseURL(t, tc.href)); got != tc.want {
			t.Errorf("HrefPath(nil, %q) = %q, want %q", tc.href, got, tc.want)
		}
	}
}

func TestClientResolveHref(t *testing.T) {
	c, err := NewClient(nil, "https://user:pass@dav.example.org/dav/")
	if err != nil {
		t.Fatal(err)
	}

	for _, tc := range []struct {
		href, want string
	}{
		{"", "https://user:pass@dav.example.org/dav/"},
		{"/a b", "https://user:pass@dav.example.org/a%20b"},
		{"a?b", "https://user:pass@dav.example.org/dav/a%3Fb"},
		{"https://dav.example.org/a?x=1", "https://user:pass@dav.example.org/a?x=1"},
		{"https://dav.example.org:443/a", "https://user:pass@dav.example.org:443/a"},
		{"https://dav.example.org:8443/a", "https://dav.example.org:8443/a"},
		{"HTTPS://dav.example.org/a", "https://user:pass@dav.example.org/a"},
		{"https://other.example.org/a?x=1", "https://other.example.org/a?x=1"},
		{"http://dav.example.org/a", "http://dav.example.org/a"},
		{"https://me@other.example.org/a", "https://me@other.example.org/a"},
	} {
		u, err := c.ResolveHref(tc.href)
		if err != nil {
			t.Errorf("ResolveHref(%q) = %v", tc.href, err)
		} else if got := u.String(); got != tc.want {
			t.Errorf("ResolveHref(%q) = %q, want %q", tc.href, got, tc.want)
		}
	}

	if _, err := c.ResolveHref("https://other.example.org/%zz"); err == nil {
		t.Errorf("ResolveHref() of an invalid URL succeeded")
	}

	// An escaped "/" in the endpoint's path, as a DNS TXT record may give
	// (RFC 6764 section 4), is part of a segment: it stays escaped, and a
	// relative path is resolved against it as such
	for _, endpoint := range []string{"https://dav.example.org/dav/a%2Fb/", "https://dav.example.org/dav/a%2Fb"} {
		c, err := NewClient(nil, endpoint)
		if err != nil {
			t.Fatal(err)
		}
		for _, tc := range []struct {
			href, want string
		}{
			{"", endpoint},
			{"c.vcf", "https://dav.example.org/dav/a%2Fb/c.vcf"},
			{"c d/", "https://dav.example.org/dav/a%2Fb/c%20d/"},
			{"c?d", "https://dav.example.org/dav/a%2Fb/c%3Fd"},
			{"../c", "https://dav.example.org/dav/c"},
			{"/c", "https://dav.example.org/c"},
		} {
			u, err := c.ResolveHref(tc.href)
			if err != nil {
				t.Errorf("%v: ResolveHref(%q) = %v", endpoint, tc.href, err)
			} else if got := u.String(); got != tc.want {
				t.Errorf("%v: ResolveHref(%q) = %q, want %q", endpoint, tc.href, got, tc.want)
			}
		}
	}
}

func TestClientRequestHrefs(t *testing.T) {
	c, err := NewClient(nil, "https://contacts.icloud.com/")
	if err != nil {
		t.Fatal(err)
	}

	const book = "https://p37-contacts.icloud.com/1234567/carddavhome/card/"
	for _, tc := range []struct {
		target      string
		paths, want []string
	}{{
		target: book,
		paths: []string{
			"/a.vcf",
			book + "b.vcf?x=1",
			"https://contacts.icloud.com/c.vcf",
			"https://user:pass@p38-contacts.icloud.com/d.vcf",
			// The default port of the scheme is the same host (RFC 3986
			// section 6.2.3)
			"https://p37-contacts.icloud.com:443/1234567/carddavhome/card/e.vcf",
			"http://p37-contacts.icloud.com:443/f.vcf",
		},
		want: []string{
			"/a.vcf",
			"/1234567/carddavhome/card/b.vcf?x=1",
			"https://contacts.icloud.com/c.vcf",
			"https://p38-contacts.icloud.com/d.vcf",
			"/1234567/carddavhome/card/e.vcf",
			"http://p37-contacts.icloud.com:443/f.vcf",
		},
	}, {
		target: "/card/",
		paths: []string{
			"https://contacts.icloud.com:443/card/a.vcf",
			"https://contacts.icloud.com:/card/b.vcf",
			"https://contacts.icloud.com",
			"https://contacts.icloud.com:8443/card/c.vcf",
		},
		want: []string{
			"/card/a.vcf",
			"/card/b.vcf",
			"/",
			"https://contacts.icloud.com:8443/card/c.vcf",
		},
	}} {
		hrefs, err := c.RequestHrefs(tc.target, tc.paths)
		if err != nil {
			t.Fatal(err)
		}
		if len(hrefs) != len(tc.want) {
			t.Fatalf("RequestHrefs(%q) = %v, want %q", tc.target, hrefs, tc.want)
		}
		for i := range hrefs {
			if got := hrefs[i].String(); got != tc.want[i] {
				t.Errorf("RequestHrefs(%q, %q) = %q, want %q", tc.target, tc.paths[i], got, tc.want[i])
			}
		}
	}

	// The endpoint's port can be the explicit default one too
	c, err = NewClient(nil, "https://dav.example.org:443/")
	if err != nil {
		t.Fatal(err)
	}
	hrefs, err := c.RequestHrefs("/card/", []string{"https://dav.example.org/card/a.vcf"})
	if err != nil {
		t.Fatal(err)
	}
	if got, want := hrefs[0].String(), "/card/a.vcf"; got != want {
		t.Errorf("RequestHrefs() = %q, want %q", got, want)
	}
}

func TestResponsePath(t *testing.T) {
	c, err := NewClient(nil, "https://contacts.icloud.com/")
	if err != nil {
		t.Fatal(err)
	}

	var ms MultiStatus
	ms.Responses = []Response{{
		Hrefs: []Href{{Path: "/1234567/carddavhome/card/"}},
	}, {
		Hrefs:  []Href{{Path: "gone.vcf"}},
		Status: &Status{Code: 404},
	}}
	base := mustParseURL(t, "https://p37-contacts.icloud.com/1234567/carddavhome/card/")
	reqURL := mustParseURL(t, "https://contacts.icloud.com/redirected")
	c.normalizeMultiStatus(&ms, &http.Request{URL: reqURL}, &http.Response{Request: &http.Request{URL: base}})

	if got, err := ms.Responses[0].Path(); err != nil || got != base.String() {
		t.Errorf("Path() = %q, %v, want %q", got, err, base.String())
	}
	got, err := ms.Responses[1].Path()
	if want := base.String() + "gone.vcf"; got != want || !IsNotFound(err) {
		t.Errorf("Path() = %q, %v, want %q and a 404 error", got, err, want)
	}
	if want := base.String() + "gone.vcf: 404 Not Found"; err == nil || err.Error() != want {
		t.Errorf("Path() error = %v, want %q", err, want)
	}

	home := Href(*mustParseURL(t, "https://p37-contacts.icloud.com:443/1234567/carddavhome/"))
	if got, want := ms.Responses[0].HrefPath(&home), "https://p37-contacts.icloud.com/1234567/carddavhome/"; got != want {
		t.Errorf("HrefPath() = %q, want %q", got, want)
	}
	principal := Href{Path: "/1234567/principal/"}
	if got, want := ms.Responses[0].HrefPath(&principal), "https://p37-contacts.icloud.com/1234567/principal/"; got != want {
		t.Errorf("HrefPath() = %q, want %q", got, want)
	}
}
