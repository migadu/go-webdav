package webdav

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestClientStatWeakETag(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/xml")
		w.WriteHeader(http.StatusMultiStatus)
		w.Write([]byte(`<?xml version="1.0" encoding="utf-8"?>
<D:multistatus xmlns:D="DAV:">
  <D:response>
    <D:href>/file.txt</D:href>
    <D:propstat>
      <D:prop>
        <D:resourcetype/>
        <D:getcontentlength>3</D:getcontentlength>
        <D:getetag>W/"xyzzy"</D:getetag>
      </D:prop>
      <D:status>HTTP/1.1 200 OK</D:status>
    </D:propstat>
  </D:response>
</D:multistatus>`))
	}))
	defer srv.Close()

	c, err := NewClient(nil, srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	fi, err := c.Stat(context.Background(), "/file.txt")
	if err != nil {
		t.Fatalf("Stat() = %v", err)
	}
	if want := `W/"xyzzy"`; fi.ETag != want {
		t.Errorf("ETag = %q, want %q", fi.ETag, want)
	}
}
