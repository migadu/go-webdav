package internal

import (
	"net/http"
	"strings"
	"testing"
)

// An If header field has at most 100 conditions, however they're split into
// lists.
func TestParseConditions_tooMany(t *testing.T) {
	for _, tc := range []struct {
		s  string
		ok bool
	}{
		{strings.Repeat("(<urn:t1>) ", 100), true},
		{strings.Repeat("(<urn:t1>) ", 101), false},
		{"(" + strings.Repeat(`["e"] `, 100) + ")", true},
		{"(" + strings.Repeat(`["e"] `, 101) + ")", false},
		{"</a> " + strings.Repeat("(<urn:t1> <urn:t2>) ", 50), true},
		{"</a> " + strings.Repeat("(<urn:t1> <urn:t2>) ", 50) + "</b> (<urn:t3>)", false},
		{strings.Repeat("(<urn:t1>) ", 1<<16), false},
	} {
		l, err := ParseConditions(tc.s)
		if tc.ok && err != nil {
			t.Errorf("ParseConditions(%.40q...) = %v", tc.s, err)
		} else if !tc.ok && err == nil {
			t.Errorf("ParseConditions(%.40q...) = %v lists, want an error", tc.s, len(l))
		}
	}

	h := http.Header{"If": {strings.Repeat("(<urn:t1>) ", 101)}}
	if _, err := ParseSubmittedToken(h); err == nil || HTTPErrorFromError(err).Code != http.StatusBadRequest {
		t.Errorf("ParseSubmittedToken() with 101 conditions = %v, want 400 Bad Request", err)
	}
}
