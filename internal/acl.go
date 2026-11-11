package internal

import (
	"encoding/xml"
	"strings"
)

// Access control properties, see RFC 3744 section 5.
var (
	OwnerName                  = xml.Name{Namespace, "owner"}
	SupportedPrivilegeSetName  = xml.Name{Namespace, "supported-privilege-set"}
	PrincipalCollectionSetName = xml.Name{Namespace, "principal-collection-set"}
)

// Owner is the DAV:owner property, which identifies the principal owning a
// resource. It isn't the DAV:owner element of LOCK requests (RFC 4918
// section 14.17), which holds any information about the creator of a lock.
//
// https://datatracker.ietf.org/doc/html/rfc3744#section-5.1
type Owner struct {
	XMLName xml.Name `xml:"DAV: owner"`
	Href    Href     `xml:"href"`
}

// SupportedPrivilegeSet identifies the privileges defined for a resource.
//
// https://datatracker.ietf.org/doc/html/rfc3744#section-5.3
type SupportedPrivilegeSet struct {
	XMLName            xml.Name             `xml:"DAV: supported-privilege-set"`
	SupportedPrivilege []SupportedPrivilege `xml:"DAV: supported-privilege"`
}

// SupportedPrivilege is a privilege of a SupportedPrivilegeSet, along with
// the privileges it aggregates.
//
// https://datatracker.ietf.org/doc/html/rfc3744#section-5.3
type SupportedPrivilege struct {
	XMLName   xml.Name  `xml:"DAV: supported-privilege"`
	Privilege Privilege `xml:"DAV: privilege"`
	// Abstract is set for a privilege which can't be granted or denied on
	// its own in an ACE.
	Abstract    *struct{}   `xml:"DAV: abstract,omitempty"`
	Description Description `xml:"DAV: description"`
	// SupportedPrivilege lists the privileges this one aggregates.
	SupportedPrivilege []SupportedPrivilege `xml:"DAV: supported-privilege,omitempty"`
}

// Description is a human-readable description of what a privilege controls
// access to, in the human language indicated by Lang.
//
// https://datatracker.ietf.org/doc/html/rfc3744#section-5.3
type Description struct {
	XMLName xml.Name `xml:"DAV: description"`
	Lang    string   `xml:"http://www.w3.org/XML/1998/namespace lang,attr,omitempty"`
	Text    string   `xml:",chardata"`
}

// PrincipalCollectionSet lists the collections which hold the principals of
// the server.
//
// https://datatracker.ietf.org/doc/html/rfc3744#section-5.8
type PrincipalCollectionSet struct {
	XMLName xml.Name `xml:"DAV: principal-collection-set"`
	Hrefs   []Href   `xml:"href"`
}

// NewSupportedPrivilege returns a non-abstract SupportedPrivilege for priv,
// described in English by description, which aggregates the privileges
// aggregated.
func NewSupportedPrivilege(priv Privilege, description string, aggregated ...SupportedPrivilege) SupportedPrivilege {
	return SupportedPrivilege{
		Privilege:          priv,
		Description:        Description{Lang: "en", Text: description},
		SupportedPrivilege: aggregated,
	}
}

// NewSupportedPrivilegeSet returns the DAV:supported-privilege-set of a
// resource, which defines the privileges the servers report in
// DAV:current-user-privilege-set: DAV:all, which is abstract, aggregates
// DAV:read and DAV:write, along with others, the privileges defined on the
// resource in addition. DAV:read aggregates the abstract
// DAV:read-current-user-privilege-set, and DAV:write aggregates
// DAV:write-properties, DAV:write-content, DAV:bind and DAV:unbind, as RFC
// 3744 section 3.12 requires.
//
// The privileges which control the DAV:acl property and the ACL and UNLOCK
// methods aren't defined: the CalDAV and CardDAV servers and ServePrincipal,
// which report this set, support none of them.
func NewSupportedPrivilegeSet(others ...SupportedPrivilege) *SupportedPrivilegeSet {
	granted := &struct{}{}

	readCurrentUserPrivilegeSet := NewSupportedPrivilege(Privilege{ReadCurrentUserPrivilegeSet: granted}, "Read the current user privilege set property")
	readCurrentUserPrivilegeSet.Abstract = granted

	privs := []SupportedPrivilege{
		NewSupportedPrivilege(Privilege{Read: granted}, "Read any object",
			readCurrentUserPrivilegeSet,
		),
		NewSupportedPrivilege(Privilege{Write: granted}, "Write any object",
			NewSupportedPrivilege(Privilege{WriteProperties: granted}, "Write properties"),
			NewSupportedPrivilege(Privilege{WriteContent: granted}, "Write resource content"),
			NewSupportedPrivilege(Privilege{Bind: granted}, "Add members to a collection"),
			NewSupportedPrivilege(Privilege{Unbind: granted}, "Remove members from a collection"),
		),
	}
	all := NewSupportedPrivilege(Privilege{All: granted}, "Any operation", append(privs, others...)...)
	all.Abstract = granted

	return &SupportedPrivilegeSet{SupportedPrivilege: []SupportedPrivilege{all}}
}

// NewPrincipalCollectionSet returns the DAV:principal-collection-set of a
// server whose principal is at principalPath: the collection which holds
// the principal. It's empty if the principal is the root.
func NewPrincipalCollectionSet(principalPath string) *PrincipalCollectionSet {
	p := strings.TrimSuffix(principalPath, "/")
	i := strings.LastIndex(p, "/")
	if i < 0 {
		return &PrincipalCollectionSet{}
	}
	return &PrincipalCollectionSet{Hrefs: []Href{{Path: p[:i+1]}}}
}

// AddACLProps adds to props the access control properties of RFC 3744
// section 5 which the servers report for a resource of the current user,
// whose principal path principalPath returns: DAV:owner, the current user,
// DAV:principal-collection-set and DAV:supported-privilege-set, with the
// privileges of NewSupportedPrivilegeSet and others. principalPath is only
// called to return a property which depends on it.
//
// DAV:acl isn't reported: the servers only know the privileges of the
// current user, not the access other principals may have.
func AddACLProps(props map[xml.Name]PropFindFunc, principalPath func() (string, error), others ...SupportedPrivilege) {
	props[OwnerName] = func(*RawXMLValue) (interface{}, error) {
		p, err := principalPath()
		if err != nil {
			return nil, err
		}
		return &Owner{Href: Href{Path: p}}, nil
	}
	props[PrincipalCollectionSetName] = func(*RawXMLValue) (interface{}, error) {
		p, err := principalPath()
		if err != nil {
			return nil, err
		}
		return NewPrincipalCollectionSet(p), nil
	}
	props[SupportedPrivilegeSetName] = func(*RawXMLValue) (interface{}, error) {
		return NewSupportedPrivilegeSet(others...), nil
	}
}
