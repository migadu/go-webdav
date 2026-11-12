package webdav_test

import (
	"context"
	"crypto/tls"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/emersion/go-webdav"
)

// originServer answers the requests for any host name from routes, keyed by
// "scheme://host METHOD URI", and records each request it gets as such a key
// followed by the user of its basic credentials, or "-" without any.
type originServer struct {
	routes map[string]http.HandlerFunc

	mu   sync.Mutex
	seen []string
}

func (s *originServer) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	scheme := "http"
	if r.TLS != nil {
		scheme = "https"
	}
	host, _, _ := strings.Cut(r.Host, ":")
	key := scheme + "://" + host + " " + r.Method + " " + r.URL.RequestURI()
	user, _, ok := r.BasicAuth()
	if !ok {
		user = "-"
	}
	s.mu.Lock()
	s.seen = append(s.seen, key+" "+user)
	s.mu.Unlock()

	if f, ok := s.routes[key]; ok {
		f(w, r)
	} else {
		http.NotFound(w, r)
	}
}

// requests returns the requests the server got since the last call.
func (s *originServer) requests() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	l := s.seen
	s.seen = nil
	return l
}

// newOriginServer starts s, and returns an HTTP client reaching it by any host
// name: with https on port 443, and with plain HTTP on any other port.
func newOriginServer(t *testing.T, s *originServer) *http.Client {
	ts := httptest.NewServer(s)
	t.Cleanup(ts.Close)
	tlsServer := httptest.NewTLSServer(s)
	t.Cleanup(tlsServer.Close)

	var dialer net.Dialer
	transport := &http.Transport{
		DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
			target := ts.Listener.Addr().String()
			if _, port, _ := net.SplitHostPort(addr); port == "443" {
				target = tlsServer.Listener.Addr().String()
			}
			return dialer.DialContext(ctx, network, target)
		},
		TLSClientConfig: &tls.Config{InsecureSkipVerify: true},
	}
	t.Cleanup(transport.CloseIdleConnections)
	return &http.Client{Transport: transport}
}

// TestHTTPClientWithBasicAuthOrigin checks that the credentials of
// HTTPClientWithBasicAuth only go to the endpoint's origin (RFC 7617 section
// 2.2): not to the other hosts a server lists resources on, not over plain
// HTTP with an https endpoint, and not to the request sent again to follow a
// redirect to another domain, which the client strips them from.
func TestHTTPClientWithBasicAuthOrigin(t *testing.T) {
	text := func(s string) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) {
			io.WriteString(w, s)
		}
	}
	redirect := func(code int, location string) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Location", location)
			w.WriteHeader(code)
		}
	}
	created := func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusCreated)
	}
	s := &originServer{routes: map[string]http.HandlerFunc{
		"https://dav.test PROPFIND /dav/": func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", `application/xml; charset="utf-8"`)
			w.WriteHeader(http.StatusMultiStatus)
			const ok = `<d:status>HTTP/1.1 200 OK</d:status>`
			io.WriteString(w, `<?xml version="1.0" encoding="utf-8"?><d:multistatus xmlns:d="DAV:">
				<d:response><d:href>/dav/</d:href><d:propstat><d:prop>
				<d:resourcetype><d:collection/></d:resourcetype></d:prop>`+ok+`</d:propstat></d:response>
				<d:response><d:href>https://files.test/dav/a.txt</d:href><d:propstat><d:prop>
				<d:resourcetype/><d:getcontentlength>1</d:getcontentlength></d:prop>`+ok+`</d:propstat></d:response>
				<d:response><d:href>http://files.test/dav/b.txt</d:href><d:propstat><d:prop>
				<d:resourcetype/><d:getcontentlength>1</d:getcontentlength></d:prop>`+ok+`</d:propstat></d:response>
				</d:multistatus>`)
		},
		"https://dav.test GET /dav/own.txt":  text("own"),
		"https://files.test GET /dav/a.txt":  text("a"),
		"http://files.test GET /dav/b.txt":   text("b"),
		"http://dav.test GET /dav/plain.txt": text("plain"),
		"https://other.test GET /direct":     text("direct"),
		"https://dav.test MKCOL /dav/301/":   redirect(http.StatusMovedPermanently, "https://other.test/new/"),
		"https://dav.test MKCOL /dav/302/":   redirect(http.StatusFound, "https://other.test/new/"),
		"https://dav.test MKCOL /dav/307/":   redirect(http.StatusTemporaryRedirect, "https://other.test/new/"),
		"https://dav.test MKCOL /dav/308/":   redirect(http.StatusPermanentRedirect, "https://other.test/new/"),
		"https://dav.test MKCOL /dav/same/":  redirect(http.StatusFound, "/dav/new/"),
		"https://dav.test MKCOL /dav/sub/":   redirect(http.StatusFound, "https://files.dav.test/new/"),
		"https://dav.test MKCOL /dav/new/":   created,
		"https://other.test MKCOL /new/":     created,
		"https://files.dav.test MKCOL /new/": created,
	}}
	hc := webdav.HTTPClientWithBasicAuth(newOriginServer(t, s), "user", "secret")
	ctx := context.Background()

	open := func(c *webdav.Client, name string) error {
		rc, err := c.Open(ctx, name)
		if err != nil {
			return err
		}
		_, err = io.ReadAll(rc)
		rc.Close()
		return err
	}

	c, err := webdav.NewClient(hc, "https://dav.test/dav/")
	if err != nil {
		t.Fatal(err)
	}
	// The endpoint, and a URL with its default port, are on its origin
	explicitPort, err := webdav.NewClient(hc, "https://dav.test:443/dav/")
	if err != nil {
		t.Fatal(err)
	}
	// Making a host the endpoint of a Client trusts it with the credentials
	filesClient, err := webdav.NewClient(hc, "https://files.test/dav/")
	if err != nil {
		t.Fatal(err)
	}

	for _, tc := range []struct {
		name string
		do   func() error
		want []string
	}{{
		name: "ReadDir on the endpoint",
		do: func() error {
			fis, err := c.ReadDir(ctx, "", false)
			if err == nil && (len(fis) != 3 || fis[1].Path != "https://files.test/dav/a.txt" || fis[2].Path != "http://files.test/dav/b.txt") {
				t.Errorf("ReadDir() = %+v, want /dav/, https://files.test/dav/a.txt and http://files.test/dav/b.txt", fis)
			}
			return err
		},
		want: []string{"https://dav.test PROPFIND /dav/ user"},
	}, {
		name: "Open on the endpoint",
		do:   func() error { return open(c, "own.txt") },
		want: []string{"https://dav.test GET /dav/own.txt user"},
	}, {
		name: "Open with the default port",
		do:   func() error { return open(explicitPort, "https://dav.test/dav/own.txt") },
		want: []string{"https://dav.test GET /dav/own.txt user"},
	}, {
		name: "Open of an href on another host",
		do:   func() error { return open(c, "https://files.test/dav/a.txt") },
		want: []string{"https://files.test GET /dav/a.txt -"},
	}, {
		name: "Open of an http href on another host",
		do:   func() error { return open(c, "http://files.test/dav/b.txt") },
		want: []string{"http://files.test GET /dav/b.txt -"},
	}, {
		name: "Open of an http URL on the endpoint's host",
		do:   func() error { return open(c, "http://dav.test/dav/plain.txt") },
		want: []string{"http://dav.test GET /dav/plain.txt -"},
	}, {
		name: "Open with a Client for the other host",
		do:   func() error { return open(filesClient, "https://files.test/dav/a.txt") },
		want: []string{"https://files.test GET /dav/a.txt user"},
	}, {
		name: "301 to another domain",
		do:   func() error { return c.Mkdir(ctx, "301/") },
		want: []string{"https://dav.test MKCOL /dav/301/ user", "https://other.test GET /new/ -", "https://other.test MKCOL /new/ -"},
	}, {
		name: "302 to another domain",
		do:   func() error { return c.Mkdir(ctx, "302/") },
		want: []string{"https://dav.test MKCOL /dav/302/ user", "https://other.test GET /new/ -", "https://other.test MKCOL /new/ -"},
	}, {
		// http.Client follows these itself, with the method of the request
		name: "307 to another domain",
		do:   func() error { return c.Mkdir(ctx, "307/") },
		want: []string{"https://dav.test MKCOL /dav/307/ user", "https://other.test MKCOL /new/ -"},
	}, {
		name: "308 to another domain",
		do:   func() error { return c.Mkdir(ctx, "308/") },
		want: []string{"https://dav.test MKCOL /dav/308/ user", "https://other.test MKCOL /new/ -"},
	}, {
		name: "302 on the endpoint's origin",
		do:   func() error { return c.Mkdir(ctx, "same/") },
		want: []string{"https://dav.test MKCOL /dav/same/ user", "https://dav.test GET /dav/new/ user", "https://dav.test MKCOL /dav/new/ user"},
	}, {
		// As http.Client does for a 307 redirect
		name: "302 to a subdomain",
		do:   func() error { return c.Mkdir(ctx, "sub/") },
		want: []string{"https://dav.test MKCOL /dav/sub/ user", "https://files.dav.test GET /new/ user", "https://files.dav.test MKCOL /new/ user"},
	}, {
		// A request the caller sends itself is up to it
		name: "request sent directly",
		do: func() error {
			req, err := http.NewRequest(http.MethodGet, "https://other.test/direct", nil)
			if err != nil {
				return err
			}
			resp, err := hc.Do(req)
			if err != nil {
				return err
			}
			return resp.Body.Close()
		},
		want: []string{"https://other.test GET /direct user"},
	}} {
		s.requests()
		if err := tc.do(); err != nil {
			t.Errorf("%v: %v", tc.name, err)
		}
		if got := s.requests(); strings.Join(got, ", ") != strings.Join(tc.want, ", ") {
			t.Errorf("%v: requests = %q, want %q", tc.name, got, tc.want)
		}
	}
}
