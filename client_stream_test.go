package webdav_test

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/emersion/go-webdav"
	"github.com/emersion/go-webdav/internal"
)

// The size of the listings of the tests below: large enough for the
// multistatus to be sent in many reads.
const largeListingSize = 3000

var largeListingModTime = time.Date(2026, 11, 12, 10, 0, 0, 0, time.UTC)

// listingError describes an error joined in the error of ReadDir, RemoveAll,
// Copy or Move: the path of the HrefError, "" if it isn't one, and the code
// of the HTTPError.
type listingError struct {
	path string
	code int
}

// largeListingResponse returns the response for member i of the large listing
// of /dir/, and the FileInfo ReadDir returns for it, or the error it returns
// instead:
//   - every 500th file is reported with a 403 Forbidden status, an HrefError;
//   - every 700th file has no DAV:getcontentlength (404 Not Found propstat),
//     which ReadDir requires for a file. The error is about the property, and
//     doesn't name the file.
func largeListingResponse(i int) (body string, fi *webdav.FileInfo, lerr *listingError) {
	lastMod := largeListingModTime.Format(http.TimeFormat)
	if i == 0 {
		body = `<D:response><D:href>/dir/</D:href><D:propstat><D:prop>` +
			`<D:resourcetype><D:collection/></D:resourcetype>` +
			`<D:getlastmodified>` + lastMod + `</D:getlastmodified>` +
			`</D:prop><D:status>HTTP/1.1 200 OK</D:status></D:propstat></D:response>`
		return body, &webdav.FileInfo{Path: "/dir/", IsDir: true, ModTime: largeListingModTime}, nil
	}

	p := fmt.Sprintf("/dir/f%05d.txt", i)
	switch {
	case i%500 == 0:
		body = `<D:response><D:href>` + p + `</D:href><D:status>HTTP/1.1 403 Forbidden</D:status></D:response>`
		return body, nil, &listingError{p, http.StatusForbidden}
	case i%700 == 0:
		body = `<D:response><D:href>` + p + `</D:href>` +
			`<D:propstat><D:prop><D:resourcetype/></D:prop><D:status>HTTP/1.1 200 OK</D:status></D:propstat>` +
			`<D:propstat><D:prop><D:getcontentlength/></D:prop><D:status>HTTP/1.1 404 Not Found</D:status></D:propstat>` +
			`</D:response>`
		return body, nil, &listingError{"", http.StatusNotFound}
	}
	body = fmt.Sprintf(`<D:response><D:href>%s</D:href><D:propstat><D:prop>`+
		`<D:resourcetype/>`+
		`<D:getcontentlength>%d</D:getcontentlength>`+
		`<D:getcontenttype>text/plain</D:getcontenttype>`+
		`<D:getetag>"e%d"</D:getetag>`+
		`<D:getlastmodified>%s</D:getlastmodified>`+
		`</D:prop><D:status>HTTP/1.1 200 OK</D:status></D:propstat></D:response>`, p, i, i, lastMod)
	fi = &webdav.FileInfo{
		Path:     p,
		Size:     int64(i),
		ModTime:  largeListingModTime,
		MIMEType: "text/plain",
		ETag:     fmt.Sprintf("e%d", i),
	}
	return body, fi, nil
}

// newListingServer returns a client of a server answering every request with
// a 207 Multi-Status response made of the given responses, written one at a
// time. If truncated is set, the multistatus is left unterminated.
func newListingServer(t *testing.T, method string, responses []string, truncated bool) *webdav.Client {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != method {
			t.Errorf("request method = %q, want %q", r.Method, method)
		}
		w.Header().Set("Content-Type", `application/xml; charset="utf-8"`)
		w.WriteHeader(http.StatusMultiStatus)
		io.WriteString(w, `<?xml version="1.0" encoding="utf-8"?>`+"\n"+`<D:multistatus xmlns:D="DAV:">`)
		for _, resp := range responses {
			io.WriteString(w, resp)
		}
		if !truncated {
			io.WriteString(w, `</D:multistatus>`)
		}
	}))
	t.Cleanup(ts.Close)

	c, err := webdav.NewClient(nil, ts.URL)
	if err != nil {
		t.Fatal(err)
	}
	return c
}

// listingErrors returns the errors joined in err, in order.
func listingErrors(t *testing.T, err error) []listingError {
	t.Helper()
	joined, ok := err.(interface{ Unwrap() []error })
	if !ok {
		t.Fatalf("error %v isn't a list of errors", err)
	}
	var l []listingError
	for _, err := range joined.Unwrap() {
		var lerr listingError
		var hrefErr *internal.HrefError
		if errors.As(err, &hrefErr) {
			lerr.path = hrefErr.Href.Path
		}
		var httpErr *webdav.HTTPError
		if errors.As(err, &httpErr) {
			lerr.code = httpErr.Code
		}
		l = append(l, lerr)
	}
	return l
}

// ReadDir returns the files of a large listing in the order of the
// multistatus, along with an error for each of the responses it couldn't
// convert, as it did when it decoded the whole multistatus first.
func TestClientReadDirLarge(t *testing.T) {
	var responses []string
	var want []webdav.FileInfo
	var wantErrs []listingError
	for i := 0; i <= largeListingSize; i++ {
		body, fi, lerr := largeListingResponse(i)
		responses = append(responses, body)
		if fi != nil {
			want = append(want, *fi)
		} else {
			wantErrs = append(wantErrs, *lerr)
		}
	}
	c := newListingServer(t, "PROPFIND", responses, false)

	got, err := c.ReadDir(context.Background(), "/dir/", false)
	if err == nil {
		t.Fatal("ReadDir() returned no error")
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("ReadDir() returned %d files, want %d", len(got), len(want))
		for i := range got {
			if i >= len(want) || !reflect.DeepEqual(got[i], want[i]) {
				t.Fatalf("file %d = %+v, want %+v", i, got[i], want[min(i, len(want)-1)])
			}
		}
	}
	if errs := listingErrors(t, err); !reflect.DeepEqual(errs, wantErrs) {
		t.Errorf("ReadDir() errors = %v, want %v", errs, wantErrs)
	}
}

// An empty listing is an empty slice, not nil, as it always was.
func TestClientReadDirEmpty(t *testing.T) {
	c := newListingServer(t, "PROPFIND", nil, false)
	got, err := c.ReadDir(context.Background(), "/dir/", false)
	if err != nil || got == nil || len(got) != 0 {
		t.Errorf("ReadDir() = %#v, %v, want an empty slice", got, err)
	}
}

// A listing whose multistatus is cut short isn't returned at all, even though
// the files received before the cut could be: it could otherwise be taken
// for the whole listing.
func TestClientReadDirTruncated(t *testing.T) {
	var responses []string
	for i := 0; i < largeListingSize; i++ {
		body, _, _ := largeListingResponse(i)
		responses = append(responses, body)
	}
	for _, tc := range []struct {
		name      string
		responses []string
	}{
		{"unterminated", responses},
		{"cut in a response", append(responses[:len(responses):len(responses)], `<D:response><D:href>/dir/cut`)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c := newListingServer(t, "PROPFIND", tc.responses, true)
			got, err := c.ReadDir(context.Background(), "/dir/", false)
			if err == nil {
				t.Fatalf("ReadDir() returned %d files and no error", len(got))
			}
			if got != nil {
				t.Errorf("ReadDir() returned %d files along with %v, want none", len(got), err)
			}
			var hrefErr *internal.HrefError
			if errors.As(err, &hrefErr) {
				t.Errorf("ReadDir() = %v, want a decoding error, not a member error", err)
			}
		})
	}
}

// The members a server failed to delete, copy or move, reported in a large
// multistatus, are returned as HrefErrors in order, and members with a 2xx
// status aren't.
func TestClientWriteMultiStatusLarge(t *testing.T) {
	var responses []string
	var wantErrs []listingError
	for i := 0; i < largeListingSize; i++ {
		p := fmt.Sprintf("/container/r%05d", i)
		status := "204 No Content"
		if i%10 == 3 {
			status = "423 Locked"
			wantErrs = append(wantErrs, listingError{p, http.StatusLocked})
		}
		responses = append(responses, `<D:response><D:href>`+p+`</D:href><D:status>HTTP/1.1 `+status+`</D:status></D:response>`)
	}

	for _, op := range writeOps {
		t.Run(op.method, func(t *testing.T) {
			c := newListingServer(t, op.method, responses, false)
			err := op.do(context.Background(), c)
			if err == nil {
				t.Fatalf("%v returned no error", op.method)
			}
			if errs := listingErrors(t, err); !reflect.DeepEqual(errs, wantErrs) {
				t.Errorf("%v returned %d member errors, want %d: %v", op.method, len(errs), len(wantErrs), errs)
			}
		})
	}
}

// A multistatus cut short fails the request with a decoding error, rather
// than with the member errors received before the cut, or none.
func TestClientWriteMultiStatusTruncated(t *testing.T) {
	var responses []string
	for i := 0; i < largeListingSize; i++ {
		status := "204 No Content"
		if i == 1 {
			status = "423 Locked"
		}
		responses = append(responses, fmt.Sprintf(`<D:response><D:href>/container/r%05d</D:href><D:status>HTTP/1.1 %s</D:status></D:response>`, i, status))
	}

	for _, op := range writeOps {
		t.Run(op.method, func(t *testing.T) {
			c := newListingServer(t, op.method, responses, true)
			err := op.do(context.Background(), c)
			if err == nil {
				t.Fatalf("%v returned no error", op.method)
			}
			var hrefErr *internal.HrefError
			if errors.As(err, &hrefErr) {
				t.Errorf("%v = %v, want a decoding error, not a member error", op.method, err)
			}
			if !strings.Contains(err.Error(), "EOF") {
				t.Errorf("%v = %v, want an unexpected EOF", op.method, err)
			}
		})
	}
}
