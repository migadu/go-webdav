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

		// On another host
		{endpoint, "https://p37-contacts.icloud.com:443/1234567/carddavhome/", other},
		{endpoint, "https://user:pass@p37-contacts.icloud.com/a#frag", "https://p37-contacts.icloud.com/a"},
		{endpoint, "http://p37-contacts.icloud.com:80/a", "http://p37-contacts.icloud.com/a"},
		{endpoint, "https://p37-contacts.icloud.com:8443/a", "https://p37-contacts.icloud.com:8443/a"},
		{other, "/1234567/carddavhome/card/", other + "card/"},
		{other, "card/a%20b.vcf", other + "card/a%20b.vcf"},
		{other, "", other},

		// With a query
		{endpoint, "/a?x=1", "https://contacts.icloud.com/a?x=1"},
		{endpoint, "http://contacts.icloud.com/a?x=1", "https://contacts.icloud.com/a?x=1"},
		{other, "card/?x=1", other + "card/?x=1"},
	} {
		got := c.HrefPath(mustParseURL(t, tc.base), mustParseURL(t, tc.href))
		if got != tc.want {
			t.Errorf("HrefPath(%q, %q) = %q, want %q", tc.base, tc.href, got, tc.want)
		}
	}

	if got, want := c.HrefPath(nil, mustParseURL(t, "card/")), "card/"; got != want {
		t.Errorf("HrefPath(nil, %q) = %q, want %q", "card/", got, want)
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
}

func TestClientRequestHrefs(t *testing.T) {
	c, err := NewClient(nil, "https://contacts.icloud.com/")
	if err != nil {
		t.Fatal(err)
	}

	const book = "https://p37-contacts.icloud.com/1234567/carddavhome/card/"
	hrefs, err := c.RequestHrefs(book, []string{
		"/a.vcf",
		book + "b.vcf?x=1",
		"https://contacts.icloud.com/c.vcf",
		"https://user:pass@p38-contacts.icloud.com/d.vcf",
	})
	if err != nil {
		t.Fatal(err)
	}
	want := []string{
		"/a.vcf",
		"/1234567/carddavhome/card/b.vcf?x=1",
		"https://contacts.icloud.com/c.vcf",
		"https://p38-contacts.icloud.com/d.vcf",
	}
	if len(hrefs) != len(want) {
		t.Fatalf("RequestHrefs() = %v, want %q", hrefs, want)
	}
	for i := range hrefs {
		if got := hrefs[i].String(); got != want[i] {
			t.Errorf("RequestHrefs()[%v] = %q, want %q", i, got, want[i])
		}
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
