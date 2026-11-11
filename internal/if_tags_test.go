package internal

import (
	"net/http"
	"testing"
)

// RFC 4918 sections 8.3 and 10.4.2: a Resource-Tag is a Simple-ref, an
// absolute URI or an absolute path with an optional query.
func TestParseConditions_resourceTags(t *testing.T) {
	for _, tag := range []string{
		"/",
		"/a",
		"/a/b/",
		"/a?q=1",
		"/a%20b",
		"http://example.com",
		"http://example.com/a/",
		"https://example.com:8443/a",
		"urn:example",
		"mailto:alice@example.com",
	} {
		s := "<" + tag + "> (<urn:t1>)"
		if l, err := ParseConditions(s); err != nil {
			t.Errorf("ParseConditions(%q) = %v", s, err)
		} else if len(l) != 1 || l[0][0].Resource != tag {
			t.Errorf("ParseConditions(%q) = %+v, want a list tagged %q", s, l, tag)
		}
	}
	for _, tag := range []string{
		"a",
		"dir/file.txt",
		"./a",
		"//example.com/a",
		"?q=1",
		"#frag",
		"/a%zz",
		"http://[::1",
	} {
		s := "<" + tag + "> (<urn:t1>)"
		if l, err := ParseConditions(s); err == nil {
			t.Errorf("ParseConditions(%q) = %+v, want an error", s, l)
		}
	}
}

// A lock refresh reads all the If header lines, as the evaluation of the If
// header field does.
func TestParseSubmittedToken_lines(t *testing.T) {
	h := http.Header{"If": {"(<urn:t1>)", " "}}
	if got, err := ParseSubmittedToken(h); err != nil || got != "urn:t1" {
		t.Errorf("ParseSubmittedToken(%q) = %q, %v, want urn:t1", h["If"], got, err)
	}
	for _, lines := range [][]string{
		{"(<urn:t1>)", "(<urn:t2>)"},
		{"(<urn:t1>)", "junk"},
		{"", "(<urn:t1>)", "(<urn:t2>)"},
	} {
		h := http.Header{"If": lines}
		if got, err := ParseSubmittedToken(h); err == nil {
			t.Errorf("ParseSubmittedToken(%q) = %q, want an error", lines, got)
		} else if code := HTTPErrorFromError(err).Code; code != http.StatusBadRequest {
			t.Errorf("ParseSubmittedToken(%q) = %v, want 400 Bad Request", lines, err)
		}
	}
	if got, err := ParseSubmittedToken(http.Header{"If": {"", " "}}); err != nil || got != "" {
		t.Errorf("ParseSubmittedToken(blank lines) = %q, %v, want no token", got, err)
	}
}
