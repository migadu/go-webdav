package webdav

import (
	"regexp"
	"testing"
)

var lockTokenRegexp = regexp.MustCompile(`^opaquelocktoken:[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$`)

// RFC 4918 section 6.5 and appendix C: lock tokens are unique URIs, here
// opaquelocktoken URIs made of random UUIDs.
func TestNewLockToken(t *testing.T) {
	seen := make(map[string]bool)
	for i := 0; i < 100; i++ {
		token := newLockToken()
		if !lockTokenRegexp.MatchString(token) {
			t.Fatalf("newLockToken() = %q, want an opaquelocktoken URI made of a version 4 UUID", token)
		}
		if seen[token] {
			t.Fatalf("newLockToken() returned %q twice", token)
		}
		seen[token] = true
	}
}
