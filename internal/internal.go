// Package internal provides low-level helpers for WebDAV clients and servers.
package internal

import (
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// Depth indicates whether a request applies to the resource's members. It's
// defined in RFC 4918 section 10.2.
type Depth int

const (
	// DepthZero indicates that the request applies only to the resource.
	DepthZero Depth = 0
	// DepthOne indicates that the request applies to the resource and its
	// internal members only.
	DepthOne Depth = 1
	// DepthInfinity indicates that the request applies to the resource and all
	// of its members.
	DepthInfinity Depth = -1
)

// ParseDepth parses a Depth header.
func ParseDepth(s string) (Depth, error) {
	switch s {
	case "0":
		return DepthZero, nil
	case "1":
		return DepthOne, nil
	case "infinity":
		return DepthInfinity, nil
	}
	return 0, fmt.Errorf("webdav: invalid Depth value")
}

// String formats the depth.
func (d Depth) String() string {
	switch d {
	case DepthZero:
		return "0"
	case DepthOne:
		return "1"
	case DepthInfinity:
		return "infinity"
	}
	panic("webdav: invalid Depth value")
}

func (d Depth) MarshalText() ([]byte, error) {
	return []byte(d.String()), nil
}

// ParseOverwrite parses an Overwrite header.
func ParseOverwrite(s string) (bool, error) {
	switch s {
	case "T":
		return true, nil
	case "F":
		return false, nil
	}
	return false, fmt.Errorf("webdav: invalid Overwrite value")
}

// FormatOverwrite formats an Overwrite header.
func FormatOverwrite(overwrite bool) string {
	if overwrite {
		return "T"
	} else {
		return "F"
	}
}

// Timeout is the value of a Timeout header field (RFC 4918 section 10.7), or
// of a DAV:timeout XML element (RFC 4918 section 14.29). A zero Duration
// means "Infinite".
type Timeout struct {
	Duration time.Duration
}

// maxTimeoutSeconds is the largest "Second-" timeout (RFC 4918 section 10.7).
const maxTimeoutSeconds = 1<<32 - 1

// ParseTimeout parses a Timeout header field: a list of timeout values, in
// order of preference. It returns the first valid one, skipping the values
// it doesn't understand. Values are case-insensitive, and "Second-" values
// larger than 2^32-1 are capped.
func ParseTimeout(s string) (Timeout, error) {
	for _, v := range strings.Split(s, ",") {
		v = strings.TrimSpace(v)
		if strings.EqualFold(v, "Infinite") {
			return Timeout{}, nil
		}
		if len(v) <= len("Second-") || !strings.EqualFold(v[:len("Second-")], "Second-") {
			continue
		}
		digits := v[len("Second-"):]
		if strings.Trim(digits, "0123456789") != "" {
			continue
		}
		n, err := strconv.ParseUint(digits, 10, 32)
		if err != nil {
			// Too many digits
			n = maxTimeoutSeconds
		}
		if n == 0 {
			continue
		}
		return Timeout{Duration: time.Duration(n) * time.Second}, nil
	}
	return Timeout{}, fmt.Errorf("webdav: invalid Timeout value %q", s)
}

func (t Timeout) String() string {
	if t.Duration == 0 {
		return "Infinite"
	}
	// Round up, so that a lock about to expire isn't reported as expired
	return fmt.Sprintf("Second-%d", (t.Duration+time.Second-1)/time.Second)
}

func (t Timeout) MarshalText() ([]byte, error) {
	return []byte(t.String()), nil
}

// ParseLockToken parses a Lock-Token header field (RFC 4918 section 10.5): a
// Coded-URL.
func ParseLockToken(s string) (string, error) {
	s = strings.TrimSpace(s)
	if len(s) < 3 || s[0] != '<' || s[len(s)-1] != '>' || strings.ContainsAny(s[1:len(s)-1], "<> \t") {
		return "", fmt.Errorf("webdav: invalid Lock-Token value")
	}
	return s[1 : len(s)-1], nil
}

func FormatLockToken(token string) string {
	return fmt.Sprintf("<%v>", token)
}

// ParseSubmittedToken parses the If header field of a lock refresh request,
// which must name a single lock token (RFC 4918 section 9.10.2). It returns
// an empty string if there is no If header field.
func ParseSubmittedToken(h http.Header) (string, error) {
	hif := h.Get("If")
	if hif == "" {
		return "", nil
	}

	conditions, err := ParseConditions(hif)
	if err != nil {
		return "", &HTTPError{http.StatusBadRequest, err}
	}
	if len(conditions) != 1 || len(conditions[0]) != 1 {
		return "", HTTPErrorf(http.StatusBadRequest, "webdav: the If header field of a lock refresh must contain a single lock token")
	}
	cond := conditions[0][0]
	if cond.Not || cond.Token == "" {
		return "", HTTPErrorf(http.StatusBadRequest, "webdav: the If header field of a lock refresh must contain a single lock token")
	}
	return cond.Token, nil
}

// Condition is a condition of an If header field (RFC 4918 section 10.4),
// matching a state token, such as a lock token, or an entity-tag.
//
// Only one of Token or ETag is set.
type Condition struct {
	// Resource is the Resource-Tag of the list holding the condition, or
	// empty for an untagged list, which applies to the Request-URI.
	Resource string
	Not      bool
	// Token is a state token, without the angle brackets of its Coded-URL.
	Token string
	// ETag is an entity-tag, with its double quotes.
	ETag string
}

type conditionParser struct {
	s string
}

func (p *conditionParser) acceptByte(ch byte) bool {
	if len(p.s) == 0 || p.s[0] != ch {
		return false
	}
	p.s = p.s[1:]
	return true
}

func (p *conditionParser) expectByte(ch byte) error {
	if len(p.s) == 0 {
		return fmt.Errorf("webdav: invalid If value: expected %q, got EOF", ch)
	} else if p.s[0] != ch {
		return fmt.Errorf("webdav: invalid If value: expected %q, got %q", ch, p.s[0])
	}
	p.s = p.s[1:]
	return nil
}

func (p *conditionParser) lws() bool {
	lws := false
	for p.acceptByte(' ') || p.acceptByte('\t') {
		lws = true
	}
	return lws
}

// codedURL parses the rest of a Coded-URL or a Resource-Tag, after the
// opening angle bracket.
func (p *conditionParser) codedURL() (string, error) {
	i := strings.IndexAny(p.s, "<> \t")
	if i < 0 || p.s[i] != '>' {
		return "", fmt.Errorf("webdav: invalid If value: unterminated URL")
	}
	if i == 0 {
		return "", fmt.Errorf("webdav: invalid If value: empty URL")
	}
	s := p.s[:i]
	p.s = p.s[i+1:]
	return s, nil
}

// entityTag parses the rest of an entity-tag condition, after the opening
// square bracket. The entity-tag may contain square brackets in its quoted
// opaque-tag.
func (p *conditionParser) entityTag() (string, error) {
	n := 0
	if strings.HasPrefix(p.s, "W/") {
		n = 2
	}
	if !strings.HasPrefix(p.s[n:], `"`) {
		return "", fmt.Errorf("webdav: invalid If value: expected a quoted entity-tag")
	}
	end := strings.IndexByte(p.s[n+1:], '"')
	if end < 0 {
		return "", fmt.Errorf("webdav: invalid If value: unterminated entity-tag")
	}
	n += end + 2
	etag := p.s[:n]
	p.s = p.s[n:]
	if err := p.expectByte(']'); err != nil {
		return "", err
	}
	return etag, nil
}

func (p *conditionParser) condition() (*Condition, error) {
	not := strings.HasPrefix(p.s, "Not")
	if not {
		p.s = strings.TrimPrefix(p.s, "Not")
		p.lws()
	}

	if p.acceptByte('<') {
		token, err := p.codedURL()
		if err != nil {
			return nil, err
		}
		return &Condition{Not: not, Token: token}, nil
	} else if p.acceptByte('[') {
		etag, err := p.entityTag()
		if err != nil {
			return nil, err
		}
		return &Condition{Not: not, ETag: etag}, nil
	} else {
		return nil, fmt.Errorf("webdav: invalid If value: invalid condition")
	}
}

func (p *conditionParser) list() ([]Condition, error) {
	if err := p.expectByte('('); err != nil {
		return nil, err
	}
	p.lws()

	var l []Condition
	for !p.acceptByte(')') {
		cond, err := p.condition()
		if err != nil {
			return nil, err
		}
		l = append(l, *cond)
		p.lws()
	}

	if len(l) == 0 {
		return nil, fmt.Errorf("webdav: invalid If value: empty list")
	}
	return l, nil
}

// parse parses an If header field: either a sequence of untagged lists, or a
// sequence of Resource-Tags, each followed by the lists applying to it.
func (p *conditionParser) parse() ([][]Condition, error) {
	var (
		conditions [][]Condition
		resource   string
		tagged     bool
	)
	for {
		p.lws()
		if p.s == "" {
			break
		}

		if p.acceptByte('<') {
			if len(conditions) > 0 && !tagged {
				return nil, fmt.Errorf("webdav: invalid If value: untagged and tagged lists can't be mixed")
			}
			var err error
			resource, err = p.codedURL()
			if err != nil {
				return nil, err
			}
			tagged = true
			p.lws()
		} else if tagged && !strings.HasPrefix(p.s, "(") {
			return nil, fmt.Errorf("webdav: invalid If value: expected a list")
		}

		// A Resource-Tag is followed by at least one list
		for {
			l, err := p.list()
			if err != nil {
				return nil, err
			}
			for i := range l {
				l[i].Resource = resource
			}
			conditions = append(conditions, l)

			p.lws()
			if !strings.HasPrefix(p.s, "(") {
				break
			}
		}
	}

	if len(conditions) == 0 {
		return nil, fmt.Errorf("webdav: invalid If value: empty list")
	}
	return conditions, nil
}

// ParseConditions parses an If header field (RFC 4918 section 10.4). It
// returns the lists of conditions: the If header field evaluates to true if
// all the conditions of any list evaluate to true.
func ParseConditions(s string) ([][]Condition, error) {
	p := conditionParser{s}
	return p.parse()
}

type HTTPError struct {
	Code int
	Err  error
}

func HTTPErrorFromError(err error) *HTTPError {
	if err == nil {
		return nil
	}
	if httpErr, ok := err.(*HTTPError); ok {
		return httpErr
	} else {
		return &HTTPError{http.StatusInternalServerError, err}
	}
}

func IsNotFound(err error) bool {
	var httpErr *HTTPError
	if errors.As(err, &httpErr) {
		return httpErr.Code == http.StatusNotFound
	}
	return false
}

func HTTPErrorf(code int, format string, a ...interface{}) *HTTPError {
	return &HTTPError{code, fmt.Errorf(format, a...)}
}

func (err *HTTPError) Error() string {
	s := fmt.Sprintf("%v %v", err.Code, http.StatusText(err.Code))
	if err.Err != nil {
		return fmt.Sprintf("%v: %v", s, err.Err)
	} else {
		return s
	}
}

func (err *HTTPError) Unwrap() error {
	return err.Err
}

// HrefError is an error about a single resource, e.g. one of the resources
// listed in a multi-status response.
type HrefError struct {
	Href url.URL
	Err  error
}

func (err *HrefError) Error() string {
	return fmt.Sprintf("%v: %v", err.Href.String(), err.Err)
}

func (err *HrefError) Unwrap() error {
	return err.Err
}
