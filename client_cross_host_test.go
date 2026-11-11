package webdav_test

import (
	"context"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/emersion/go-webdav"
)

// TestClientCrossHost checks that the client follows hrefs to another host
// than the endpoint's, and resolves the relative hrefs this host returns
// against its own URL.
func TestClientCrossHost(t *testing.T) {
	multiStatus := func(w http.ResponseWriter, body string) {
		w.Header().Set("Content-Type", `application/xml; charset="utf-8"`)
		w.WriteHeader(http.StatusMultiStatus)
		io.WriteString(w, `<?xml version="1.0" encoding="utf-8"?><d:multistatus xmlns:d="DAV:">`+body+`</d:multistatus>`)
	}
	const ok = `<d:status>HTTP/1.1 200 OK</d:status>`

	var copyDest string
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch key := r.Host + " " + r.Method + " " + r.URL.RequestURI(); key {
		case "dav.test PROPFIND /":
			multiStatus(w, `<d:response><d:href>/</d:href><d:propstat><d:prop>
				<d:current-user-principal><d:href>http://files.test/principal/</d:href></d:current-user-principal>
				</d:prop>`+ok+`</d:propstat></d:response>`)
		case "files.test PROPFIND /dir/":
			multiStatus(w, `<d:response><d:href>/dir/</d:href><d:propstat><d:prop>
				<d:resourcetype><d:collection/></d:resourcetype>
				</d:prop>`+ok+`</d:propstat></d:response>
				<d:response><d:href>a.txt</d:href><d:propstat><d:prop>
				<d:resourcetype/><d:getcontentlength>5</d:getcontentlength>
				</d:prop>`+ok+`</d:propstat></d:response>`)
		case "files.test GET /dir/a.txt":
			io.WriteString(w, "hello")
		case "files.test COPY /dir/a.txt":
			copyDest = r.Header.Get("Destination")
			w.WriteHeader(http.StatusCreated)
		default:
			t.Errorf("unexpected request %v", key)
			http.NotFound(w, r)
		}
	}))
	defer ts.Close()

	var dialer net.Dialer
	transport := &http.Transport{
		DialContext: func(ctx context.Context, network, _ string) (net.Conn, error) {
			return dialer.DialContext(ctx, network, ts.Listener.Addr().String())
		},
	}
	defer transport.CloseIdleConnections()

	ctx := context.Background()
	c, err := webdav.NewClient(&http.Client{Transport: transport}, "http://dav.test/")
	if err != nil {
		t.Fatal(err)
	}

	principal, err := c.FindCurrentUserPrincipal(ctx)
	if err != nil {
		t.Fatalf("FindCurrentUserPrincipal() = %v", err)
	} else if want := "http://files.test/principal/"; principal != want {
		t.Errorf("FindCurrentUserPrincipal() = %q, want %q", principal, want)
	}

	fis, err := c.ReadDir(ctx, "http://files.test/dir/", false)
	if err != nil {
		t.Fatalf("ReadDir() = %v", err)
	}
	if len(fis) != 2 || fis[0].Path != "http://files.test/dir/" || fis[1].Path != "http://files.test/dir/a.txt" {
		t.Fatalf("ReadDir() = %+v, want http://files.test/dir/ and http://files.test/dir/a.txt", fis)
	}

	rc, err := c.Open(ctx, fis[1].Path)
	if err != nil {
		t.Fatalf("Open() = %v", err)
	}
	b, err := io.ReadAll(rc)
	rc.Close()
	if err != nil || string(b) != "hello" {
		t.Errorf("Open() read %q, %v, want %q", b, err, "hello")
	}

	dest := strings.TrimSuffix(fis[1].Path, "a.txt") + "b.txt"
	if err := c.Copy(ctx, fis[1].Path, dest, nil); err != nil {
		t.Fatalf("Copy() = %v", err)
	}
	if copyDest != dest {
		t.Errorf("Copy() sent Destination %q, want %q", copyDest, dest)
	}
}
