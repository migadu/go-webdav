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
	} {
		if got := SamePath(tc.a, tc.b); got != tc.want {
			t.Errorf("SamePath(%q, %q) = %v, want %v", tc.a, tc.b, got, tc.want)
		}
		if got := SamePath(tc.b, tc.a); got != tc.want {
			t.Errorf("SamePath(%q, %q) = %v, want %v", tc.b, tc.a, got, tc.want)
		}
	}
}
