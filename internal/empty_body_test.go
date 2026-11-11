package internal

import (
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// IsRequestBodyEmpty tells empty bodies apart whatever the body reader, and
// leaves the whole body to read afterwards.
func TestIsRequestBodyEmpty(t *testing.T) {
	const xmlBody = `<?xml version="1.0"?><D:propfind xmlns:D="DAV:"><D:allprop/></D:propfind>`
	for _, tc := range []struct {
		name  string
		body  func(w http.ResponseWriter) io.ReadCloser
		empty bool
		want  string
	}{
		{"nil", func(http.ResponseWriter) io.ReadCloser { return nil }, true, ""},
		{"http.NoBody", func(http.ResponseWriter) io.ReadCloser { return http.NoBody }, true, ""},
		{"empty", func(http.ResponseWriter) io.ReadCloser { return io.NopCloser(strings.NewReader("")) }, true, ""},
		{"non-empty", func(http.ResponseWriter) io.ReadCloser { return io.NopCloser(strings.NewReader(xmlBody)) }, false, xmlBody},
		{"single byte", func(http.ResponseWriter) io.ReadCloser { return io.NopCloser(strings.NewReader("x")) }, false, "x"},
		// http.MaxBytesReader answers a read of zero bytes without reading
		// anything
		{"http.MaxBytesReader, empty", func(w http.ResponseWriter) io.ReadCloser {
			return http.MaxBytesReader(w, http.NoBody, 1<<20)
		}, true, ""},
		{"http.MaxBytesReader, non-empty", func(w http.ResponseWriter) io.ReadCloser {
			return http.MaxBytesReader(w, io.NopCloser(strings.NewReader(xmlBody)), 1<<20)
		}, false, xmlBody},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := httptest.NewRequest("PROPFIND", "/", nil)
			r.Body = tc.body(httptest.NewRecorder())
			if empty := IsRequestBodyEmpty(r); empty != tc.empty {
				t.Errorf("IsRequestBodyEmpty() = %v, want %v", empty, tc.empty)
			}
			// Asking again doesn't change the answer, nor the body
			if empty := IsRequestBodyEmpty(r); empty != tc.empty {
				t.Errorf("IsRequestBodyEmpty() a second time = %v, want %v", empty, tc.empty)
			}
			if r.Body == nil {
				return
			}
			b, err := io.ReadAll(r.Body)
			if err != nil {
				t.Fatalf("reading the body afterwards = %v", err)
			}
			if string(b) != tc.want {
				t.Errorf("body read afterwards = %q, want %q", b, tc.want)
			}
		})
	}
}

// A body which fails to read isn't empty, and the error is returned to the
// reader of the body.
func TestIsRequestBodyEmpty_errors(t *testing.T) {
	w := httptest.NewRecorder()
	r := httptest.NewRequest("PROPFIND", "/", nil)
	r.Body = http.MaxBytesReader(w, io.NopCloser(strings.NewReader("<propfind/>")), 0)
	if IsRequestBodyEmpty(r) {
		t.Errorf("IsRequestBodyEmpty() = true for a body over the limit, want false")
	}
	_, err := io.ReadAll(r.Body)
	var maxBytesErr *http.MaxBytesError
	if !errors.As(err, &maxBytesErr) {
		t.Errorf("reading a body over the limit = %v, want a *http.MaxBytesError", err)
	}

	srcErr := HTTPErrorf(http.StatusRequestTimeout, "timeout")
	r = httptest.NewRequest("PROPFIND", "/", errReader{srcErr})
	if IsRequestBodyEmpty(r) {
		t.Errorf("IsRequestBodyEmpty() = true for a failing body, want false")
	}
	if _, err := io.ReadAll(r.Body); err != error(srcErr) {
		t.Errorf("reading a failing body = %v, want %v", err, srcErr)
	}
}

// Closing the body after IsRequestBodyEmpty closes the original body.
func TestIsRequestBodyEmpty_close(t *testing.T) {
	body := &closeRecorder{Reader: strings.NewReader("x")}
	r := httptest.NewRequest("PROPFIND", "/", nil)
	r.Body = body
	IsRequestBodyEmpty(r)
	if err := r.Body.Close(); err != nil {
		t.Fatalf("Close() = %v", err)
	}
	if !body.closed {
		t.Errorf("closing the body didn't close the original body")
	}
}
