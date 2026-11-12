package internal

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"
)

func TestMultigetPaths(t *testing.T) {
	req := httptest.NewRequest("REPORT", "/dav/cal/", nil)
	got, err := MultigetPaths(req, []Href{
		{Path: "/dav/cal/a.ics"},
		{Path: "b.ics"},
		{Path: "./sub/../c.ics"},
		{Path: "../other/d.ics"},
		{Path: "/dav/cal/../cal/e.ics"},
		{Scheme: "https", Host: "dav.example.com", Path: "/dav/cal/f.ics"},
	})
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"/dav/cal/a.ics", "/dav/cal/b.ics", "/dav/cal/c.ics", "/dav/other/d.ics", "/dav/cal/e.ics", "/dav/cal/f.ics"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("MultigetPaths() = %q, want %q", got, want)
	}

	var httpErr *HTTPError
	if _, err := MultigetPaths(req, nil); !errors.As(err, &httpErr) || httpErr.Code != http.StatusBadRequest {
		t.Errorf("MultigetPaths(no href) = %v, want 400", err)
	}
}
