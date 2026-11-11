package internal

import (
	"bytes"
	"compress/gzip"
	"compress/zlib"
	"errors"
	"io"
	"math/rand"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
)

type closeRecorder struct {
	io.Reader
	closed bool
}

func (c *closeRecorder) Close() error {
	c.closed = true
	return nil
}

func gzipData(t *testing.T, b []byte) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := gzip.NewWriter(&buf)
	if _, err := zw.Write(b); err != nil {
		t.Fatal(err)
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func TestDecodeRequestBody(t *testing.T) {
	content := []byte("Hello, world!")
	encoded := gzipData(t, content)
	src := &closeRecorder{Reader: bytes.NewReader(encoded)}

	r := httptest.NewRequest(http.MethodPut, "/", nil)
	r.Body = src
	r.ContentLength = int64(len(encoded))
	r.Header.Set("Content-Length", strconv.Itoa(len(encoded)))
	r.Header.Set("Content-Encoding", "gzip")

	w := httptest.NewRecorder()
	r2, err := DecodeRequestBody(w, r)
	if err != nil {
		t.Fatalf("DecodeRequestBody() = %v", err)
	}

	if v := r2.Header.Get("Content-Encoding"); v != "" {
		t.Errorf("decoded request has Content-Encoding %q", v)
	}
	if v := r2.Header.Get("Content-Length"); v != "" {
		t.Errorf("decoded request has Content-Length header %q", v)
	}
	if r2.ContentLength != -1 {
		t.Errorf("decoded request ContentLength = %d, want -1", r2.ContentLength)
	}
	if r.Header.Get("Content-Encoding") != "gzip" || r.ContentLength != int64(len(encoded)) {
		t.Errorf("original request was modified")
	}

	if IsRequestBodyEmpty(r2) {
		t.Errorf("IsRequestBodyEmpty() = true for a non-empty body")
	}
	got, err := io.ReadAll(r2.Body)
	if err != nil {
		t.Fatalf("failed to read decoded body: %v", err)
	}
	if !bytes.Equal(got, content) {
		t.Errorf("decoded body = %q, want %q", got, content)
	}
	if err := r2.Body.Close(); err != nil {
		t.Errorf("Close() = %v", err)
	}
	if !src.closed {
		t.Errorf("closing the decoded body didn't close the original body")
	}
}

func TestDecodeRequestBody_noEncoding(t *testing.T) {
	r := httptest.NewRequest(http.MethodPut, "/", bytes.NewReader([]byte("plain")))
	r2, err := DecodeRequestBody(httptest.NewRecorder(), r)
	if err != nil {
		t.Fatalf("DecodeRequestBody() = %v", err)
	}
	if r2 != r {
		t.Errorf("DecodeRequestBody() returned a new request for a request without Content-Encoding")
	}
}

func TestDecodeRequestBody_identity(t *testing.T) {
	r := httptest.NewRequest(http.MethodPut, "/", bytes.NewReader([]byte("plain")))
	r.Header.Set("Content-Encoding", "identity")
	r2, err := DecodeRequestBody(httptest.NewRecorder(), r)
	if err != nil {
		t.Fatalf("DecodeRequestBody() = %v", err)
	}
	if v := r2.Header.Get("Content-Encoding"); v != "" {
		t.Errorf("decoded request has Content-Encoding %q", v)
	}
	if r2.ContentLength != 5 {
		t.Errorf("ContentLength = %d, want unchanged length 5", r2.ContentLength)
	}
	if got, err := io.ReadAll(r2.Body); err != nil || string(got) != "plain" {
		t.Errorf("body = %q, %v, want %q", got, err, "plain")
	}
}

func TestDecodeRequestBody_unsupported(t *testing.T) {
	r := httptest.NewRequest(http.MethodPut, "/", bytes.NewReader([]byte("data")))
	r.Header.Add("Content-Encoding", "gzip")
	r.Header.Add("Content-Encoding", "br")
	w := httptest.NewRecorder()
	_, err := DecodeRequestBody(w, r)

	var httpErr *HTTPError
	if !errors.As(err, &httpErr) || httpErr.Code != http.StatusUnsupportedMediaType {
		t.Fatalf("DecodeRequestBody() = %v, want HTTP 415", err)
	}
	if got := w.Header().Get("Accept-Encoding"); got != acceptedContentCodings {
		t.Errorf("Accept-Encoding = %q, want %q", got, acceptedContentCodings)
	}
}

func TestDecodeRequestBody_corrupt(t *testing.T) {
	encoded := gzipData(t, []byte("Hello, world!"))
	badChecksum := append([]byte(nil), encoded...)
	badChecksum[len(badChecksum)-8] ^= 0xff // CRC-32 of the gzip trailer
	var zbuf bytes.Buffer
	zw := zlib.NewWriter(&zbuf)
	zw.Write([]byte("Hello, world!"))
	zw.Close()
	zlibData := zbuf.Bytes()

	for _, tc := range []struct {
		name, coding string
		body         []byte
	}{
		{"not gzip", "gzip", []byte("this is not gzip data")},
		{"truncated gzip", "gzip", encoded[:len(encoded)-4]},
		{"gzip checksum", "gzip", badChecksum},
		{"not zlib", "deflate", []byte("this is not zlib data")},
		{"truncated zlib", "deflate", zlibData[:len(zlibData)-6]},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := httptest.NewRequest(http.MethodPut, "/", bytes.NewReader(tc.body))
			r.Header.Set("Content-Encoding", tc.coding)
			r2, err := DecodeRequestBody(httptest.NewRecorder(), r)
			if err != nil {
				t.Fatalf("DecodeRequestBody() = %v", err)
			}
			_, err = io.ReadAll(r2.Body)
			// Malformed data from the client is a bad request, not a server
			// error
			var httpErr *HTTPError
			if !errors.As(err, &httpErr) || httpErr.Code != http.StatusBadRequest {
				t.Errorf("reading a corrupt body = %v, want an HTTP 400 error", err)
			}
		})
	}
}

// Errors from the encoded body itself, such as the one of a
// http.MaxBytesReader, are returned as is rather than as a malformed body.
func TestDecodeRequestBody_sourceErrors(t *testing.T) {
	encoded := gzipData(t, bytes.Repeat([]byte("Hello, world! "), 1000))

	r := httptest.NewRequest(http.MethodPut, "/", bytes.NewReader(encoded))
	r.Body = http.MaxBytesReader(httptest.NewRecorder(), r.Body, int64(len(encoded)/2))
	r.Header.Set("Content-Encoding", "gzip")
	r2, err := DecodeRequestBody(httptest.NewRecorder(), r)
	if err != nil {
		t.Fatalf("DecodeRequestBody() = %v", err)
	}
	_, err = io.ReadAll(r2.Body)
	var maxBytesErr *http.MaxBytesError
	if !errors.As(err, &maxBytesErr) {
		t.Errorf("reading past the limit = %v, want a *http.MaxBytesError", err)
	}

	srcErr := HTTPErrorf(http.StatusRequestTimeout, "timeout")
	r = httptest.NewRequest(http.MethodPut, "/", io.MultiReader(bytes.NewReader(encoded[:20]), errReader{srcErr}))
	r.Header.Set("Content-Encoding", "gzip")
	r2, err = DecodeRequestBody(httptest.NewRecorder(), r)
	if err != nil {
		t.Fatalf("DecodeRequestBody() = %v", err)
	}
	if _, err = io.ReadAll(r2.Body); err != error(srcErr) {
		t.Errorf("reading a failing body = %v, want %v", err, srcErr)
	}
}

type errReader struct{ err error }

func (r errReader) Read([]byte) (int, error) { return 0, r.err }

// Each content coding adds a decoder to the chain: refuse more than a few,
// rather than letting a header listing thousands of them build as many
// decoders and overflow the stack on the first read.
func TestDecodeRequestBody_tooManyCodings(t *testing.T) {
	content := []byte("Hello, world!")
	encoded := content
	for i := 0; i < maxContentCodings; i++ {
		encoded = gzipData(t, encoded)
	}
	codings := strings.TrimSuffix(strings.Repeat("gzip, identity, ", maxContentCodings), ", ")

	r := httptest.NewRequest(http.MethodPut, "/", bytes.NewReader(encoded))
	r.Header.Set("Content-Encoding", codings)
	r2, err := DecodeRequestBody(httptest.NewRecorder(), r)
	if err != nil {
		t.Fatalf("DecodeRequestBody() with %d codings = %v", maxContentCodings, err)
	}
	if b, err := io.ReadAll(r2.Body); err != nil || !bytes.Equal(b, content) {
		t.Errorf("reading the body = %q, %v, want %q", b, err, content)
	}

	for _, n := range []int{maxContentCodings + 1, 100000} {
		r := httptest.NewRequest(http.MethodPut, "/", bytes.NewReader(gzipData(t, encoded)))
		r.Header.Set("Content-Encoding", strings.TrimSuffix(strings.Repeat("gzip,", n), ","))
		w := httptest.NewRecorder()
		_, err := DecodeRequestBody(w, r)
		var httpErr *HTTPError
		if !errors.As(err, &httpErr) || httpErr.Code != http.StatusUnsupportedMediaType {
			t.Errorf("DecodeRequestBody() with %d codings = %v, want an HTTP 415 error", n, err)
		}
	}
}

func TestDecodeRequestBody_large(t *testing.T) {
	// A large body with an ordinary compression ratio must not be mistaken for
	// a decompression bomb
	rnd := rand.New(rand.NewSource(1))
	content := make([]byte, 4<<20)
	for i := range content {
		content[i] = 'a' + byte(rnd.Intn(26))
	}

	r := httptest.NewRequest(http.MethodPut, "/", bytes.NewReader(gzipData(t, content)))
	r.Header.Set("Content-Encoding", "gzip")
	r2, err := DecodeRequestBody(httptest.NewRecorder(), r)
	if err != nil {
		t.Fatalf("DecodeRequestBody() = %v", err)
	}
	got, err := io.ReadAll(r2.Body)
	if err != nil {
		t.Fatalf("failed to read decoded body: %v", err)
	}
	if !bytes.Equal(got, content) {
		t.Errorf("decoded body doesn't match the original body")
	}
}

func TestDecodeRequestBody_bomb(t *testing.T) {
	// 16 MiB of zeros compress to about 16 KiB with gzip, and to a few hundred
	// bytes when compressed twice
	content := make([]byte, 16<<20)
	for _, coding := range []string{"gzip", "gzip, gzip"} {
		t.Run(coding, func(t *testing.T) {
			encoded := gzipData(t, content)
			if coding == "gzip, gzip" {
				encoded = gzipData(t, encoded)
			}
			r := httptest.NewRequest(http.MethodPut, "/", bytes.NewReader(encoded))
			r.Header.Set("Content-Encoding", coding)
			r2, err := DecodeRequestBody(httptest.NewRecorder(), r)
			if err != nil {
				t.Fatalf("DecodeRequestBody() = %v", err)
			}

			n, err := io.Copy(io.Discard, r2.Body)
			var httpErr *HTTPError
			if !errors.As(err, &httpErr) || httpErr.Code != http.StatusRequestEntityTooLarge {
				t.Fatalf("reading a %d bytes long body decoded to %d bytes, %v, want HTTP 413", len(encoded), n, err)
			}
			if limit := int64(maxDecodedBodyRatio * len(encoded)); n > minDecodedBodyLimit && n > limit {
				t.Errorf("decoded %d bytes before failing, want at most max(%d, %d)", n, minDecodedBodyLimit, limit)
			}
			// The error must stick
			if _, err := r2.Body.Read(make([]byte, 1)); !errors.As(err, &httpErr) {
				t.Errorf("Read() after HTTP 413 = %v, want HTTP 413", err)
			}
		})
	}
}
