package internal

import "testing"

func TestSamePath(t *testing.T) {
	for _, tc := range []struct {
		a, b string
		want bool
	}{
		{"/", "/", true},
		{"/c", "/c", true},
		{"/c/", "/c/", true},
		// Upstream issue 41: a collection with and without a trailing slash
		{"/c", "/c/", true},
		{"/a/b", "/a/b/", true},
		{"https://example.com/c", "https://example.com/c/", true},
		// Distinct resources
		{"/c//", "/c/", false},
		{"/c//", "/c", false},
		{"//", "/", false},
		{"/a//b", "/a/b", false},
		{"/c", "/cd", false},
		{"/c/", "/cd", false},
		{"/c", "/c/d", false},
		{"/c/", "/c/d/", false},
		{"/c", "/C", false},
		{"/c", "/c.ics", false},
		{"https://example.com/c/", "https://example.org/c/", false},
		{"https://example.com/c/", "/c/", false},
		// An empty path isn't the root, although both trimmed of a trailing
		// slash are empty
		{"", "", true},
		{"", "/", false},
		{"", "//", false},
	} {
		if got := SamePath(tc.a, tc.b); got != tc.want {
			t.Errorf("SamePath(%q, %q) = %v, want %v", tc.a, tc.b, got, tc.want)
		}
		if got := SamePath(tc.b, tc.a); got != tc.want {
			t.Errorf("SamePath(%q, %q) = %v, want %v", tc.b, tc.a, got, tc.want)
		}
	}
}

// An HrefError with an empty path isn't about the root collection.
func TestContainsPathEmpty(t *testing.T) {
	if containsPath([]string{"/"}, "") {
		t.Errorf(`containsPath(["/"], "") = true, want false`)
	}
	if !onlyHrefs([]Response{{Hrefs: []Href{{Path: "/"}}}}, []string{"/"}) {
		t.Errorf(`onlyHrefs() = false for a response on "/", want true`)
	}
	if onlyHrefs([]Response{{Hrefs: []Href{{Path: ""}}}}, []string{"/"}) {
		t.Errorf(`onlyHrefs() = true for a response on "", want false`)
	}
}

func TestIsInScope(t *testing.T) {
	for _, tc := range []struct {
		scope, p string
		want     bool
	}{
		{"/cal/", "/cal/a.ics", true},
		{"/cal", "/cal/a.ics", true},
		{"/cal/", "/cal/sub/a.ics", true},
		{"/cal/", "/cal/", true},
		{"/cal/", "/cal", true},
		{"/cal/a.ics", "/cal/a.ics", true},
		{"/", "/cal/a.ics", true},
		{"/cal/", "/calendar/a.ics", false},
		{"/cal/", "/other/a.ics", false},
		{"/cal/", "/cal/../other/a.ics", false},
		{"/cal/a.ics", "/cal/b.ics", false},
		{"/cal/", "", false},
	} {
		if got := IsInScope(tc.scope, tc.p); got != tc.want {
			t.Errorf("IsInScope(%q, %q) = %v, want %v", tc.scope, tc.p, got, tc.want)
		}
	}
}
