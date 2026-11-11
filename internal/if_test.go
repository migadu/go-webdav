package internal

import (
	"net/http"
	"reflect"
	"testing"
	"time"
)

// RFC 4918 section 10.4.2: a Resource-Tag applies to all the lists following
// it, up to the next Resource-Tag, and an If header field is made either of
// untagged lists or of tagged lists, not both.
func TestParseConditions_tagged(t *testing.T) {
	for _, tc := range []struct {
		s    string
		want [][]Condition
	}{
		{
			s: `</a> (<urn:t1>) (["e1"]) </b> (Not <urn:t2>)`,
			want: [][]Condition{
				{{Resource: "/a", Token: "urn:t1"}},
				{{Resource: "/a", ETag: `"e1"`}},
				{{Resource: "/b", Not: true, Token: "urn:t2"}},
			},
		},
		{
			s: `<http://example.com/a/>(<urn:t1>)(<urn:t2>)`,
			want: [][]Condition{
				{{Resource: "http://example.com/a/", Token: "urn:t1"}},
				{{Resource: "http://example.com/a/", Token: "urn:t2"}},
			},
		},
	} {
		got, err := ParseConditions(tc.s)
		if err != nil {
			t.Errorf("ParseConditions(%q) = %v", tc.s, err)
		} else if !reflect.DeepEqual(got, tc.want) {
			t.Errorf("ParseConditions(%q) = %+v, want %+v", tc.s, got, tc.want)
		}
	}
}

func TestParseConditions_entityTags(t *testing.T) {
	for _, tc := range []struct {
		s    string
		want [][]Condition
	}{
		{`(["a]b"])`, [][]Condition{{{ETag: `"a]b"`}}}},
		{`([W/"x y"] Not ["z"])`, [][]Condition{{{ETag: `W/"x y"`}, {Not: true, ETag: `"z"`}}}},
		{"(\t<urn:t1>\t[\"e\"] )", [][]Condition{{{Token: "urn:t1"}, {ETag: `"e"`}}}},
	} {
		got, err := ParseConditions(tc.s)
		if err != nil {
			t.Errorf("ParseConditions(%q) = %v", tc.s, err)
		} else if !reflect.DeepEqual(got, tc.want) {
			t.Errorf("ParseConditions(%q) = %+v, want %+v", tc.s, got, tc.want)
		}
	}
}

func TestParseConditions_invalid(t *testing.T) {
	for _, s := range []string{
		``,
		`()`,
		`</a>`,
		`</a> ()`,
		`(<urn:t1>`,
		`(<urn:t1>) </b> (<urn:t2>)`, // untagged then tagged
		`</a> (<urn:t1>) </b>`,
		`(Nope <urn:t1>)`,
		`(Not)`,
		`(<urn:t1>) junk`,
		`(["unterminated])`,
		`(urn:t1)`,
	} {
		if l, err := ParseConditions(s); err == nil {
			t.Errorf("ParseConditions(%q) = %+v, want an error", s, l)
		}
	}
}

// RFC 4918 section 10.7: the Timeout header field is a list of TimeType
// values, the server picks one. Values are case-insensitive, unknown values
// are skipped, and Second- values are at most 2^32-1.
func TestParseTimeout(t *testing.T) {
	for _, tc := range []struct {
		s    string
		want time.Duration
	}{
		{"Infinite", 0},
		{"infinite", 0},
		{"Second-3600", time.Hour},
		{"second-60", time.Minute},
		{" Second-60 ", time.Minute},
		{"Infinite, Second-4100000000", 0},
		{"Second-4100000000, Infinite", 4100000000 * time.Second},
		{"Second-99999999999999999999", (1<<32 - 1) * time.Second},
		{"Extend-60, Second-60", time.Minute},
		{"Second-0, Second-60", time.Minute},
	} {
		got, err := ParseTimeout(tc.s)
		if err != nil {
			t.Errorf("ParseTimeout(%q) = %v", tc.s, err)
		} else if got.Duration != tc.want {
			t.Errorf("ParseTimeout(%q) = %v, want %v", tc.s, got.Duration, tc.want)
		}
	}

	for _, s := range []string{"", "bogus", "Second-", "Second-x", "Second--1"} {
		if got, err := ParseTimeout(s); err == nil {
			t.Errorf("ParseTimeout(%q) = %v, want an error", s, got.Duration)
		}
	}
}

func TestParseLockToken(t *testing.T) {
	for _, tc := range []struct {
		s, want string
	}{
		{"<urn:uuid:x>", "urn:uuid:x"},
		{" <opaquelocktoken:y> ", "opaquelocktoken:y"},
	} {
		if got, err := ParseLockToken(tc.s); err != nil || got != tc.want {
			t.Errorf("ParseLockToken(%q) = %q, %v, want %q", tc.s, got, err, tc.want)
		}
	}
	for _, s := range []string{"", "<>", "urn:uuid:x", "<urn:uuid:x", "<a> <b>"} {
		if got, err := ParseLockToken(s); err == nil {
			t.Errorf("ParseLockToken(%q) = %q, want an error", s, got)
		}
	}
}

// RFC 4918 section 9.10.2: a lock refresh names a single lock token in the
// If header field.
func TestParseSubmittedToken(t *testing.T) {
	for _, tc := range []struct {
		s, want string
	}{
		{"", ""},
		{"(<urn:t1>)", "urn:t1"},
		{"</a> (<urn:t1>)", "urn:t1"},
	} {
		h := http.Header{}
		if tc.s != "" {
			h.Set("If", tc.s)
		}
		if got, err := ParseSubmittedToken(h); err != nil || got != tc.want {
			t.Errorf("ParseSubmittedToken(%q) = %q, %v, want %q", tc.s, got, err, tc.want)
		}
	}
	for _, s := range []string{
		"(Not <urn:t1>)",
		`(["etag"])`,
		"(<urn:t1>) (<urn:t2>)",
		"(<urn:t1> <urn:t2>)",
		"(<urn:t1>",
	} {
		h := http.Header{"If": {s}}
		if got, err := ParseSubmittedToken(h); err == nil {
			t.Errorf("ParseSubmittedToken(%q) = %q, want an error", s, got)
		} else if code := HTTPErrorFromError(err).Code; code != http.StatusBadRequest {
			t.Errorf("ParseSubmittedToken(%q) = %v, want 400 Bad Request", s, err)
		}
	}
}
