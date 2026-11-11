package webdav

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestConditionalMatch(t *testing.T) {
	for _, tc := range []struct {
		header      string
		etag        string
		strong      bool
		weak        bool
		expectError bool
	}{
		// No current representation: never matches, not even "*"
		{header: `*`, etag: ""},
		{header: `"a"`, etag: ""},
		{header: `*`, etag: "a", strong: true, weak: true},
		{header: `"a"`, etag: "a", strong: true, weak: true},
		{header: `"a"`, etag: "b"},
		// Lists, with any amount of whitespace
		{header: `"x", "a", "y"`, etag: "a", strong: true, weak: true},
		{header: `"x","a"`, etag: "a", strong: true, weak: true},
		{header: ` "x" ,  "a" `, etag: "a", strong: true, weak: true},
		{header: `"x", "y"`, etag: "a"},
		{header: `"x", , "a"`, etag: "a", strong: true, weak: true},
		// A comma may appear inside an opaque-tag
		{header: `"a,b"`, etag: "a,b", strong: true, weak: true},
		{header: `"a,b"`, etag: "a"},
		// RFC 7232 section 2.3.2: weak tags only match with weak comparison
		{header: `W/"a"`, etag: `W/"a"`, weak: true},
		{header: `W/"a"`, etag: "a", weak: true},
		{header: `"a"`, etag: `W/"a"`, weak: true},
		{header: `W/"a"`, etag: `W/"b"`},
		// Out of spec unquoted tags
		{header: `a`, etag: "a", strong: true, weak: true},
		{header: `x, a`, etag: "a", strong: true, weak: true},
		// Malformed
		{header: `"a`, etag: "a", expectError: true},
		{header: `"a" "b"`, etag: "a", expectError: true},
		{header: `,`, etag: "a", expectError: true},
	} {
		val := ConditionalMatch(tc.header)
		strong, err := val.MatchETag(tc.etag)
		if tc.expectError {
			if err == nil {
				t.Errorf("%s: MatchETag(%q) = %v, want an error", tc.header, tc.etag, strong)
			}
			if weak, err := val.MatchETagWeak(tc.etag); err == nil {
				t.Errorf("%s: MatchETagWeak(%q) = %v, want an error", tc.header, tc.etag, weak)
			}
			continue
		}
		if err != nil {
			t.Errorf("%s: MatchETag(%q) = %v", tc.header, tc.etag, err)
		} else if strong != tc.strong {
			t.Errorf("%s: MatchETag(%q) = %v, want %v", tc.header, tc.etag, strong, tc.strong)
		}
		weak, err := val.MatchETagWeak(tc.etag)
		if err != nil {
			t.Errorf("%s: MatchETagWeak(%q) = %v", tc.header, tc.etag, err)
		} else if weak != tc.weak {
			t.Errorf("%s: MatchETagWeak(%q) = %v, want %v", tc.header, tc.etag, weak, tc.weak)
		}
	}
}

func TestPutConditional(t *testing.T) {
	h := Handler{FileSystem: LocalFileSystem(t.TempDir())}
	put := func(header, value string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodPut, "/file.txt", strings.NewReader("hello"))
		if header != "" {
			req.Header.Set(header, value)
		}
		w := httptest.NewRecorder()
		h.ServeHTTP(w, req)
		return w
	}

	// If-None-Match: * creates the resource only if it doesn't exist yet
	if w := put("If-None-Match", "*"); w.Code != http.StatusCreated {
		t.Fatalf("PUT with If-None-Match: * on a new file returned %v, want 201", w.Code)
	}
	if w := put("If-None-Match", "*"); w.Code != http.StatusPreconditionFailed {
		t.Fatalf("PUT with If-None-Match: * on an existing file returned %v, want 412", w.Code)
	}

	w := put("", "")
	etag := w.Header().Get("ETag")
	if etag == "" {
		t.Fatalf("PUT returned no ETag")
	}
	if w := put("If-Match", `"stale", `+etag); w.Code != http.StatusNoContent {
		t.Errorf("PUT with If-Match listing the current ETag returned %v, want 204", w.Code)
	}
	if w := put("If-Match", `"stale", "older"`); w.Code != http.StatusPreconditionFailed {
		t.Errorf("PUT with If-Match not listing the current ETag returned %v, want 412", w.Code)
	}
}
