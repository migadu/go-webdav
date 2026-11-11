package internal

import (
	"encoding/xml"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"reflect"
	"strings"
	"testing"
)

func newHrefError(path string, err error) *HrefError {
	return &HrefError{Href: url.URL{Path: path}, Err: err}
}

func TestHrefError_Error(t *testing.T) {
	err := newHrefError("/dir/c.txt", errors.New("permission denied"))
	if got, want := err.Error(), "/dir/c.txt: permission denied"; got != want {
		t.Errorf("Error() = %q, want %q", got, want)
	}
}

func TestHrefErrorResponses(t *testing.T) {
	forbidden := newHrefError("/a", &HTTPError{Code: http.StatusForbidden})
	ioErr := newHrefError("/b", errors.New("I/O error"))
	locked := newHrefError("/c", HTTPErrorf(http.StatusLocked, "locked by %v", "someone"))
	plain := errors.New("disk on fire")

	for _, tc := range []struct {
		name  string
		err   error
		paths []string // nil if err isn't a partial failure
	}{
		{"nil", nil, nil},
		{"plain", plain, nil},
		{"HTTPError", &HTTPError{Code: http.StatusForbidden}, nil},
		{"HrefError", forbidden, []string{"/a"}},
		{"joined", errors.Join(forbidden, ioErr), []string{"/a", "/b"}},
		{"nested join", errors.Join(errors.Join(forbidden, ioErr), locked), []string{"/a", "/b", "/c"}},
		{"multiple %w", fmt.Errorf("%w; %w", forbidden, ioErr), []string{"/a", "/b"}},
		{"multi-error with nil", multiError{nil, forbidden}, []string{"/a"}},
		{"joined with plain", errors.Join(forbidden, plain), nil},
		{"nested join with plain", errors.Join(forbidden, errors.Join(ioErr, plain)), nil},
		{"wrapped", fmt.Errorf("listing: %w", forbidden), nil},
		{"HTTPError wrapping HrefError", &HTTPError{Code: http.StatusBadGateway, Err: forbidden}, nil},
		{"empty multi-error", multiError(nil), nil},
		{"multi-error of nils", multiError{nil, nil}, nil},
		{"nil HrefError", (*HrefError)(nil), nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			resps, err := HrefErrorResponses(tc.err)
			// multiError values aren't comparable with ==
			if tc.paths == nil && !reflect.DeepEqual(err, tc.err) {
				t.Fatalf("HrefErrorResponses() error = %v, want the original error %v", err, tc.err)
			} else if tc.paths != nil && err != nil {
				t.Fatalf("HrefErrorResponses() error = %v, want nil", err)
			}
			if len(resps) != len(tc.paths) {
				t.Fatalf("HrefErrorResponses() returned %d responses, want %d", len(resps), len(tc.paths))
			}
			for i, resp := range resps {
				if len(resp.Hrefs) != 1 || resp.Hrefs[0].Path != tc.paths[i] {
					t.Errorf("response %d: hrefs = %v, want %q", i, resp.Hrefs, tc.paths[i])
				}
			}
		})
	}
}

func TestNewErrorResponse_elementOrder(t *testing.T) {
	precondition := &Error{Raw: []RawXMLValue{
		*NewRawXMLElement(xml.Name{Space: Namespace, Local: "lock-token-submitted"}, nil, nil),
	}}
	err := &HTTPError{Code: http.StatusLocked, Err: fmt.Errorf("locked: %w", precondition)}
	b, mErr := xml.Marshal(NewErrorResponse("/a", err))
	if mErr != nil {
		t.Fatal(mErr)
	}

	// https://tools.ietf.org/html/rfc4918#section-14.24
	s := string(b)
	last := -1
	for _, elt := range []string{"<href>", "<status>", "<error ", "<responsedescription>"} {
		i := strings.Index(s, elt)
		if i < 0 {
			t.Fatalf("%v missing in %v", elt, s)
		} else if i < last {
			t.Errorf("%v out of order in %v", elt, s)
		}
		last = i
	}
}

type multiError []error

func (m multiError) Error() string   { return "multiple errors" }
func (m multiError) Unwrap() []error { return m }

func TestHrefErrorResponses_response(t *testing.T) {
	precondition := &Error{Raw: []RawXMLValue{
		*NewRawXMLElement(xml.Name{Space: Namespace, Local: "lock-token-submitted"}, nil, nil),
	}}
	for _, tc := range []struct {
		name  string
		err   error
		code  int
		desc  string
		error bool
	}{
		// The status conveys the code and the error element the DAV:error,
		// so neither is repeated in the description.
		{"HTTPError", &HTTPError{Code: http.StatusForbidden}, http.StatusForbidden, "", false},
		{"HTTPError with cause", HTTPErrorf(http.StatusLocked, "busy"), http.StatusLocked, "busy", false},
		{"wrapped HTTPError", fmt.Errorf("oops: %w", HTTPErrorf(http.StatusLocked, "busy")), http.StatusLocked, "oops: 423 Locked: busy", false},
		{"error", errors.New("I/O error"), http.StatusInternalServerError, "I/O error", false},
		{"nil", nil, http.StatusInternalServerError, "", false},
		{"DAV:error", &HTTPError{Code: http.StatusLocked, Err: precondition}, http.StatusLocked, "", true},
		{"DAV:error with context", &HTTPError{Code: http.StatusLocked, Err: fmt.Errorf("locked: %w", precondition)}, http.StatusLocked, "locked: " + precondition.Error(), true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			resps, err := HrefErrorResponses(newHrefError("/dir/c.txt", tc.err))
			if err != nil || len(resps) != 1 {
				t.Fatalf("HrefErrorResponses() = %v, %v, want one response", resps, err)
			}
			resp := resps[0]
			if len(resp.Hrefs) != 1 || resp.Hrefs[0].Path != "/dir/c.txt" {
				t.Errorf("hrefs = %v, want /dir/c.txt", resp.Hrefs)
			}
			if resp.Status == nil || resp.Status.Code != tc.code {
				t.Errorf("status = %v, want %d", resp.Status, tc.code)
			}
			if resp.ResponseDescription != tc.desc {
				t.Errorf("responsedescription = %q, want %q", resp.ResponseDescription, tc.desc)
			}
			if (resp.Error != nil) != tc.error {
				t.Errorf("error element = %v, want present: %v", resp.Error, tc.error)
			}
			if len(resp.PropStats) != 0 {
				t.Errorf("propstats = %v, want none", resp.PropStats)
			}
		})
	}
}

func TestParseConditions(t *testing.T) {
	tests := []struct {
		name       string
		s          string
		conditions [][]Condition
	}{
		{
			name: "RFC 4918 section 10.4.6: No-tag Production",
			s: `(<urn:uuid:181d4fae-7d8c-11d0-a765-00a0c91e6bf2>
  ["I am an ETag"])
  (["I am another ETag"])`,
			conditions: [][]Condition{
				{
					{Token: "urn:uuid:181d4fae-7d8c-11d0-a765-00a0c91e6bf2"},
					{ETag: `"I am an ETag"`},
				},
				{
					{ETag: `"I am another ETag"`},
				},
			},
		},
		{
			name: `RFC 4918 section 10.4.7: Using "Not" with No-tag Production`,
			s: `(Not <urn:uuid:181d4fae-7d8c-11d0-a765-00a0c91e6bf2>
  <urn:uuid:58f202ac-22cf-11d1-b12d-002035b29092>)`,
			conditions: [][]Condition{
				{
					{Not: true, Token: "urn:uuid:181d4fae-7d8c-11d0-a765-00a0c91e6bf2"},
					{Token: "urn:uuid:58f202ac-22cf-11d1-b12d-002035b29092"},
				},
			},
		},
		{
			name: "RFC 4918 section 10.4.8: Causing a Condition to Always Evaluate to True",
			s: `(<urn:uuid:181d4fae-7d8c-11d0-a765-00a0c91e6bf2>)
  (Not <DAV:no-lock>)`,
			conditions: [][]Condition{
				{
					{Token: "urn:uuid:181d4fae-7d8c-11d0-a765-00a0c91e6bf2"},
				},
				{
					{Not: true, Token: "DAV:no-lock"},
				},
			},
		},
		{
			name: "RFC 4918 section 10.4.9: Tagged List If Header in COPY",
			s: `</resource1>
  (<urn:uuid:181d4fae-7d8c-11d0-a765-00a0c91e6bf2>
  [W/"A weak ETag"]) (["strong ETag"])`,
			conditions: [][]Condition{
				{
					{Resource: "/resource1", Token: "urn:uuid:181d4fae-7d8c-11d0-a765-00a0c91e6bf2"},
					{Resource: "/resource1", ETag: `W/"A weak ETag"`},
				},
				// Both lists apply to /resource1: "either it must be locked
				// with a lock token of [...] or it must have a strongly
				// matching ETag of "strong ETag""
				{
					{Resource: "/resource1", ETag: `"strong ETag"`},
				},
			},
		},
		{
			name: "RFC 4918 section 10.4.10: Matching Lock Tokens with Collection Locks",
			s: `<http://www.example.com/specs/>
  (<urn:uuid:181d4fae-7d8c-11d0-a765-00a0c91e6bf2>)`,
			conditions: [][]Condition{
				{
					{Resource: "http://www.example.com/specs/", Token: "urn:uuid:181d4fae-7d8c-11d0-a765-00a0c91e6bf2"},
				},
			},
		},
		{
			name: "RFC 4918 section 10.4.11: Matching ETags on Unmapped URLs",
			s:    `</specs/rfc2518.doc> (["4217"])`,
			conditions: [][]Condition{
				{
					{Resource: "/specs/rfc2518.doc", ETag: `"4217"`},
				},
			},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			l, err := ParseConditions(strings.ReplaceAll(tc.s, "\n", " "))
			if err != nil {
				t.Fatalf("ParseConditions() = %v", err)
			} else if !reflect.DeepEqual(l, tc.conditions) {
				t.Errorf("ParseConditions() = \n %#v \n but want: \n %#v", l, tc.conditions)
			}
		})
	}
}
