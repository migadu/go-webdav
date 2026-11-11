package webdav_test

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/emersion/go-webdav"
)

func TestClientErrorCarriesStatus(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.NotFound(w, r)
	}))
	defer ts.Close()

	c, err := webdav.NewClient(nil, ts.URL)
	if err != nil {
		t.Fatal(err)
	}
	_, err = c.Open(context.Background(), "/gone.txt")
	var httpErr *webdav.HTTPError
	if !errors.As(err, &httpErr) {
		t.Fatalf("Open() = %v, want an HTTPError", err)
	}
	if httpErr.Code != http.StatusNotFound {
		t.Errorf("HTTPError.Code = %d, want %d", httpErr.Code, http.StatusNotFound)
	}
}
