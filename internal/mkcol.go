package internal

import (
	"encoding/xml"
	"net/http"
	"slices"
)

// mkcolName is the name of the root element of an extended MKCOL request.
var mkcolName = xml.Name{Namespace, "mkcol"}

// Mkcol is the body of an extended MKCOL request (RFC 5689 section 5.1). It
// also holds the DAV:set element of other requests creating a collection, see
// DecodeCreateCollectionRequest.
type Mkcol struct {
	XMLName xml.Name `xml:"DAV: mkcol"`
	Set     []Set    `xml:"DAV: set"`
}

// MkcolResponse is the body of a response to an extended MKCOL request (RFC
// 5689 section 5.2). It's also an error, the one returned by Mkcol.PropError:
// ServeError serves it as the body of the response.
type MkcolResponse struct {
	XMLName   xml.Name   `xml:"DAV: mkcol-response"`
	PropStats []PropStat `xml:"propstat"`
}

func (resp *MkcolResponse) Error() string {
	b, _ := xml.Marshal(resp)
	return string(b)
}

// DecodeMkcolRequest decodes the body of a MKCOL request. A request without a
// body creates an ordinary collection (RFC 4918 section 9.3), as an extended
// MKCOL request setting no property does (RFC 5689 section 3): an empty Mkcol
// is returned for it.
//
// A body which isn't XML is an HTTP 415 error, as RFC 4918 section 9.3
// requires for a body the server doesn't support. Errors decoding the body
// keep their status, as with DecodeXMLRequest.
func DecodeMkcolRequest(r *http.Request) (*Mkcol, error) {
	return DecodeCreateCollectionRequest(r, mkcolName)
}

// DecodeCreateCollectionRequest decodes the body of a request creating a
// collection and setting its properties all at once, whose root element is
// root and holds DAV:set elements: DAV:mkcol for an extended MKCOL request
// (see DecodeMkcolRequest), CALDAV:mkcalendar for a MKCALENDAR request (RFC
// 4791 section 5.3.1). The body is optional: an empty Mkcol is returned for a
// request without one. A body which isn't XML is an HTTP 415 error, as both
// RFCs have it, and one with another root element an HTTP 400 error.
func DecodeCreateCollectionRequest(r *http.Request, root xml.Name) (*Mkcol, error) {
	var m Mkcol
	if IsRequestBodyEmpty(r) {
		return &m, nil
	}
	if !isContentXML(r.Header) {
		return nil, HTTPErrorf(http.StatusUnsupportedMediaType, "webdav: expected an XML %v request body", r.Method)
	}
	var body struct {
		XMLName xml.Name
		Set     []Set `xml:"DAV: set"`
	}
	if err := decodeXMLBody(r, &body); err != nil {
		return nil, err
	}
	if body.XMLName != root {
		return nil, HTTPErrorf(http.StatusBadRequest, "webdav: expected <%v %v> element in %v request body, got <%v %v>",
			root.Space, root.Local, r.Method, body.XMLName.Space, body.XMLName.Local)
	}
	m.Set = body.Set
	return &m, nil
}

// props returns the properties m sets, in document order.
func (m *Mkcol) props() []RawXMLValue {
	var props []RawXMLValue
	for _, set := range m.Set {
		props = append(props, set.Prop.Raw...)
	}
	return props
}

// SetProps calls set for each property m sets, in document order, so that a
// property set twice gets its last value. The properties are set all at once
// or not at all (RFC 5689 section 3, RFC 4791 section 5.3.1): if set fails for
// some of them, SetProps still calls it for the others, and returns the
// propstat elements reporting each property which failed, with the status,
// the DAV:error element and the description of its error, then 424 Failed
// Dependency for the others. It returns nil if set succeeds for all of them.
//
// An extended MKCOL request reports the propstat elements with PropError, a
// MKCALENDAR request with PropMultiStatus.
func (m *Mkcol) SetProps(set func(raw *RawXMLValue) error) []PropStat {
	props := m.props()
	var failed []xml.Name
	var propStats []PropStat
	for i := range props {
		name, ok := props[i].XMLName()
		if !ok {
			continue
		}
		// A property set twice is reported once, with its first error
		if err := set(&props[i]); err != nil && !slices.Contains(failed, name) {
			failed = append(failed, name)
			propStats = append(propStats, failedPropStat(name, err))
		}
	}
	if len(failed) == 0 {
		return nil
	}
	return append(propStats, m.failedDependencies(failed)...)
}

// CheckResourceType checks that m creates a collection of the resource type
// name: its DAV:resourcetype property must hold DAV:collection and name (RFC
// 5689 section 3).
//
// Otherwise the server doesn't support the resource type m asks for, and the
// error is 403 Forbidden with the DAV:valid-resourcetype precondition
// (section 3.3). Since the property can't be set, it's reported in a
// DAV:mkcol-response element, as in the example of section 3.5. If m doesn't
// set DAV:resourcetype, as a MKCOL request without a body, it asks for an
// ordinary collection: the precondition is then reported in a DAV:error
// element.
func (m *Mkcol) CheckResourceType(name xml.Name) error {
	props := Prop{Raw: m.props()}
	raw := props.Get(ResourceTypeName)
	if raw == nil {
		return newValidResourceTypeError()
	}
	var t ResourceType
	if err := raw.Decode(&t); err != nil {
		return &HTTPError{http.StatusBadRequest, err}
	}
	if !t.Is(CollectionName) || !t.Is(name) {
		return m.PropError(m.SetProps(func(raw *RawXMLValue) error {
			if n, _ := raw.XMLName(); n == ResourceTypeName {
				return newValidResourceTypeError()
			}
			return nil
		}))
	}
	return nil
}

// newValidResourceTypeError returns a 403 Forbidden error with the
// DAV:valid-resourcetype precondition (RFC 5689 section 3.3).
func newValidResourceTypeError() error {
	elt := NewRawXMLElement(xml.Name{Namespace, "valid-resourcetype"}, nil, nil)
	return &HTTPError{Code: http.StatusForbidden, Err: &Error{Raw: []RawXMLValue{*elt}}}
}

// PropError returns the error of m, an extended MKCOL request, when some of
// the properties it sets can't be, as propStats, returned by SetProps,
// report. RFC 5689 section 3 requires a DAV:mkcol-response element with the
// status of each property: the error has the status of the first property
// which failed, and wraps that element, which ServeError serves as the body
// of the response.
func (m *Mkcol) PropError(propStats []PropStat) error {
	return &HTTPError{Code: propStats[0].Status.Code, Err: &MkcolResponse{PropStats: propStats}}
}

// PropMultiStatus is PropError for a MKCALENDAR request for href, which
// reports the properties in a 207 Multi-Status response instead (RFC 4791
// section 5.3.1.1): it returns its body, with propStats in the response
// element for href.
func (m *Mkcol) PropMultiStatus(href string, propStats []PropStat) *MultiStatus {
	return NewMultiStatus(Response{
		Hrefs:     []Href{{Path: href}},
		PropStats: propStats,
	})
}

// failedPropStat returns the propstat element reporting that the property
// name can't be set, as err says: with the status of err, and the DAV:error
// element and the description it has, as NewErrorResponse finds them.
func failedPropStat(name xml.Name, err error) PropStat {
	resp := NewErrorResponse("", err)
	return PropStat{
		Prop:                Prop{Raw: xmlNamesToRaw([]xml.Name{name})},
		Status:              *resp.Status,
		Error:               resp.Error,
		ResponseDescription: resp.ResponseDescription,
	}
}

// failedDependencies returns the propstat element reporting 424 Failed
// Dependency for the properties m sets other than those of failed, or nil if
// there are none.
func (m *Mkcol) failedDependencies(failed []xml.Name) []PropStat {
	var others []xml.Name
	for _, raw := range m.props() {
		if n, ok := raw.XMLName(); ok && !slices.Contains(failed, n) && !slices.Contains(others, n) {
			others = append(others, n)
		}
	}
	if len(others) == 0 {
		return nil
	}
	return []PropStat{{
		Prop:   Prop{Raw: xmlNamesToRaw(others)},
		Status: Status{Code: http.StatusFailedDependency},
	}}
}

// cannotModifyProtectedPropertyName is the precondition of a request setting
// a protected property (RFC 4918 section 16).
var cannotModifyProtectedPropertyName = xml.Name{Namespace, "cannot-modify-protected-property"}

// NewProtectedPropError returns the error of setting a protected property:
// 403 Forbidden, with the DAV:cannot-modify-protected-property precondition
// (RFC 4918 section 9.2.1).
func NewProtectedPropError() error {
	return NewConditionError(http.StatusForbidden, cannotModifyProtectedPropertyName)
}

// protectedProps lists the protected properties which the server maintains
// on collections, or which the RFCs defining them require to be protected:
// RFC 4918 section 15 (getetag, getlastmodified, getcontentlength,
// lockdiscovery, supportedlock), RFC 3253 section 3.1.5
// (supported-report-set), RFC 3744 sections 5.1, 5.3, 5.4 and 5.8 (owner,
// which the server computes, supported-privilege-set,
// current-user-privilege-set, principal-collection-set), RFC 5397 section 3
// (current-user-principal) and RFC 6578 section 4 (sync-token), along with
// CS:getctag, which the server computes.
var protectedProps = []xml.Name{
	GetETagName,
	GetLastModifiedName,
	GetContentLengthName,
	LockDiscoveryName,
	SupportedLockName,
	SupportedReportSetName,
	OwnerName,
	SupportedPrivilegeSetName,
	CurrentUserPrivilegeSetName,
	PrincipalCollectionSetName,
	CurrentUserPrincipalName,
	SyncTokenName,
	GetCTagName,
}

// IsProtectedProp reports whether name is a protected property of the
// collections served by the caldav and carddav packages, which clients can't
// set, as listed by protectedProps or in others.
func IsProtectedProp(name xml.Name, others ...xml.Name) bool {
	return slices.Contains(protectedProps, name) || slices.Contains(others, name)
}
