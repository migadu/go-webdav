// Package webdav provides a client and server WebDAV filesystem implementation.
//
// WebDAV is defined in RFC 4918.
package webdav

import (
	"fmt"
	"strings"
	"time"

	"github.com/emersion/go-webdav/internal"
)

// FileInfo holds information about a WebDAV file.
type FileInfo struct {
	Path     string
	Size     int64
	ModTime  time.Time
	IsDir    bool
	MIMEType string
	ETag     string
}

type CreateOptions struct {
	IfMatch     ConditionalMatch
	IfNoneMatch ConditionalMatch
}

type RemoveAllOptions struct {
	IfMatch     ConditionalMatch
	IfNoneMatch ConditionalMatch
}

type CopyOptions struct {
	NoRecursive bool
	NoOverwrite bool
}

type MoveOptions struct {
	NoOverwrite bool
}

// ConditionalMatch represents the value of an If-Match or If-None-Match
// header field (RFC 7232 sections 3.1 and 3.2): either a wildcard, or a
// comma-separated list of entity-tags.
type ConditionalMatch string

func (val ConditionalMatch) IsSet() bool {
	return val != ""
}

func (val ConditionalMatch) IsWildcard() bool {
	return val == "*"
}

// ETag returns the entity-tag of a value holding a single one.
func (val ConditionalMatch) ETag() (string, error) {
	var e internal.ETag
	if err := e.UnmarshalText([]byte(val)); err != nil {
		return "", err
	}
	return string(e), nil
}

// MatchETag reports whether the current entity-tag of a resource matches the
// value, using the strong comparison If-Match requires: a weak entity-tag
// never matches. etag is empty if the resource has no current representation,
// in which case nothing matches, not even a wildcard.
func (val ConditionalMatch) MatchETag(etag string) (bool, error) {
	return val.match(etag, true)
}

// MatchETagWeak is like MatchETag, but uses the weak comparison If-None-Match
// requires: entity-tags match if their opaque-tags are equal, weak or not.
func (val ConditionalMatch) MatchETagWeak(etag string) (bool, error) {
	return val.match(etag, false)
}

func (val ConditionalMatch) match(etag string, strong bool) (bool, error) {
	if etag == "" {
		return false, nil
	}
	if val.IsWildcard() {
		return true, nil
	}

	etags, err := val.etags()
	if err != nil {
		return false, err
	}
	cur := internal.ETag(etag)
	for _, e := range etags {
		if strong && (e.IsWeak() || cur.IsWeak()) {
			continue
		}
		if opaqueTag(e) == opaqueTag(cur) {
			return true, nil
		}
	}
	return false, nil
}

// etags parses the list of entity-tags of the value.
func (val ConditionalMatch) etags() ([]internal.ETag, error) {
	var l []internal.ETag
	s := string(val)
	for {
		s = strings.TrimLeft(s, " \t")
		if s == "" {
			break
		}
		if s[0] == ',' {
			// Empty list elements are allowed (RFC 7230 section 7)
			s = s[1:]
			continue
		}

		// An opaque-tag may contain commas, so find its closing quote
		n := 0
		if strings.HasPrefix(s, "W/") {
			n = 2
		}
		if strings.HasPrefix(s[n:], `"`) {
			end := strings.IndexByte(s[n+1:], '"')
			if end < 0 {
				return nil, fmt.Errorf("webdav: malformed entity-tag in %q", string(val))
			}
			n += end + 2
		} else {
			// Out of spec unquoted opaque-tag: up to the next comma
			n = strings.IndexByte(s, ',')
			if n < 0 {
				n = len(s)
			}
		}

		var etag internal.ETag
		if err := etag.UnmarshalText([]byte(s[:n])); err != nil {
			return nil, err
		}
		l = append(l, etag)

		s = strings.TrimLeft(s[n:], " \t")
		if s != "" && s[0] != ',' {
			return nil, fmt.Errorf("webdav: malformed entity-tag list %q", string(val))
		}
	}
	if len(l) == 0 {
		return nil, fmt.Errorf("webdav: empty entity-tag list %q", string(val))
	}
	return l, nil
}

// opaqueTag strips the weakness indicator of an entity-tag. A malformed weak
// entity-tag, such as a backend may store, is returned whole: it contains a
// double quote, so it matches no parsed opaque-tag.
func opaqueTag(etag internal.ETag) string {
	s := string(etag)
	if etag.IsWeak() && len(s) >= 4 && strings.HasSuffix(s, `"`) {
		return s[3 : len(s)-1]
	}
	return s
}
