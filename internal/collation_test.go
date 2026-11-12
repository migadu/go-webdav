package internal

import "testing"

// RFC 4791 section 7.5 and RFC 6352 section 8.3: the "default" collation
// identifier (RFC 4790 section 3.1) names the default collation of the
// protocol, as an absent one does.
func TestCollateKeyDefault(t *testing.T) {
	if !IsSupportedCollation("default") {
		t.Errorf(`IsSupportedCollation("default") = false, want true`)
	}
	for _, defaultCollation := range []string{CollationASCIICasemap, CollationUnicodeCasemap} {
		const s = "Café"
		want, _ := CollateKey(s, "", defaultCollation)
		got, ok := CollateKey(s, "default", defaultCollation)
		if !ok || got != want {
			t.Errorf(`CollateKey(%q, "default", %q) = %q, %v, want %q, true`, s, defaultCollation, got, ok, want)
		}
	}
}

// RFC 5051 section 2: i;unicode-casemap converts each codepoint to its
// titlecase, then fully decomposes it ("effectively Normalization Form KD").
func TestCollateKeyUnicodeCasemap(t *testing.T) {
	for _, tc := range []struct {
		name string
		a, b string
	}{
		{"ASCII case", "jose", "JOSE"},
		{"non-ASCII case", "josé", "JOSÉ"},
		// U+00E9 and U+0065 U+0301 both prepare to U+0045 U+0301
		{"precomposed and decomposed", "José", "José"},
		// U+FF4A... have <wide> decompositions to ASCII
		{"fullwidth compatibility", "ｊｏｓｅ", "JOSE"},
		// U+0131 has titlecase U+0049
		{"dotless i titlecase", "ı", "I"},
		// U+01C6 has titlecase U+01C5, as U+01C4 has
		{"digraph titlecase", "ǆ", "Ǆ"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ka, _ := CollateKey(tc.a, CollationUnicodeCasemap, "")
			kb, _ := CollateKey(tc.b, CollationUnicodeCasemap, "")
			if ka != kb {
				t.Errorf("CollateKey(%+q) = %+q, CollateKey(%+q) = %+q, want equal", tc.a, ka, tc.b, kb)
			}
		})
	}

	for _, tc := range []struct {
		name, s, want string
	}{
		{"ASCII", "Jose", "JOSE"},
		// The example of RFC 5051 section 2
		{"RFC 5051 example", "Ǆ", "Dž"},
		{"titlecase then decomposition", "xé", "XÉ"},
		// Each codepoint is prepared on its own, without the canonical
		// reordering of NFKD across codepoints
		{"no reordering across codepoints", "ẹ́", "Ẹ́"},
		// RFC 5051 section 2 step (1)(b): a string which isn't valid UTF-8
		// is used unchanged with i;octet
		{"invalid UTF-8", "\xffjosé", "\xffjosé"},
		{"overlong UTF-8", "\xc1\xaajose", "\xc1\xaajose"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := CollateKey(tc.s, CollationUnicodeCasemap, "")
			if !ok || got != tc.want {
				t.Errorf("CollateKey(%+q) = %+q, %v, want %+q, true", tc.s, got, ok, tc.want)
			}
		})
	}
}
