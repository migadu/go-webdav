package internal

import (
	"encoding/xml"
	"errors"
	"net/http"
	"slices"
)

// Mkcol is the body of an extended MKCOL request (RFC 5689 section 5.1).
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
	var m Mkcol
	if IsRequestBodyEmpty(r) {
		return &m, nil
	}
	if !isContentXML(r.Header) {
		return nil, HTTPErrorf(http.StatusUnsupportedMediaType, "webdav: expected an XML MKCOL request body")
	}
	if err := decodeXMLBody(r, &m); err != nil {
		return nil, err
	}
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

// DecodeProp decodes the properties m sets into v, as a DAV:prop element
// holding them: v is generally a struct with a field for each property the
// server reads, tagged with its name.
func (m *Mkcol) DecodeProp(v interface{}) error {
	prop := NewRawXMLElement(xml.Name{Namespace, "prop"}, nil, m.props())
	if err := prop.Decode(v); err != nil {
		return &HTTPError{http.StatusBadRequest, err}
	}
	return nil
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
		return m.PropError(ResourceTypeName, newValidResourceTypeError())
	}
	return nil
}

// newValidResourceTypeError returns a 403 Forbidden error with the
// DAV:valid-resourcetype precondition (RFC 5689 section 3.3).
func newValidResourceTypeError() error {
	elt := NewRawXMLElement(xml.Name{Namespace, "valid-resourcetype"}, nil, nil)
	return &HTTPError{Code: http.StatusForbidden, Err: &Error{Raw: []RawXMLValue{*elt}}}
}

// PropError returns the error of m when the property name it sets can't be
// set, as err says. RFC 5689 section 3 requires a DAV:mkcol-response element
// with the status of each property: the error has the status of err, and
// wraps one reporting that status for the property, along with the DAV:error
// element err wraps if any, and 424 Failed Dependency for the others.
func (m *Mkcol) PropError(name xml.Name, err error) error {
	code := HTTPErrorFromError(err).Code
	var errElt *Error
	errors.As(err, &errElt)
	resp := &MkcolResponse{PropStats: []PropStat{{
		Prop:   Prop{Raw: xmlNamesToRaw([]xml.Name{name})},
		Status: Status{Code: code},
		Error:  errElt,
	}}}

	var others []xml.Name
	for _, raw := range m.props() {
		if n, ok := raw.XMLName(); ok && n != name && !slices.Contains(others, n) {
			others = append(others, n)
		}
	}
	if len(others) > 0 {
		resp.PropStats = append(resp.PropStats, PropStat{
			Prop:   Prop{Raw: xmlNamesToRaw(others)},
			Status: Status{Code: http.StatusFailedDependency},
		})
	}
	return &HTTPError{Code: code, Err: resp}
}
