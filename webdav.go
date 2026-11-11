// Package webdav provides a client and server WebDAV filesystem implementation.
//
// WebDAV is defined in RFC 4918.
package webdav

import (
	"encoding/xml"
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

// DeadProperty is a dead property of a file: a property which clients define
// and set with PROPPATCH, and which the server stores and returns as is,
// without interpreting it (RFC 4918 section 4).
//
// A dead property is an XML element, the property element, whose name is the
// name of the property and whose content is its value. DeadProperty
// implements xml.Marshaler and xml.Unmarshaler to encode and decode it.
//
// The value is kept as RFC 4918 section 4.3 requires: the namespace and local
// name of the elements and attributes, the values of the attributes, and the
// character data, whitespace included. Namespace prefixes, comments and
// processing instructions aren't kept.
type DeadProperty struct {
	// XMLName is the name of the property.
	XMLName xml.Name
	// Attr lists the attributes of the property element, such as xml:lang,
	// whose name is in the "http://www.w3.org/XML/1998/namespace" namespace.
	// Namespace declarations aren't attributes.
	Attr []xml.Attr
	// InnerXML is the value of the property: the content of the property
	// element, which is an XML fragment, such as character data or elements.
	// The elements declare the namespaces they use, so that the fragment is
	// well-formed on its own.
	InnerXML []byte
}

// newDeadProperty converts a property element.
func newDeadProperty(raw *internal.RawXMLValue) (*DeadProperty, error) {
	name, ok := raw.XMLName()
	if !ok {
		return nil, fmt.Errorf("webdav: dead property isn't an element")
	}
	inner, err := raw.InnerXML()
	if err != nil {
		return nil, err
	}
	return &DeadProperty{XMLName: name, Attr: raw.Attr(), InnerXML: inner}, nil
}

// rawXMLValue converts the property into a property element. It fails if
// InnerXML isn't well-formed.
func (prop *DeadProperty) rawXMLValue() (*internal.RawXMLValue, error) {
	raw, err := internal.NewRawXMLFragment(prop.XMLName, prop.Attr, prop.InnerXML)
	if err != nil {
		return nil, fmt.Errorf("webdav: malformed dead property <%v %v>: %w", prop.XMLName.Space, prop.XMLName.Local, err)
	}
	return raw, nil
}

// MarshalXML implements xml.Marshaler: it encodes the property element. It
// fails if InnerXML isn't well-formed.
func (prop DeadProperty) MarshalXML(e *xml.Encoder, start xml.StartElement) error {
	raw, err := prop.rawXMLValue()
	if err != nil {
		return err
	}
	return raw.MarshalXML(e, start)
}

// UnmarshalXML implements xml.Unmarshaler: it decodes a property element.
func (prop *DeadProperty) UnmarshalXML(d *xml.Decoder, start xml.StartElement) error {
	var raw internal.RawXMLValue
	if err := raw.UnmarshalXML(d, start); err != nil {
		return err
	}
	p, err := newDeadProperty(&raw)
	if err != nil {
		return err
	}
	*prop = *p
	return nil
}

// DeadPropertyPatch is an instruction of a PROPPATCH request: it sets or
// removes a dead property.
type DeadPropertyPatch struct {
	// Remove is true to remove the property, and false to set it.
	Remove bool
	// Property is the property to set. To remove one, only its XMLName is
	// used.
	Property DeadProperty
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

// NewConditionalMatch builds the value of an If-Match or If-None-Match header
// field matching the given entity-tags, as found in FileInfo.ETag,
// caldav.CalendarObject.ETag and carddav.AddressObject.ETag: a bare strong
// opaque-tag, or a weak entity-tag in the W/"..." form. With no entity-tags,
// the value is empty, that is, not set.
func NewConditionalMatch(etags ...string) ConditionalMatch {
	l := make([]string, len(etags))
	for i, etag := range etags {
		l[i] = internal.ETag(etag).String()
	}
	return ConditionalMatch(strings.Join(l, ", "))
}

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
