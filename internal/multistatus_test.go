package internal

import (
	"bytes"
	"context"
	"encoding/xml"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// sampleMultiStatus returns a multistatus with responses of every kind, and
// properties in several namespaces, with attributes whose namespace prefixes
// the encoder has to make up.
func sampleMultiStatus(t *testing.T) *MultiStatus {
	t.Helper()
	zName := func(local string) xml.Name { return xml.Name{"http://ns.example.com/z/", local} }
	// Its prefix collides with the one of the namespace of zName
	otherZName := func(local string) xml.Name { return xml.Name{"http://other.example.com/z", local} }
	fragment := func(name xml.Name, attr []xml.Attr, content string) *RawXMLValue {
		t.Helper()
		raw, err := NewRawXMLFragment(name, attr, []byte(content))
		if err != nil {
			t.Fatal(err)
		}
		return raw
	}
	mustEncodeProp := func(resp *Response, code int, v interface{}) {
		t.Helper()
		if err := resp.EncodeProp(code, v); err != nil {
			t.Fatal(err)
		}
	}

	props := &Response{Hrefs: []Href{{Path: "/a/b c/"}}}
	mustEncodeProp(props, http.StatusOK, NewResourceType(CollectionName))
	mustEncodeProp(props, http.StatusOK, &DisplayName{Name: "A & B <c>"})
	mustEncodeProp(props, http.StatusOK, &GetETag{ETag: "x"})
	mustEncodeProp(props, http.StatusOK, &GetLastModified{LastModified: Time(time.Date(2026, 11, 11, 10, 0, 0, 0, time.UTC))})
	mustEncodeProp(props, http.StatusOK, fragment(zName("authors"), []xml.Attr{
		{Name: zName("kind"), Value: "list"},
		{Name: xml.Name{"http://www.w3.org/XML/1998/namespace", "lang"}, Value: "en"},
	}, `<Z:author xmlns:Z="http://ns.example.com/z/" xmlns:O="http://other.example.com/z" O:role="lead">Jim</Z:author><plain>text</plain>`))
	mustEncodeProp(props, http.StatusNotFound, NewRawXMLElement(zName("missing"), nil, nil))
	mustEncodeProp(props, http.StatusNotFound, NewRawXMLElement(xml.Name{Local: "nonamespace"}, nil, nil))

	collision := &Response{Hrefs: []Href{{Path: "/a/collision"}}}
	mustEncodeProp(collision, http.StatusOK, fragment(zName("outer"), []xml.Attr{{Name: zName("a"), Value: "1"}},
		`<O:inner xmlns:O="http://other.example.com/z" O:b="2" xmlns:Z="http://ns.example.com/z/" Z:c="3"/>`))
	mustEncodeProp(collision, http.StatusForbidden, fragment(otherZName("outer"), []xml.Attr{{Name: otherZName("a"), Value: "1"}},
		`<Z:inner xmlns:Z="http://ns.example.com/z/" Z:b="2"/>`))

	denied := NewErrorResponse("/b", &HTTPError{Code: http.StatusForbidden, Err: errors.New("denied")})
	truncated := NewErrorResponse("/c", NewConditionError(http.StatusInsufficientStorage, NumberOfMatchesWithinLimitsName))
	moved := &Response{
		Hrefs:    []Href{{Path: "/d"}},
		Status:   &Status{Code: http.StatusMovedPermanently},
		Location: &Location{Href: Href{Path: "/e"}},
	}
	failed := &Response{
		Hrefs:  []Href{{Path: "/f"}, {Scheme: "https", Host: "example.com", Path: "/g"}},
		Status: &Status{Code: http.StatusFailedDependency, Text: "Failed, as a dependency"},
	}

	ms := NewMultiStatus(*props, *collision, *denied, *truncated, *moved, *failed)
	ms.ResponseDescription = "Partial <result>"
	ms.SyncToken = "http://example.com/sync/1"
	return ms
}

// encodeMultiStatus returns the encoding of ms used before responses were
// streamed: the XML header followed by the whole multistatus element.
func encodeMultiStatus(t *testing.T, ms *MultiStatus) string {
	t.Helper()
	var buf bytes.Buffer
	buf.WriteString(xml.Header)
	if err := xml.NewEncoder(&buf).Encode(ms); err != nil {
		t.Fatal(err)
	}
	return buf.String()
}

// writeMultiStatus writes the responses of ms to mw.
func writeMultiStatus(mw *MultiStatusWriter, ms *MultiStatus) error {
	for i := range ms.Responses {
		if err := mw.WriteResponse(&ms.Responses[i]); err != nil {
			return err
		}
	}
	mw.ResponseDescription = ms.ResponseDescription
	mw.SyncToken = ms.SyncToken
	return nil
}

// Streaming responses doesn't change their encoding.
func TestMultiStatusWriterEncoding(t *testing.T) {
	for _, tc := range []struct {
		name string
		ms   *MultiStatus
	}{
		{"sample", sampleMultiStatus(t)},
		{"empty", NewMultiStatus()},
		{"sync-token only", &MultiStatus{SyncToken: "http://example.com/sync/2"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			want := encodeMultiStatus(t, tc.ms)
			check := func(t *testing.T, w *httptest.ResponseRecorder) {
				t.Helper()
				if w.Code != http.StatusMultiStatus {
					t.Errorf("status = %v, want 207", w.Code)
				}
				if got := w.Body.String(); got != want {
					t.Errorf("streamed encoding differs:\ngot:  %s\nwant: %s", got, want)
				}
			}

			t.Run("MultiStatusWriter", func(t *testing.T) {
				w := httptest.NewRecorder()
				mw := NewMultiStatusWriter(w)
				if err := writeMultiStatus(mw, tc.ms); err != nil {
					t.Fatal(err)
				}
				if err := mw.Close(); err != nil {
					t.Fatal(err)
				}
				check(t, w)
			})
			t.Run("StreamMultiStatus", func(t *testing.T) {
				w := httptest.NewRecorder()
				r := httptest.NewRequest("PROPFIND", "/", nil)
				err := StreamMultiStatus(w, r, func(mw *MultiStatusWriter) error {
					return writeMultiStatus(mw, tc.ms)
				})
				if err != nil {
					t.Fatal(err)
				}
				check(t, w)
			})
			t.Run("ServeMultiStatus", func(t *testing.T) {
				w := httptest.NewRecorder()
				if err := ServeMultiStatus(w, tc.ms); err != nil {
					t.Fatal(err)
				}
				check(t, w)
			})
		})
	}
}

// failingResponseWriter fails to write once n bytes have been written.
type failingResponseWriter struct {
	*httptest.ResponseRecorder
	n int
}

var errWrite = errors.New("connection reset")

func (w *failingResponseWriter) Write(b []byte) (int, error) {
	if len(b) > w.n {
		n, _ := w.ResponseRecorder.Write(b[:w.n])
		w.n = 0
		return n, errWrite
	}
	w.n -= len(b)
	return w.ResponseRecorder.Write(b)
}

// Once a response failed to be written, the writer doesn't write anything
// more.
func TestMultiStatusWriterError(t *testing.T) {
	w := &failingResponseWriter{ResponseRecorder: httptest.NewRecorder(), n: 100}
	mw := NewMultiStatusWriter(w)
	resp := NewOKResponse("/a")
	resp.ResponseDescription = strings.Repeat("x", 200)
	if err := mw.WriteResponse(resp); !errors.Is(err, errWrite) {
		t.Fatalf("WriteResponse() = %v, want %v", err, errWrite)
	}
	n := w.Body.Len()
	if err := mw.WriteResponse(NewOKResponse("/b")); !errors.Is(err, errWrite) {
		t.Errorf("WriteResponse() after an error = %v, want %v", err, errWrite)
	}
	if err := mw.Close(); !errors.Is(err, errWrite) {
		t.Errorf("Close() after an error = %v, want %v", err, errWrite)
	}
	if w.Body.Len() != n {
		t.Errorf("wrote %q after an error", w.Body.String()[n:])
	}
}

func TestMultiStatusWriterClosed(t *testing.T) {
	w := httptest.NewRecorder()
	mw := NewMultiStatusWriter(w)
	if err := mw.Close(); err != nil {
		t.Fatal(err)
	}
	n := w.Body.Len()
	if err := mw.WriteResponse(NewOKResponse("/a")); err == nil {
		t.Errorf("WriteResponse() after Close() succeeded")
	}
	if w.Body.Len() != n {
		t.Errorf("wrote %q after Close()", w.Body.String()[n:])
	}
}

var errStream = errors.New("backend failed")

// An error before any response has been written is returned, to be served as
// the status of the request.
func TestStreamMultiStatusErrorBeforeStart(t *testing.T) {
	w := httptest.NewRecorder()
	r := httptest.NewRequest("PROPFIND", "/", nil)
	err := StreamMultiStatus(w, r, func(mw *MultiStatusWriter) error {
		return &HTTPError{Code: http.StatusForbidden, Err: errStream}
	})
	if !errors.Is(err, errStream) {
		t.Fatalf("StreamMultiStatus() = %v, want %v", err, errStream)
	}
	ServeError(w, err)
	if w.Code != http.StatusForbidden {
		t.Errorf("status = %v, want 403", w.Code)
	}
	if strings.Contains(w.Body.String(), "multistatus") {
		t.Errorf("multistatus written along with the error: %s", w.Body.String())
	}
}

// An error after a response has been written is not returned, and leaves the
// multistatus element unterminated. Without an http.Server to abort the
// response, the handler returns normally.
func TestStreamMultiStatusErrorAfterStart(t *testing.T) {
	w := httptest.NewRecorder()
	r := httptest.NewRequest("PROPFIND", "/", nil)
	resp := NewOKResponse("/a")
	err := StreamMultiStatus(w, r, func(mw *MultiStatusWriter) error {
		if err := mw.WriteResponse(resp); err != nil {
			return err
		}
		mw.SyncToken = "http://example.com/sync/1"
		return errStream
	})
	if err != nil {
		t.Fatalf("StreamMultiStatus() = %v, want nil", err)
	}
	if w.Code != http.StatusMultiStatus {
		t.Errorf("status = %v, want 207", w.Code)
	}
	b, err := xml.Marshal(resp)
	if err != nil {
		t.Fatal(err)
	}
	if want := xml.Header + `<multistatus xmlns="DAV:">` + string(b); w.Body.String() != want {
		t.Errorf("body = %s, want %s", w.Body.String(), want)
	}
}

// Under an http.Server, an error after a response has been sent aborts the
// response: the client gets an incomplete HTTP message.
func TestStreamMultiStatusAbort(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		err := StreamMultiStatus(w, r, func(mw *MultiStatusWriter) error {
			// Large enough to get through the buffers of net/http
			resp := NewOKResponse("/a")
			resp.ResponseDescription = strings.Repeat("Streaming multistatus. ", 1500)
			if err := mw.WriteResponse(resp); err != nil {
				return err
			}
			return errStream
		})
		if err != nil {
			ServeError(w, err)
		}
	}))
	defer srv.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, "PROPFIND", srv.URL, nil)
	if err != nil {
		t.Fatal(err)
	}
	resp, err := srv.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusMultiStatus {
		t.Fatalf("status = %v, want 207", resp.Status)
	}
	b, err := io.ReadAll(resp.Body)
	if !errors.Is(err, io.ErrUnexpectedEOF) {
		t.Errorf("reading the response returned %v, want %v", err, io.ErrUnexpectedEOF)
	}
	if !bytes.Contains(b, []byte("<href>/a</href>")) {
		t.Errorf("response for /a missing: %s", b)
	}
	if bytes.Contains(b, []byte("</multistatus>")) || bytes.Contains(b, []byte(errStream.Error())) {
		t.Errorf("aborted response looks complete or holds the error: %s", b)
	}
}
