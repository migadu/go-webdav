package internal

import (
	"encoding/xml"
	"path"
	"strings"
)

// Principal properties, see RFC 3744 section 4.
var (
	AlternateURISetName = xml.Name{Namespace, "alternate-URI-set"}
	PrincipalURLName    = xml.Name{Namespace, "principal-URL"}
	GroupMembershipName = xml.Name{Namespace, "group-membership"}
)

// https://datatracker.ietf.org/doc/html/rfc3744#section-4.1
type AlternateURISet struct {
	XMLName xml.Name `xml:"DAV: alternate-URI-set"`
	Hrefs   []Href   `xml:"href"`
}

// https://datatracker.ietf.org/doc/html/rfc3744#section-4.2
type PrincipalURL struct {
	XMLName xml.Name `xml:"DAV: principal-URL"`
	Href    Href     `xml:"href"`
}

// https://datatracker.ietf.org/doc/html/rfc3744#section-4.4
type GroupMembership struct {
	XMLName xml.Name `xml:"DAV: group-membership"`
	Hrefs   []Href   `xml:"href"`
}

// AddPrincipalProps adds to props the properties RFC 3744 section 4 defines
// for the principal at principalPath: DAV:principal-URL, an empty
// DAV:alternate-URI-set and DAV:group-membership, and DAV:displayname, which a
// principal must have non-empty. If displayName is empty, the last segment of
// principalPath is used.
func AddPrincipalProps(props map[xml.Name]PropFindFunc, principalPath, displayName string) {
	if displayName == "" {
		displayName = path.Base(strings.TrimSuffix(principalPath, "/"))
		if displayName == "/" || displayName == "." || displayName == "" {
			displayName = "principal"
		}
	}

	props[PrincipalURLName] = PropFindValue(&PrincipalURL{Href: Href{Path: principalPath}})
	props[AlternateURISetName] = PropFindValue(&AlternateURISet{})
	props[GroupMembershipName] = PropFindValue(&GroupMembership{})
	if _, ok := props[DisplayNameName]; !ok {
		props[DisplayNameName] = PropFindValue(&DisplayName{Name: displayName})
	}
}
