package internal

import (
	"errors"
	"mime"
	"net/http"
	"net/http/httptest"
	"testing"
)

type recorderClient struct {
	handler http.Handler
}

func (rc recorderClient) Do(req *http.Request) (*http.Response, error) {
	w := httptest.NewRecorder()
	rc.handler.ServeHTTP(w, req)
	res := w.Result()
	res.Request = req
	return res, nil
}

func TestServeMultiStatusContentType(t *testing.T) {
	w := httptest.NewRecorder()
	if err := ServeMultiStatus(w, NewMultiStatus()); err != nil {
		t.Fatalf("ServeMultiStatus() = %v", err)
	}
	resp := w.Result()
	if resp.StatusCode != http.StatusMultiStatus {
		t.Fatalf("status = %d, want %d", resp.StatusCode, http.StatusMultiStatus)
	}
	mediaType, params, err := mime.ParseMediaType(resp.Header.Get("Content-Type"))
	if err != nil {
		t.Fatalf("ParseMediaType(%q) = %v", resp.Header.Get("Content-Type"), err)
	}
	if mediaType != "application/xml" || params["charset"] != "utf-8" {
		t.Errorf("Content-Type = %q, want application/xml; charset=\"utf-8\"", resp.Header.Get("Content-Type"))
	}
	if vals := resp.Header.Values("Content-Type"); len(vals) != 1 {
		t.Errorf("Content-Type values = %q, want a single header", vals)
	}
}

func TestServeErrorXMLContentTypeAndClientDecode(t *testing.T) {
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ServeError(w, NewConditionError(http.StatusForbidden, ValidSyncTokenName))
	})

	w := httptest.NewRecorder()
	handler.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/", nil))
	if got := w.Result().Header.Get("Content-Type"); got != `application/xml; charset="utf-8"` {
		t.Errorf("ServeError Content-Type = %q, want application/xml; charset=\"utf-8\"", got)
	}

	c, err := NewClient(recorderClient{handler: handler}, "http://example.com/")
	if err != nil {
		t.Fatal(err)
	}
	req, err := c.NewRequest(http.MethodGet, "/", nil)
	if err != nil {
		t.Fatal(err)
	}
	_, err = c.Do(req)
	if err == nil {
		t.Fatal("Do() succeeded, want 403 error")
	}

	var httpErr *HTTPError
	if !errors.As(err, &httpErr) || httpErr.Code != http.StatusForbidden {
		t.Fatalf("Do() error = %v, want 403 *HTTPError", err)
	}
	var davErr *Error
	if !errors.As(err, &davErr) {
		t.Fatalf("Do() error = %T (%v), want wrapped *Error decoded from XML response", httpErr.Err, err)
	}
	if len(davErr.Raw) != 1 {
		t.Fatalf("davErr.Raw len = %d, want 1", len(davErr.Raw))
	}
	if name, _ := davErr.Raw[0].XMLName(); name != ValidSyncTokenName {
		t.Errorf("condition = %v, want %v", name, ValidSyncTokenName)
	}
}
