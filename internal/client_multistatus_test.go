package internal

import (
	"context"
	"encoding/xml"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"time"
)

// decodeMultiStatus decodes a multistatus as xml.Decoder.Decode does.
func TestDecodeMultiStatus(t *testing.T) {
	for _, tc := range []struct {
		name    string
		body    string
		invalid bool
	}{
		{name: "sample", body: encodeMultiStatus(t, sampleMultiStatus(t))},
		{name: "empty multistatus", body: `<multistatus xmlns="DAV:"/>`},
		{name: "prefixes, comments and unknown elements", body: `<?xml version="1.0"?>
<!-- comment -->
<D:multistatus xmlns:D="DAV:" xmlns:X="urn:x">
  <D:response><D:href>/a</D:href><D:status>HTTP/1.1 200 OK</D:status></D:response>
  <X:unknown><D:response><D:href>/nested</D:href></D:response></X:unknown>
  <!-- comment -->
  <D:response><D:href>/b</D:href><D:propstat><D:prop><X:p a="1">v</X:p></D:prop><D:status>HTTP/1.1 200 OK</D:status></D:propstat></D:response>
  <D:responsedescription>first</D:responsedescription>
  <D:responsedescription>second</D:responsedescription>
  <X:sync-token>http://example.com/sync/1</X:sync-token>
</D:multistatus>`},
		{"response in another namespace", `<multistatus xmlns="DAV:"><X:response xmlns:X="urn:x"><href>/a</href></X:response></multistatus>`, true},
		{"truncated", `<multistatus xmlns="DAV:"><response><href>/a</href></response><resp`, true},
		{"unterminated", `<multistatus xmlns="DAV:"><response><href>/a</href></response>`, true},
		{"other root", `<propfind xmlns="DAV:"><response><href>/a</href></response></propfind>`, true},
		{"root in another namespace", `<multistatus xmlns="urn:x"/>`, true},
		{"malformed response", `<multistatus xmlns="DAV:"><response><status>bogus</status></response></multistatus>`, true},
		{"empty body", ``, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var want MultiStatus
			wantErr := xml.NewDecoder(strings.NewReader(tc.body)).Decode(&want)

			var resps []Response
			got, err := decodeMultiStatus(strings.NewReader(tc.body), func(resp *Response) error {
				resps = append(resps, *resp)
				return nil
			})
			if (err != nil) != (wantErr != nil) {
				t.Fatalf("decodeMultiStatus() = %v, xml.Decoder.Decode() = %v", err, wantErr)
			} else if (err != nil) != tc.invalid {
				t.Fatalf("decodeMultiStatus() = %v, want an error: %v", err, tc.invalid)
			} else if err != nil {
				return
			}
			got.Responses = resps
			if !reflect.DeepEqual(got, &want) {
				t.Errorf("decodeMultiStatus() = %+v, want %+v", got, &want)
			}
		})
	}
}

// DoMultiStatusFunc hands each response over as soon as it's received, before
// the rest of the multistatus is.
func TestDoMultiStatusFuncStreaming(t *testing.T) {
	firstDecoded := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusMultiStatus)
		io.WriteString(w, xml.Header+`<multistatus xmlns="DAV:"><response><href>/a</href><status>HTTP/1.1 200 OK</status></response>`)
		w.(http.Flusher).Flush()
		select {
		case <-firstDecoded:
		case <-r.Context().Done():
			return
		}
		io.WriteString(w, `<response><href>/b</href><status>HTTP/1.1 200 OK</status></response><sync-token>http://example.com/sync/1</sync-token></multistatus>`)
	}))
	defer srv.Close()

	c, err := NewClient(srv.Client(), srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	req, err := c.NewRequest("PROPFIND", "/", nil)
	if err != nil {
		t.Fatal(err)
	}

	var paths []string
	ms, err := c.DoMultiStatusFunc(req.WithContext(ctx), func(resp *Response) error {
		p, err := resp.Path()
		if err != nil {
			return err
		}
		paths = append(paths, p)
		if len(paths) == 1 {
			close(firstDecoded)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("DoMultiStatusFunc() = %v", err)
	}
	if want := []string{"/a", "/b"}; !reflect.DeepEqual(paths, want) {
		t.Errorf("got responses for %v, want %v", paths, want)
	}
	if ms.SyncToken != "http://example.com/sync/1" || ms.Responses != nil {
		t.Errorf("DoMultiStatusFunc() = %+v, want only the sync token", ms)
	}
}

// An error returned by the function stops reading the response.
func TestDoMultiStatusFuncError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusMultiStatus)
		io.WriteString(w, xml.Header+`<multistatus xmlns="DAV:"><response><href>/a</href><status>HTTP/1.1 200 OK</status></response><response><href>/b</href><status>HTTP/1.1 200 OK</status></response></multistatus>`)
	}))
	defer srv.Close()

	c, err := NewClient(srv.Client(), srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	req, err := c.NewRequest("PROPFIND", "/", nil)
	if err != nil {
		t.Fatal(err)
	}

	errStop := errors.New("stop")
	calls := 0
	_, err = c.DoMultiStatusFunc(req, func(resp *Response) error {
		calls++
		return errStop
	})
	if !errors.Is(err, errStop) {
		t.Errorf("DoMultiStatusFunc() = %v, want %v", err, errStop)
	}
	if calls != 1 {
		t.Errorf("function called %d times, want once", calls)
	}
}

// PropStatErr reports the propstat elements with a status outside 2xx, other
// than 404 Not Found, as HrefErrors. One which names no property, as a server
// answering a request with an empty DAV:prop sends it, is an error too, with
// the status and description of the propstat element.
func TestResponsePropStatErr(t *testing.T) {
	for _, tc := range []struct {
		name     string
		propstat string
		wantErr  []string
	}{
		{"200 OK", `<D:propstat><D:prop><D:getetag>"1"</D:getetag></D:prop><D:status>HTTP/1.1 200 OK</D:status></D:propstat>`, nil},
		{"200 OK without properties", `<D:propstat><D:prop/><D:status>HTTP/1.1 200 OK</D:status></D:propstat>`, nil},
		{"404 Not Found", `<D:propstat><D:prop><D:getetag/></D:prop><D:status>HTTP/1.1 404 Not Found</D:status></D:propstat>`, nil},
		{"404 Not Found without properties", `<D:propstat><D:prop/><D:status>HTTP/1.1 404 Not Found</D:status></D:propstat>`, nil},
		{
			"500 Internal Server Error",
			`<D:propstat><D:prop><D:getetag/></D:prop><D:status>HTTP/1.1 500 Internal Server Error</D:status></D:propstat>`,
			[]string{"/a.ics", "DAV: getetag", "500 Internal Server Error"},
		},
		{
			"500 Internal Server Error without properties",
			`<D:propstat><D:prop/><D:status>HTTP/1.1 500 Internal Server Error</D:status><D:responsedescription>corrupt object</D:responsedescription></D:propstat>`,
			[]string{"/a.ics", "propstat without properties", "500 Internal Server Error", "corrupt object"},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var resp Response
			body := `<D:response xmlns:D="DAV:"><D:href>/a.ics</D:href>` + tc.propstat + `</D:response>`
			if err := xml.Unmarshal([]byte(body), &resp); err != nil {
				t.Fatal(err)
			}
			err := resp.PropStatErr()
			if tc.wantErr == nil {
				if err != nil {
					t.Errorf("PropStatErr() = %v, want nil", err)
				}
				return
			}
			var hrefErr *HrefError
			var httpErr *HTTPError
			if !errors.As(err, &hrefErr) || hrefErr.Href.Path != "/a.ics" || !errors.As(err, &httpErr) || httpErr.Code != http.StatusInternalServerError {
				t.Fatalf("PropStatErr() = %v, want an HrefError for /a.ics with a 500 HTTPError", err)
			}
			msg, i := err.Error(), 0
			for _, s := range tc.wantErr {
				j := strings.Index(msg[i:], s)
				if j < 0 {
					t.Fatalf("PropStatErr() = %q, want %q after offset %v", msg, s, i)
				}
				i += j + len(s)
			}
		})
	}
}
