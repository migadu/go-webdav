package webdav_test

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/emersion/go-webdav"
)

// apacheServer answers as Apache does with mod_dav, e.g. for a Hetzner
// storage box: a request for a collection without the trailing slash is
// redirected with 302 Found to the URL with it, whatever the method, and a
// GET request for a collection gets an HTML index. It records the requests it
// gets, as "METHOD URI".
type apacheServer struct {
	mu       sync.Mutex
	requests []string
	auth     []string
	putBody  string
}

func (s *apacheServer) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	b, _ := io.ReadAll(r.Body)
	s.mu.Lock()
	s.requests = append(s.requests, r.Method+" "+r.URL.RequestURI())
	s.auth = append(s.auth, r.Header.Get("Authorization"))
	s.mu.Unlock()

	switch r.URL.Path {
	case "/dir", "/dir/sub":
		w.Header().Set("Location", "http://"+r.Host+r.URL.Path+"/")
		w.Header().Set("Content-Type", "text/html; charset=iso-8859-1")
		w.WriteHeader(http.StatusFound)
		io.WriteString(w, `<html><body><p>The document has moved <a href="`+r.URL.Path+`/">here</a>.</p></body></html>`)
		return
	}

	switch r.Method + " " + r.URL.Path {
	case "GET /dir/", "GET /dir/sub/":
		w.Header().Set("Content-Type", "text/html")
		io.WriteString(w, `<html><body><h1>Index of `+r.URL.Path+`</h1></body></html>`)
	case "PROPFIND /dir/":
		if !strings.Contains(string(b), "resourcetype") || r.Header.Get("Depth") != "1" {
			http.Error(w, "bad PROPFIND", http.StatusBadRequest)
			return
		}
		w.Header().Set("Content-Type", `application/xml; charset="utf-8"`)
		w.WriteHeader(http.StatusMultiStatus)
		io.WriteString(w, `<?xml version="1.0" encoding="utf-8"?>
<D:multistatus xmlns:D="DAV:">
<D:response><D:href>/dir/</D:href><D:propstat><D:prop>
<D:resourcetype><D:collection/></D:resourcetype>
</D:prop><D:status>HTTP/1.1 200 OK</D:status></D:propstat></D:response>
<D:response><D:href>/dir/asset1</D:href><D:propstat><D:prop>
<D:resourcetype/><D:getcontentlength>5</D:getcontentlength>
</D:prop><D:status>HTTP/1.1 200 OK</D:status></D:propstat></D:response>
</D:multistatus>`)
	case "MKCOL /dir/sub/":
		w.WriteHeader(http.StatusCreated)
	case "PUT /dir/sub/":
		s.mu.Lock()
		s.putBody = string(b)
		s.mu.Unlock()
		w.WriteHeader(http.StatusCreated)
	default:
		http.NotFound(w, r)
	}
}

func (s *apacheServer) reset() {
	s.mu.Lock()
	s.requests, s.auth, s.putBody = nil, nil, ""
	s.mu.Unlock()
}

// TestClientRedirectCollection reproduces upstream issue 123: ReadDir and
// Mkdir of a collection without its trailing slash follow the 302 redirect
// with their own method, rather than with a GET request getting an HTML page.
func TestClientRedirectCollection(t *testing.T) {
	s := &apacheServer{}
	ts := httptest.NewServer(s)
	defer ts.Close()

	hc := webdav.HTTPClientWithBasicAuth(nil, "user", "pass")
	c, err := webdav.NewClient(hc, ts.URL)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()

	files, err := c.ReadDir(ctx, "/dir", false)
	if err != nil {
		t.Fatalf("ReadDir() = %v (requests: %q)", err, s.requests)
	}
	if len(files) != 2 || files[0].Path != "/dir/" || !files[0].IsDir || files[1].Path != "/dir/asset1" || files[1].Size != 5 {
		t.Errorf("ReadDir() = %+v, want /dir/ and /dir/asset1", files)
	}
	want := []string{"PROPFIND /dir", "GET /dir/", "PROPFIND /dir/"}
	if strings.Join(s.requests, ", ") != strings.Join(want, ", ") {
		t.Errorf("requests = %q, want %q", s.requests, want)
	}
	// HTTPClientWithBasicAuth adds the credentials to the request sent again
	for i, auth := range s.auth {
		if auth == "" {
			t.Errorf("request %q has no Authorization", s.requests[i])
		}
	}

	s.reset()
	if err := c.Mkdir(ctx, "/dir/sub"); err != nil {
		t.Errorf("Mkdir() = %v (requests: %q)", err, s.requests)
	} else if got := s.requests[len(s.requests)-1]; got != "MKCOL /dir/sub/" {
		t.Errorf("last request = %q, want %q", got, "MKCOL /dir/sub/")
	}
}

// TestClientRedirectCreate checks that a file written with Create, whose
// body is streamed and can't be sent again, isn't reported as written when
// the PUT request is redirected with 302 Found: http.Client follows the
// redirect with a GET request, whose 200 OK response says nothing about the
// PUT one.
func TestClientRedirectCreate(t *testing.T) {
	s := &apacheServer{}
	ts := httptest.NewServer(s)
	defer ts.Close()

	c, err := webdav.NewClient(nil, ts.URL)
	if err != nil {
		t.Fatal(err)
	}

	wc, err := c.Create(context.Background(), "/dir/sub", nil)
	if err != nil {
		t.Fatal(err)
	}
	io.WriteString(wc, "hello")
	err = wc.Close()
	var httpErr *webdav.HTTPError
	if !errors.As(err, &httpErr) || httpErr.Code != http.StatusFound {
		t.Errorf("Close() = %v, want an HTTP error 302 (requests: %q)", err, s.requests)
	}
	if s.putBody != "" {
		t.Errorf("PUT body at /dir/sub/ = %q, want no PUT request there", s.putBody)
	}
}
