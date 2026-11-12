package internal

import (
	"unicode"
	"unicode/utf8"

	"golang.org/x/text/unicode/norm"
)

const (
	CollationOctet          = "i;octet"
	CollationASCIICasemap   = "i;ascii-casemap"
	CollationUnicodeCasemap = "i;unicode-casemap"

	// CollationDefault is the identifier of the default collation of the
	// protocol (RFC 4790 section 3.1), which is i;ascii-casemap for CalDAV
	// (RFC 4791 section 7.5) and i;unicode-casemap for CardDAV (RFC 6352
	// section 8.3).
	CollationDefault = "default"
)

// SupportedCollations returns the identifiers of the supported collations,
// which servers advertise in the supported-collation-set property (RFC 4791
// section 7.5.1, RFC 6352 section 8.3.1).
func SupportedCollations() []string {
	return []string{CollationASCIICasemap, CollationOctet, CollationUnicodeCasemap}
}

// IsSupportedCollation reports whether collation is empty, "default" or one
// of the supported RFC 4790 / RFC 5051 collations.
func IsSupportedCollation(collation string) bool {
	switch collation {
	case "", CollationDefault, CollationOctet, CollationASCIICasemap, CollationUnicodeCasemap:
		return true
	default:
		return false
	}
}

// CollateKey converts s into a canonical comparison string under collation
// (using defaultCollation when collation is empty or "default"). It returns
// ok=false when collation is unsupported.
func CollateKey(s, collation, defaultCollation string) (string, bool) {
	if collation == "" || collation == CollationDefault {
		collation = defaultCollation
	}
	switch collation {
	case CollationOctet:
		return s, true
	case CollationASCIICasemap:
		return asciiLower(s), true
	case CollationUnicodeCasemap:
		return unicodeCasemap(s), true
	default:
		return "", false
	}
}

func asciiLower(s string) string {
	hasUpper := false
	for i := 0; i < len(s); i++ {
		if s[i] >= 'A' && s[i] <= 'Z' {
			hasUpper = true
			break
		}
	}
	if !hasUpper {
		return s
	}
	b := make([]byte, len(s))
	for i := 0; i < len(s); i++ {
		c := s[i]
		if c >= 'A' && c <= 'Z' {
			c += 'a' - 'A'
		}
		b[i] = c
	}
	return string(b)
}

// unicodeCasemap prepares s for the i;unicode-casemap collation, as the
// "titlecased canonicalized UTF-8" string of RFC 5051 section 2: each
// codepoint is converted to its titlecase, which is then fully decomposed
// (NFKD) on its own. A string which isn't valid UTF-8 is used unchanged, as
// with i;octet.
func unicodeCasemap(s string) string {
	if !utf8.ValidString(s) {
		return s
	}

	b := make([]byte, 0, len(s))
	var buf [utf8.UTFMax]byte
	var decomposed []byte
	for _, r := range s {
		// ASCII titlecases to ASCII, which doesn't decompose
		if r < utf8.RuneSelf {
			if r >= 'a' && r <= 'z' {
				r -= 'a' - 'A'
			}
			b = append(b, byte(r))
			continue
		}
		n := utf8.EncodeRune(buf[:], unicode.ToTitle(r))
		// Not appended to b directly: norm would reorder combining marks
		// across codepoints
		decomposed = norm.NFKD.Append(decomposed[:0], buf[:n]...)
		b = append(b, decomposed...)
	}
	return string(b)
}
