package internal

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"testing"
)

// seenRequest is a request received by a redirectServer.
type seenRequest struct {
	method, uri, body string
	header            http.Header
}

// redirectServer answers requests by host name, method and request URI from
// routes, falling back to the method and request URI, then to the request URI
// alone, and records the requests it gets.
type redirectServer struct {
	t *testing.T

	mu     sync.Mutex
	routes map[string]http.HandlerFunc // e.g. "dav.test PROPFIND /a", "PROPFIND /a", "/a"
	seen   []seenRequest
}

func (s *redirectServer) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	b, err := io.ReadAll(r.Body)
	if err != nil {
		s.t.Errorf("failed to read the body of %v %v: %v", r.Method, r.URL, err)
	}
	host, _, _ := strings.Cut(r.Host, ":")

	s.mu.Lock()
	s.seen = append(s.seen, seenRequest{r.Method, r.URL.RequestURI(), string(b), r.Header.Clone()})
	var f http.HandlerFunc
	for _, key := range []string{
		host + " " + r.Method + " " + r.URL.RequestURI(),
		r.Method + " " + r.URL.RequestURI(),
		r.URL.RequestURI(),
	} {
		var ok bool
		if f, ok = s.routes[key]; ok {
			break
		}
	}
	s.mu.Unlock()

	if f == nil {
		f = http.NotFound
	}
	f(w, r)
}

func (s *redirectServer) route(key string, f http.HandlerFunc) {
	s.mu.Lock()
	s.routes[key] = f
	s.mu.Unlock()
}

// requests returns the requests the server got, as "METHOD URI".
func (s *redirectServer) requests() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	l := make([]string, len(s.seen))
	for i, r := range s.seen {
		l[i] = r.method + " " + r.uri
	}
	return l
}

// last returns the last request the server got.
func (s *redirectServer) last() seenRequest {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.seen) == 0 {
		s.t.Fatal("the server got no request")
	}
	return s.seen[len(s.seen)-1]
}

func (s *redirectServer) reset() {
	s.mu.Lock()
	s.seen = nil
	s.mu.Unlock()
}

// newRedirectServer starts s, and returns an HTTP client reaching it by any
// host name: with https on port 443, and with plain HTTP on any other port.
func newRedirectServer(t *testing.T, s *redirectServer) *http.Client {
	s.t = t
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

func redirectTo(code int, location string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Location", location)
		w.WriteHeader(code)
	}
}

// htmlPage answers GET requests as Apache answers a GET request for a
// collection, and fails the others.
func htmlPage(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "unexpected method", http.StatusMethodNotAllowed)
		return
	}
	w.Header().Set("Content-Type", "text/html")
	io.WriteString(w, "<html><body>Index of /new/</body></html>")
}

// davResource answers requests with any method but GET with 200 OK, and GET
// ones with htmlPage.
func davResource(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodGet {
		htmlPage(w, r)
		return
	}
	w.Header().Set("Content-Type", "text/plain")
	io.WriteString(w, r.Method+" done")
}

// TestClientRedirectKeepsMethod checks that the client follows a 301 or 302
// redirect with the method, header and body of the request, rather than with
// a GET request: RFC 9110 sections 15.4.2 and 15.4.3 only allow changing POST
// to GET. Apache answers e.g. a PROPFIND request for a collection without the
// trailing slash this way (upstream issue 123).
func TestClientRedirectKeepsMethod(t *testing.T) {
	s := &redirectServer{routes: map[string]http.HandlerFunc{
		"/new": davResource,
	}}
	hc := newRedirectServer(t, s)
	c, err := NewClient(hc, "http://dav.test/")
	if err != nil {
		t.Fatal(err)
	}

	for _, code := range []int{http.StatusMovedPermanently, http.StatusFound} {
		for _, location := range []string{"/new", "http://dav.test/new"} {
			s.route("/old", redirectTo(code, location))
			for _, tc := range []struct {
				method, body string
			}{
				{"PROPFIND", `<propfind xmlns="DAV:"><allprop/></propfind>`},
				{"REPORT", `<sync-collection xmlns="DAV:"/>`},
				{"PROPPATCH", `<propertyupdate xmlns="DAV:"/>`},
				{"PUT", "BEGIN:VCALENDAR\r\nEND:VCALENDAR\r\n"},
				{"MKCOL", ""},
				{"DELETE", ""},
				{"COPY", ""},
				{"MOVE", ""},
				{"LOCK", `<lockinfo xmlns="DAV:"/>`},
				{"UNLOCK", ""},
				{"OPTIONS", ""},
			} {
				name := fmt.Sprintf("%v %v to %v", tc.method, code, location)
				s.reset()

				var body io.Reader
				if tc.body != "" {
					body = strings.NewReader(tc.body)
				}
				req, err := c.NewRequest(tc.method, "/old", body)
				if err != nil {
					t.Fatal(err)
				}
				req.Header.Set("Depth", "1")
				req.Header.Set("Destination", "http://dav.test/dest")
				req.Header.Set("Lock-Token", "<urn:uuid:1>")
				if tc.body != "" {
					req.Header.Set("Content-Type", "text/xml")
				}

				resp, err := c.Do(req)
				if err != nil {
					t.Errorf("%v: Do() = %v", name, err)
					continue
				}
				b, _ := io.ReadAll(resp.Body)
				resp.Body.Close()
				if got, want := string(b), tc.method+" done"; got != want {
					t.Errorf("%v: response body = %q, want %q", name, got, want)
				}
				if got := resp.Request.Method + " " + resp.Request.URL.Path; got != tc.method+" /new" {
					t.Errorf("%v: final request = %q, want %q", name, got, tc.method+" /new")
				}

				got := s.last()
				if got.method != tc.method || got.uri != "/new" {
					t.Errorf("%v: last request = %v %v, want %v /new (requests: %q)", name, got.method, got.uri, tc.method, s.requests())
					continue
				}
				if got.body != tc.body {
					t.Errorf("%v: request body at /new = %q, want %q", name, got.body, tc.body)
				}
				for _, k := range []string{"Depth", "Destination", "Lock-Token"} {
					if v := got.header.Get(k); v != req.Header.Get(k) {
						t.Errorf("%v: %v at /new = %q, want %q", name, k, v, req.Header.Get(k))
					}
				}
				if tc.body != "" && got.header.Get("Content-Type") != "text/xml" {
					t.Errorf("%v: Content-Type at /new = %q, want %q", name, got.header.Get("Content-Type"), "text/xml")
				}
			}
		}
	}
}

// TestClientRedirectChangesMethod checks that the redirects which do change
// the method still do: a 303 See Other one is followed with a GET request
// (RFC 9110 section 15.4.4), and so is a 301 or 302 one of a POST request.
func TestClientRedirectChangesMethod(t *testing.T) {
	s := &redirectServer{routes: map[string]http.HandlerFunc{
		"/new": davResource,
	}}
	hc := newRedirectServer(t, s)
	c, err := NewClient(hc, "http://dav.test/")
	if err != nil {
		t.Fatal(err)
	}

	for _, tc := range []struct {
		method string
		code   int
	}{
		{"PROPFIND", http.StatusSeeOther},
		{"PUT", http.StatusSeeOther},
		{"REPORT", http.StatusSeeOther},
		{"POST", http.StatusMovedPermanently},
		{"POST", http.StatusFound},
		{"POST", http.StatusSeeOther},
	} {
		name := fmt.Sprintf("%v %v", tc.method, tc.code)
		s.reset()
		s.route("/old", redirectTo(tc.code, "/new"))

		req, err := c.NewRequest(tc.method, "/old", strings.NewReader("body"))
		if err != nil {
			t.Fatal(err)
		}
		resp, err := c.Do(req)
		if err != nil {
			t.Errorf("%v: Do() = %v", name, err)
			continue
		}
		resp.Body.Close()
		want := []string{tc.method + " /old", "GET /new"}
		if got := s.requests(); fmt.Sprint(got) != fmt.Sprint(want) {
			t.Errorf("%v: requests = %q, want %q", name, got, want)
		}
		if got := s.last(); got.body != "" {
			t.Errorf("%v: GET request body = %q, want none", name, got.body)
		}
	}
}

// TestClientRedirectTemporary checks that 307 and 308 redirects keep being
// followed with the method and body of the request, as http.Client does.
func TestClientRedirectTemporary(t *testing.T) {
	s := &redirectServer{routes: map[string]http.HandlerFunc{
		"/new": davResource,
	}}
	hc := newRedirectServer(t, s)
	c, err := NewClient(hc, "http://dav.test/")
	if err != nil {
		t.Fatal(err)
	}

	for _, code := range []int{http.StatusTemporaryRedirect, http.StatusPermanentRedirect} {
		for _, method := range []string{"PROPFIND", "REPORT", "PUT"} {
			name := fmt.Sprintf("%v %v", method, code)
			s.reset()
			s.route("/old", redirectTo(code, "/new"))

			req, err := c.NewRequest(method, "/old", strings.NewReader("body"))
			if err != nil {
				t.Fatal(err)
			}
			resp, err := c.Do(req)
			if err != nil {
				t.Errorf("%v: Do() = %v", name, err)
				continue
			}
			resp.Body.Close()
			want := []string{method + " /old", method + " /new"}
			if got := s.requests(); fmt.Sprint(got) != fmt.Sprint(want) {
				t.Errorf("%v: requests = %q, want %q", name, got, want)
			}
			if got := s.last(); got.body != "body" {
				t.Errorf("%v: request body at /new = %q, want %q", name, got.body, "body")
			}
		}
	}
}

// TestClientRedirectChain checks that a request is sent again to the target
// of the redirect which changed its method, rather than to the end of the
// redirects the GET request was given, and that the redirects followed
// before keep working.
func TestClientRedirectChain(t *testing.T) {
	s := &redirectServer{routes: map[string]http.HandlerFunc{
		"PROPFIND /a":   redirectTo(http.StatusTemporaryRedirect, "/b"),
		"PROPFIND /b":   redirectTo(http.StatusFound, "/c/"),
		"/c/":           davResource,
		"GET /c/":       redirectTo(http.StatusFound, "/c/index.html"),
		"/c/index.html": htmlPage,
	}}
	hc := newRedirectServer(t, s)
	c, err := NewClient(hc, "http://dav.test/")
	if err != nil {
		t.Fatal(err)
	}

	req, err := c.NewRequest("PROPFIND", "/a", strings.NewReader("body"))
	if err != nil {
		t.Fatal(err)
	}
	resp, err := c.Do(req)
	if err != nil {
		t.Fatalf("Do() = %v", err)
	}
	resp.Body.Close()
	if got := resp.Request.Method + " " + resp.Request.URL.Path; got != "PROPFIND /c/" {
		t.Errorf("final request = %q, want %q", got, "PROPFIND /c/")
	}
	want := []string{"PROPFIND /a", "PROPFIND /b", "GET /c/", "GET /c/index.html", "PROPFIND /c/"}
	if got := s.requests(); fmt.Sprint(got) != fmt.Sprint(want) {
		t.Errorf("requests = %q, want %q", got, want)
	}
	if got := s.last(); got.body != "body" {
		t.Errorf("request body at /c/ = %q, want %q", got.body, "body")
	}
}

// TestClientRedirectLoop checks that a request redirected back and forth
// stops after 10 redirects, as with http.Client.
func TestClientRedirectLoop(t *testing.T) {
	s := &redirectServer{routes: map[string]http.HandlerFunc{
		"PROPFIND /a": redirectTo(http.StatusFound, "/b"),
		"PROPFIND /b": redirectTo(http.StatusMovedPermanently, "/a"),
		"GET /a":      htmlPage,
		"GET /b":      htmlPage,
	}}
	hc := newRedirectServer(t, s)
	c, err := NewClient(hc, "http://dav.test/")
	if err != nil {
		t.Fatal(err)
	}

	req, err := c.NewRequest("PROPFIND", "/a", nil)
	if err != nil {
		t.Fatal(err)
	}
	resp, err := c.Do(req)
	if err == nil {
		resp.Body.Close()
		t.Fatalf("Do() = %v %v, want an error", resp.Request.Method, resp.Status)
	} else if !strings.Contains(err.Error(), "stopped after 10 redirects") {
		t.Errorf("Do() = %v, want an error saying it stopped after 10 redirects", err)
	}

	var propfinds int
	for _, r := range s.requests() {
		if strings.HasPrefix(r, "PROPFIND ") {
			propfinds++
		}
	}
	// The request and the 10 redirects followed
	if propfinds != 11 {
		t.Errorf("got %v PROPFIND requests, want 11: %q", propfinds, s.requests())
	}
}

// TestClientRedirectBodyNotReplayable checks that a request whose body can't
// be sent again fails when it's redirected with a 301 or 302 status, rather
// than returning the response to the GET request http.Client follows the
// redirect with. That's what happens with a 307 or 308 status, with which
// http.Client returns the redirect.
func TestClientRedirectBodyNotReplayable(t *testing.T) {
	s := &redirectServer{routes: map[string]http.HandlerFunc{
		"/new": davResource,
	}}
	hc := newRedirectServer(t, s)
	c, err := NewClient(hc, "http://dav.test/")
	if err != nil {
		t.Fatal(err)
	}

	for _, code := range []int{http.StatusMovedPermanently, http.StatusFound, http.StatusTemporaryRedirect} {
		s.reset()
		s.route("/old", redirectTo(code, "/new"))

		// A body http.NewRequest doesn't know how to send again
		body := struct{ io.Reader }{strings.NewReader("data")}
		req, err := c.NewRequest(http.MethodPut, "/old", body)
		if err != nil {
			t.Fatal(err)
		}
		resp, err := c.Do(req)
		if err == nil {
			resp.Body.Close()
			t.Errorf("%v: Do() = %v %v, want an error", code, resp.Request.Method, resp.Status)
			continue
		}
		var httpErr *HTTPError
		if !errors.As(err, &httpErr) || httpErr.Code != code {
			t.Errorf("%v: Do() = %v, want an HTTP error %v", code, err, code)
		}
		for _, r := range s.requests() {
			if r == "PUT /new" {
				t.Errorf("%v: requests = %q, want no PUT at /new", code, s.requests())
			}
		}
	}
}

// TestClientRedirectCredentials checks that the Authorization header is only
// sent to the target of a 301 or 302 redirect on the origin of the request,
// as the default client sends it to the target of a 307 one: not to another
// port or scheme of the host, a subdomain or another domain, nor after a
// redirect to another origin. The sensitive header fields bound to a domain,
// such as Cookie, are sent as http.Client sends them: to the same domain or a
// subdomain, and not to another domain, nor after a redirect to another
// domain. Credentials from the request URL are kept for a relative redirect.
func TestClientRedirectCredentials(t *testing.T) {
	s := &redirectServer{routes: map[string]http.HandlerFunc{
		"/new":                      davResource,
		"other.test PROPFIND /back": redirectTo(http.StatusTemporaryRedirect, "http://dav.test/new"),
		"/back-from-port":           redirectTo(http.StatusTemporaryRedirect, "http://dav.test/new"),
	}}
	hc := newRedirectServer(t, s)
	hc.CheckRedirect = checkRedirect

	for _, tc := range []struct {
		name, endpoint, location string
		wantAuth, wantCookie     bool
	}{
		{"same host", "http://dav.test/", "/new", true, true},
		{"same host, absolute", "http://dav.test/", "http://dav.test/new", true, true},
		{"same host, default port", "http://dav.test/", "http://dav.test:80/new", true, true},
		{"other port", "http://dav.test/", "http://dav.test:8080/new", false, true},
		{"other scheme", "http://dav.test/", "https://dav.test/new", false, true},
		{"back from another port", "http://dav.test/", "http://dav.test:8080/back-from-port", false, true},
		{"subdomain", "http://dav.test/", "http://files.dav.test/new", false, true},
		{"parent domain", "http://files.dav.test/", "http://dav.test/new", false, false},
		{"other domain", "http://dav.test/", "http://other.test/new", false, false},
		{"back from another domain", "http://dav.test/", "http://other.test/back", false, false},
		{"URL credentials, relative", "http://user:pass@dav.test/", "/new", true, true},
		{"URL credentials, other port", "http://user:pass@dav.test/", "http://dav.test:8080/new", false, true},
		{"URL credentials, other domain", "http://user:pass@dav.test/", "http://other.test/new", false, false},
	} {
		c, err := NewClient(hc, tc.endpoint)
		if err != nil {
			t.Fatal(err)
		}

		// What http.Client does with a 307 redirect, which it follows with
		// the method of the request
		var want307 string
		for _, code := range []int{http.StatusTemporaryRedirect, http.StatusFound, http.StatusMovedPermanently} {
			name := fmt.Sprintf("%v, %v", tc.name, code)
			s.reset()
			s.route("/old", redirectTo(code, tc.location))

			req, err := c.NewRequest("PROPFIND", "/old", strings.NewReader("body"))
			if err != nil {
				t.Fatal(err)
			}
			if c.endpoint.User == nil {
				req.Header.Set("Authorization", "Bearer secret")
			}
			req.Header.Set("Cookie", "session=secret")
			resp, err := c.Do(req)
			if err != nil {
				t.Errorf("%v: Do() = %v", name, err)
				continue
			}
			resp.Body.Close()

			got := s.last()
			if got.method != "PROPFIND" || got.uri != "/new" {
				t.Errorf("%v: last request = %v %v, want PROPFIND /new (requests: %q)", name, got.method, got.uri, s.requests())
				continue
			}
			auth := got.header.Get("Authorization")
			if (auth != "") != tc.wantAuth {
				t.Errorf("%v: Authorization at the target = %q, want it sent: %v", name, auth, tc.wantAuth)
			}
			if cookie := got.header.Get("Cookie"); (cookie != "") != tc.wantCookie {
				t.Errorf("%v: Cookie at the target = %q, want it sent: %v", name, cookie, tc.wantCookie)
			}
			if code == http.StatusTemporaryRedirect {
				want307 = auth
			} else if auth != want307 {
				t.Errorf("%v: Authorization at the target = %q, want %q as with a 307 redirect", name, auth, want307)
			}
		}
	}
}

// basicAuthClient adds basic authentication to every request it's given, in
// place, as an HTTP client adding credentials to its requests may do.
type basicAuthClient struct {
	c HTTPClient
}

func (c basicAuthClient) Do(req *http.Request) (*http.Response, error) {
	req.SetBasicAuth("user", "secret")
	return c.c.Do(req)
}

// TestClientRedirectDowngrade checks that a request for an https URL isn't
// sent again to an http URL to follow a 301 or 302 redirect, as a reverse
// proxy unaware of TLS may answer: the body, and the credentials of an HTTP
// client adding them to every request, would go in cleartext. Do returns an
// HTTPError with the redirect status instead. Redirects which keep or upgrade
// the scheme are still followed.
func TestClientRedirectDowngrade(t *testing.T) {
	s := &redirectServer{routes: map[string]http.HandlerFunc{
		"/new": davResource,
	}}
	hc := basicAuthClient{newRedirectServer(t, s)}

	for _, tc := range []struct {
		name, endpoint, location string
		wantResent               bool
	}{
		{"https to http", "https://dav.test/", "http://dav.test/new", false},
		{"https to http, other port", "https://dav.test/", "http://dav.test:8080/new", false},
		{"https to http, subdomain", "https://dav.test/", "http://files.dav.test/new", false},
		{"https to https", "https://dav.test/", "/new", true},
		{"http to https", "http://dav.test/", "https://dav.test/new", true},
	} {
		c, err := NewClient(hc, tc.endpoint)
		if err != nil {
			t.Fatal(err)
		}

		for _, code := range []int{http.StatusMovedPermanently, http.StatusFound} {
			name := fmt.Sprintf("%v, %v", tc.name, code)
			s.reset()
			s.route("/old", redirectTo(code, tc.location))

			req, err := c.NewRequest("PROPFIND", "/old", strings.NewReader("body"))
			if err != nil {
				t.Fatal(err)
			}
			req.Header.Set("Cookie", "session=secret")
			resp, err := c.Do(req)
			resent := slices.Contains(s.requests(), "PROPFIND /new")

			if tc.wantResent {
				if err != nil {
					t.Errorf("%v: Do() = %v", name, err)
					continue
				}
				resp.Body.Close()
				if !resent {
					t.Errorf("%v: requests = %q, want PROPFIND /new", name, s.requests())
				}
				continue
			}

			if err == nil {
				resp.Body.Close()
				got := s.last()
				t.Errorf("%v: Do() = %v %v, want an HTTP error %v (sent %v %v over plain HTTP with Authorization %q and Cookie %q)", name, resp.Request.Method, resp.Status, code, got.method, got.uri, got.header.Get("Authorization"), got.header.Get("Cookie"))
				continue
			}
			var httpErr *HTTPError
			if !errors.As(err, &httpErr) || httpErr.Code != code {
				t.Errorf("%v: Do() = %v, want an HTTP error %v", name, err, code)
			}
			if resent {
				t.Errorf("%v: requests = %q, want no PROPFIND at /new", name, s.requests())
			}
		}
	}
}

// TestClientRedirectCount checks that the redirects the HTTP client follows
// itself for a request Do sent again, such as 307 ones, count towards the 10
// redirects a request follows at most, with those before.
func TestClientRedirectCount(t *testing.T) {
	repeat := func(code, n int) []int {
		return slices.Repeat([]int{code}, n)
	}
	for _, tc := range []struct {
		name    string
		before  int   // 307 redirects before the 302 one
		after   []int // redirects of the request sent again
		wantErr bool
	}{
		{"10 redirects", 4, repeat(http.StatusTemporaryRedirect, 5), false},
		{"11 redirects", 4, repeat(http.StatusTemporaryRedirect, 6), true},
		{"13 redirects", 6, repeat(http.StatusTemporaryRedirect, 6), true},
		{"11 redirects, 308", 1, repeat(http.StatusPermanentRedirect, 9), true},
		{"10 redirects, then 303", 4, append(repeat(http.StatusTemporaryRedirect, 4), http.StatusSeeOther), false},
		{"11 redirects, then 303", 4, append(repeat(http.StatusTemporaryRedirect, 5), http.StatusSeeOther), true},
	} {
		s := &redirectServer{routes: map[string]http.HandlerFunc{}}
		for i := range tc.before {
			s.route(fmt.Sprintf("PROPFIND /a%v", i), redirectTo(http.StatusTemporaryRedirect, fmt.Sprintf("/a%v", i+1)))
		}
		s.route(fmt.Sprintf("PROPFIND /a%v", tc.before), redirectTo(http.StatusFound, "/b0"))
		s.route("GET /b0", htmlPage)
		for i, code := range tc.after {
			s.route(fmt.Sprintf("PROPFIND /b%v", i), redirectTo(code, fmt.Sprintf("/b%v", i+1)))
		}
		s.route(fmt.Sprintf("/b%v", len(tc.after)), davResource)

		c, err := NewClient(newRedirectServer(t, s), "http://dav.test/")
		if err != nil {
			t.Fatal(err)
		}
		req, err := c.NewRequest("PROPFIND", "/a0", strings.NewReader("body"))
		if err != nil {
			t.Fatal(err)
		}
		resp, err := c.Do(req)
		if err == nil {
			resp.Body.Close()
		}
		if !tc.wantErr {
			if err != nil {
				t.Errorf("%v: Do() = %v", tc.name, err)
			}
			continue
		}
		// Not the error of the HTTP client, which stops after 10 requests
		const want = "webdav: PROPFIND http://dav.test/a0: stopped after 10 redirects"
		if err == nil {
			t.Errorf("%v: Do() = %v %v %v, want an error (requests: %q)", tc.name, resp.Request.Method, resp.Request.URL.Path, resp.Status, s.requests())
		} else if err.Error() != want {
			t.Errorf("%v: Do() = %v, want %v", tc.name, err, want)
		}
	}
}

// credentialsClient records whether CredentialsAllowed holds for each request
// it's given, and answers it with 200 OK.
type credentialsClient struct {
	allowed []bool
}

func (c *credentialsClient) Do(req *http.Request) (*http.Response, error) {
	c.allowed = append(c.allowed, CredentialsAllowed(req))
	return &http.Response{StatusCode: http.StatusOK, Body: http.NoBody, Request: req}, nil
}

// TestCredentialsAllowed checks that the requests a Client sends may get the
// credentials of its endpoint if they have the same origin: the same scheme,
// host name and port, the default port of the scheme being implied.
func TestCredentialsAllowed(t *testing.T) {
	for _, tc := range []struct {
		endpoint, target string
		want             bool
	}{
		{"https://dav.test/dav/", "a.txt", true},
		{"https://dav.test/dav/", "/other/", true},
		{"https://dav.test/dav/", "https://dav.test/other/", true},
		{"https://dav.test/dav/", "https://DAV.test:443/other/", true},
		{"https://dav.test:443/dav/", "https://dav.test/other/", true},
		{"http://dav.test:8080/", "http://dav.test:8080/a", true},
		{"https://[::1]/", "https://[::1]:443/a", true},
		{"https://dav.test/dav/", "http://dav.test/other/", false},
		{"https://dav.test/dav/", "http://dav.test:443/other/", false},
		{"http://dav.test/", "https://dav.test/a", false},
		{"https://dav.test/dav/", "https://dav.test:8443/other/", false},
		{"https://dav.test:8443/dav/", "https://dav.test/other/", false},
		{"https://dav.test/dav/", "https://files.test/other/", false},
		{"https://dav.test/dav/", "https://files.dav.test/other/", false},
	} {
		hc := &credentialsClient{}
		c, err := NewClient(hc, tc.endpoint)
		if err != nil {
			t.Fatal(err)
		}
		// Do sends a GET request as is, and a PROPFIND one so that it can
		// follow a redirect with its method
		for _, method := range []string{http.MethodGet, "PROPFIND"} {
			req, err := c.NewRequest(method, tc.target, nil)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := c.Do(req); err != nil {
				t.Errorf("%v %v with endpoint %v: Do() = %v", method, tc.target, tc.endpoint, err)
			}
		}
		for _, got := range hc.allowed {
			if got != tc.want {
				t.Errorf("%v with endpoint %v: CredentialsAllowed() = %v, want %v", tc.target, tc.endpoint, got, tc.want)
			}
		}
	}

	// A request not sent by a Client is the caller's own
	req, err := http.NewRequest(http.MethodGet, "https://other.test/", nil)
	if err != nil {
		t.Fatal(err)
	}
	if !CredentialsAllowed(req) {
		t.Errorf("CredentialsAllowed() = false for a request not sent by a Client, want true")
	}
}
