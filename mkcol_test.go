package webdav_test

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/emersion/go-webdav"
)

// RFC 4918 section 9.3: "If the server receives a MKCOL request entity type
// it does not support or understand, it MUST respond with a 415 (Unsupported
// Media Type) status code." Whether there's such a body depends on the body,
// not on the Content-Type header: a body without one is refused, and a
// request without a body creates the collection even with one.
func TestHandlerMkcolBody(t *testing.T) {
	for _, tc := range []struct {
		name        string
		contentType string
		body        string
		// maxBytes, if set, limits the size of the request body
		maxBytes int64
		wantCode int
	}{
		{
			name:     "without a body",
			wantCode: http.StatusCreated,
		},
		{
			// Some HTTP clients and proxies add one
			name:        "empty body with a content type",
			contentType: "application/octet-stream",
			wantCode:    http.StatusCreated,
		},
		{
			name:        "empty body with a content type and a size limit",
			contentType: "application/xml",
			maxBytes:    64,
			wantCode:    http.StatusCreated,
		},
		{
			name:     "body without a content type",
			body:     "<mkcol/>",
			wantCode: http.StatusUnsupportedMediaType,
		},
		{
			// What litmus's basic mkcol_with_body test sends
			name:        "body with a content type",
			contentType: "xzy/invalid",
			body:        "afafafaf",
			wantCode:    http.StatusUnsupportedMediaType,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			h := &webdav.Handler{FileSystem: webdav.LocalFileSystem(dir)}

			req := httptest.NewRequest("MKCOL", "/coll/", strings.NewReader(tc.body))
			if tc.contentType != "" {
				req.Header.Set("Content-Type", tc.contentType)
			}
			w := httptest.NewRecorder()
			if tc.maxBytes > 0 {
				req.Body = http.MaxBytesReader(w, req.Body, tc.maxBytes)
			}
			h.ServeHTTP(w, req)

			if w.Code != tc.wantCode {
				t.Errorf("MKCOL returned %v, want %v: %s", w.Code, tc.wantCode, w.Body.String())
			}
			_, err := os.Stat(filepath.Join(dir, "coll"))
			if created := err == nil; created != (tc.wantCode == http.StatusCreated) {
				t.Errorf("MKCOL created the collection: %v, want %v", created, !created)
			}
		})
	}
}
