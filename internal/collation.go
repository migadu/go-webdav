package internal

import (
	"strings"
	"unicode"
	"unicode/utf8"
)

const (
	CollationOctet          = "i;octet"
	CollationASCIICasemap   = "i;ascii-casemap"
	CollationUnicodeCasemap = "i;unicode-casemap"
)

// IsSupportedCollation reports whether collation is empty or one of the
// supported RFC 4790 / RFC 5051 collations.
func IsSupportedCollation(collation string) bool {
	switch collation {
	case "", CollationOctet, CollationASCIICasemap, CollationUnicodeCasemap:
		return true
	default:
		return false
	}
}

// CollateKey converts s into a canonical comparison string under collation
// (using defaultCollation when collation is empty). It returns ok=false when
// collation is unsupported.
func CollateKey(s, collation, defaultCollation string) (string, bool) {
	if collation == "" {
		collation = defaultCollation
	}
	switch collation {
	case CollationOctet:
		return s, true
	case CollationASCIICasemap:
		return asciiLower(s), true
	case CollationUnicodeCasemap:
		return unicodeFold(s), true
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

func canonicalFoldRune(r rune) rune {
	min := r
	for next := unicode.SimpleFold(r); next != r; next = unicode.SimpleFold(next) {
		if next < min {
			min = next
		}
	}
	return min
}

func unicodeFold(s string) string {
	var sb strings.Builder
	sb.Grow(len(s))
	for i := 0; i < len(s); {
		r, size := utf8.DecodeRuneInString(s[i:])
		if r == utf8.RuneError && size == 1 {
			sb.WriteByte(s[i])
			i++
			continue
		}
		sb.WriteRune(canonicalFoldRune(r))
		i += size
	}
	return sb.String()
}
