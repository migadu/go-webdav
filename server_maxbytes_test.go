package webdav

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// maxBytesHandler limits the size of request bodies with http.MaxBytesReader,
// as servers are advised to.
func maxBytesHandler(h http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		r.Body = http.MaxBytesReader(w, r.Body, 1<<20)
		h.ServeHTTP(w, r)
	})
}

// do sends a request to srv and returns the response and its body.
func doMaxBytesTest(t *testing.T, srv *httptest.Server, method, target, body string, header ...string) (*http.Response, string) {
	t.Helper()
	var rd io.Reader
	if body != "" {
		rd = strings.NewReader(body)
	}
	req, err := http.NewRequest(method, srv.URL+target, rd)
	if err != nil {
		t.Fatal(err)
	}
	if body != "" {
		req.Header.Set("Content-Type", "application/xml")
	}
	for i := 0; i+1 < len(header); i += 2 {
		req.Header.Set(header[i], header[i+1])
	}
	resp, err := srv.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	b, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	return resp, string(b)
}

// Requests without a body are told apart from the others when the body is
// read through http.MaxBytesReader, which answers a read of zero bytes
// without reading anything: a LOCK request without body refreshes a lock
// (RFC 4918 section 9.10.2), and a PROPFIND request without body asks for
// all properties (section 9.1). Requests with a body are read in full.
func TestHandlerMaxBytesReader(t *testing.T) {
	dir := newLockTestDir(t)
	srv := httptest.NewServer(maxBytesHandler(&Handler{FileSystem: LocalFileSystem(dir), LockSystem: &MemLockSystem{}}))
	defer srv.Close()

	resp, body := doMaxBytesTest(t, srv, "LOCK", "/file.txt", exclusiveLockInfo)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("LOCK: status = %v, want 200\n%v", resp.StatusCode, body)
	}
	token := resp.Header.Get("Lock-Token")
	if !strings.Contains(body, "<owner") || token == "" {
		t.Fatalf("LOCK: Lock-Token = %q, response lacks the owner:\n%v", token, body)
	}

	resp, body = doMaxBytesTest(t, srv, "LOCK", "/file.txt", "", "If", "("+token+")", "Timeout", "Second-60")
	if resp.StatusCode != http.StatusOK {
		t.Errorf("LOCK refresh: status = %v, want 200\n%v", resp.StatusCode, body)
	} else if !strings.Contains(body, "Second-60") {
		t.Errorf("LOCK refresh: response lacks the new timeout:\n%v", body)
	}

	resp, body = doMaxBytesTest(t, srv, "PROPFIND", "/file.txt", "", "Depth", "0")
	if resp.StatusCode != http.StatusMultiStatus {
		t.Errorf("PROPFIND without body: status = %v, want 207\n%v", resp.StatusCode, body)
	} else if !strings.Contains(body, "getcontentlength") || !strings.Contains(body, "lockdiscovery") {
		t.Errorf("PROPFIND without body: response lacks allprop properties:\n%v", body)
	}

	resp, body = doMaxBytesTest(t, srv, "PROPFIND", "/file.txt", propFindLockDiscovery, "Depth", "0")
	if resp.StatusCode != http.StatusMultiStatus {
		t.Errorf("PROPFIND: status = %v, want 207\n%v", resp.StatusCode, body)
	} else if !strings.Contains(body, "lockdiscovery") || strings.Contains(body, "getcontentlength") {
		t.Errorf("PROPFIND: response doesn't list only the requested property:\n%v", body)
	}

	resp, body = doMaxBytesTest(t, srv, "UNLOCK", "/file.txt", "", "Lock-Token", token)
	if resp.StatusCode != http.StatusNoContent {
		t.Errorf("UNLOCK: status = %v, want 204\n%v", resp.StatusCode, body)
	}
}

// The same holds without a LockSystem, which reads the body only in
// PROPFIND.
func TestHandlerMaxBytesReaderNoLocking(t *testing.T) {
	dir := newLockTestDir(t)
	srv := httptest.NewServer(maxBytesHandler(&Handler{FileSystem: LocalFileSystem(dir)}))
	defer srv.Close()

	resp, body := doMaxBytesTest(t, srv, "PROPFIND", "/file.txt", "", "Depth", "0")
	if resp.StatusCode != http.StatusMultiStatus {
		t.Errorf("PROPFIND without body: status = %v, want 207\n%v", resp.StatusCode, body)
	} else if !strings.Contains(body, "getcontentlength") {
		t.Errorf("PROPFIND without body: response lacks allprop properties:\n%v", body)
	}
}

// A request built by hand may have no body at all.
func TestHandlerNilBody(t *testing.T) {
	dir := newLockTestDir(t)
	for _, h := range []*Handler{
		{FileSystem: LocalFileSystem(dir)},
		{FileSystem: LocalFileSystem(dir), LockSystem: &MemLockSystem{}},
	} {
		req := httptest.NewRequest("PROPFIND", "/file.txt", nil)
		req.Header.Set("Depth", "0")
		req.Body = nil
		w := httptest.NewRecorder()
		h.ServeHTTP(w, req)
		if w.Code != http.StatusMultiStatus {
			t.Errorf("PROPFIND without body (locking: %v): status = %v, want 207\n%v", h.LockSystem != nil, w.Code, w.Body.String())
		}
	}
}
