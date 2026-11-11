package internal

import (
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"net/url"
	"strings"
)

func ServeError(w http.ResponseWriter, err error) {
	code := http.StatusInternalServerError
	var httpErr *HTTPError
	if errors.As(err, &httpErr) {
		code = httpErr.Code
	}

	var errElt *Error
	if errors.As(err, &errElt) {
		w.WriteHeader(code)
		ServeXML(w).Encode(errElt)
		return
	}

	http.Error(w, err.Error(), code)
}

func isContentXML(h http.Header) bool {
	t, _, _ := mime.ParseMediaType(h.Get("Content-Type"))
	return t == "application/xml" || t == "text/xml"
}

func DecodeXMLRequest(r *http.Request, v interface{}) error {
	if !isContentXML(r.Header) {
		return HTTPErrorf(http.StatusBadRequest, "webdav: expected application/xml request")
	}

	if err := xml.NewDecoder(r.Body).Decode(v); err != nil {
		return &HTTPError{http.StatusBadRequest, err}
	}
	return nil
}

func IsRequestBodyEmpty(r *http.Request) bool {
	_, err := r.Body.Read(nil)
	return err == io.EOF
}

func ServeXML(w http.ResponseWriter) *xml.Encoder {
	w.Header().Add("Content-Type", "application/xml; charset=\"utf-8\"")
	w.Write([]byte(xml.Header))
	return xml.NewEncoder(w)
}

func ServeMultiStatus(w http.ResponseWriter, ms *MultiStatus) error {
	// TODO: streaming
	w.WriteHeader(http.StatusMultiStatus)
	return ServeXML(w).Encode(ms)
}

type Backend interface {
	Options(r *http.Request) (caps []string, allow []string, err error)
	HeadGet(w http.ResponseWriter, r *http.Request) error
	PropFind(r *http.Request, pf *PropFind, depth Depth) (*MultiStatus, error)
	PropPatch(r *http.Request, pu *PropertyUpdate) (*Response, error)
	Put(w http.ResponseWriter, r *http.Request) error
	Delete(r *http.Request) error
	Mkcol(r *http.Request) error
	Copy(r *http.Request, dest *Href, recursive, overwrite bool) (created bool, err error)
	Move(r *http.Request, dest *Href, overwrite bool) (created bool, err error)
}

type Handler struct {
	Backend Backend
}

func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	var err error
	if h.Backend == nil {
		err = fmt.Errorf("webdav: no backend available")
	} else {
		switch r.Method {
		case http.MethodOptions:
			err = h.handleOptions(w, r)
		case http.MethodGet, http.MethodHead:
			err = h.Backend.HeadGet(w, r)
		case http.MethodPut:
			err = h.Backend.Put(w, r)
		case http.MethodDelete:
			// TODO: send a multistatus in case of partial failure
			err = h.Backend.Delete(r)
			if err == nil {
				w.WriteHeader(http.StatusNoContent)
			}
		case "PROPFIND":
			err = h.handlePropfind(w, r)
		case "PROPPATCH":
			err = h.handleProppatch(w, r)
		case "MKCOL":
			err = h.Backend.Mkcol(r)
			if err == nil {
				w.WriteHeader(http.StatusCreated)
			}
		case "COPY", "MOVE":
			err = h.handleCopyMove(w, r)
		default:
			err = HTTPErrorf(http.StatusMethodNotAllowed, "webdav: unsupported method")
		}
	}

	if err != nil {
		ServeError(w, err)
	}
}

func (h *Handler) handleOptions(w http.ResponseWriter, r *http.Request) error {
	caps, allow, err := h.Backend.Options(r)
	if err != nil {
		return err
	}
	caps = append([]string{"1", "3"}, caps...)

	w.Header().Add("DAV", strings.Join(caps, ", "))
	w.Header().Add("Allow", strings.Join(allow, ", "))
	w.WriteHeader(http.StatusNoContent)
	return nil
}

func (h *Handler) handlePropfind(w http.ResponseWriter, r *http.Request) error {
	var propfind PropFind
	if IsRequestBodyEmpty(r) {
		// NOTE: properly handle PROPFIND requests without a body,
		// regardless of the "Content-Type" header of the request.
		propfind.AllProp = &struct{}{}
	} else if isContentXML(r.Header) {
		if err := DecodeXMLRequest(r, &propfind); err != nil {
			return err
		}
	} else {
		return HTTPErrorf(http.StatusBadRequest, "webdav: unsupported request body")
	}

	depth := DepthInfinity
	if s := r.Header.Get("Depth"); s != "" {
		var err error
		depth, err = ParseDepth(s)
		if err != nil {
			return &HTTPError{http.StatusBadRequest, err}
		}
	}

	ms, err := h.Backend.PropFind(r, &propfind, depth)
	if err != nil {
		return err
	}

	return ServeMultiStatus(w, ms)
}

type PropFindFunc func(raw *RawXMLValue) (interface{}, error)

func PropFindValue(value interface{}) PropFindFunc {
	return func(raw *RawXMLValue) (interface{}, error) {
		return value, nil
	}
}

// allPropNames lists the live properties defined in RFC 4918, which an
// allprop PROPFIND request returns (RFC 4918 section 14.2).
var allPropNames = map[xml.Name]bool{
	{Namespace, "creationdate"}:       true,
	{Namespace, "displayname"}:        true,
	{Namespace, "getcontentlanguage"}: true,
	{Namespace, "getcontentlength"}:   true,
	{Namespace, "getcontenttype"}:     true,
	{Namespace, "getetag"}:            true,
	{Namespace, "getlastmodified"}:    true,
	{Namespace, "lockdiscovery"}:      true,
	{Namespace, "resourcetype"}:       true,
	{Namespace, "supportedlock"}:      true,
}

// isAllProp reports whether an allprop PROPFIND request returns a property:
// the live properties defined in RFC 4918 are, and so are the others named in
// the include element. Other specifications, such as RFC 4791 and RFC 6352,
// ask for theirs not to be returned by allprop.
func isAllProp(name xml.Name, include *Include) bool {
	if allPropNames[name] {
		return true
	}
	if include != nil {
		for _, raw := range include.Raw {
			if n, ok := raw.XMLName(); ok && n == name {
				return true
			}
		}
	}
	return false
}

func NewPropFindResponse(path string, propfind *PropFind, props map[xml.Name]PropFindFunc) (*Response, error) {
	resp := &Response{Hrefs: []Href{Href{Path: path}}}

	if _, ok := props[ResourceTypeName]; !ok {
		props[ResourceTypeName] = PropFindValue(NewResourceType())
	}

	if propfind.PropName != nil {
		for xmlName, _ := range props {
			emptyVal := NewRawXMLElement(xmlName, nil, nil)
			if err := resp.EncodeProp(http.StatusOK, emptyVal); err != nil {
				return nil, err
			}
		}
	} else if propfind.AllProp != nil {
		for xmlName, f := range props {
			if !isAllProp(xmlName, propfind.Include) {
				continue
			}

			emptyVal := NewRawXMLElement(xmlName, nil, nil)

			val, err := f(emptyVal)

			code := http.StatusOK
			if err != nil {
				// TODO: don't throw away error message here
				code = HTTPErrorFromError(err).Code
				val = emptyVal
			}

			if err := resp.EncodeProp(code, val); err != nil {
				return nil, err
			}
		}
	} else if prop := propfind.Prop; prop != nil {
		for _, raw := range prop.Raw {
			xmlName, ok := raw.XMLName()
			if !ok {
				continue
			}

			emptyVal := NewRawXMLElement(xmlName, nil, nil)

			var code int
			var val interface{} = emptyVal
			f, ok := props[xmlName]
			if ok {
				if v, err := f(&raw); err != nil {
					// TODO: don't throw away error message here
					code = HTTPErrorFromError(err).Code
				} else {
					code = http.StatusOK
					val = v
				}
			} else {
				code = http.StatusNotFound
			}

			if err := resp.EncodeProp(code, val); err != nil {
				return nil, err
			}
		}
	} else {
		return nil, HTTPErrorf(http.StatusBadRequest, "webdav: request missing propname, allprop or prop element")
	}

	return resp, nil
}

// NewPropPatchResponse returns a response to a PROPPATCH request for path
// which reports every property named in update with the status code. It is
// meant for resources whose properties cannot be modified: the request still
// gets a multistatus naming each property, as RFC 4918 section 9.2 requires,
// rather than failing as a whole.
func NewPropPatchResponse(path string, update *PropertyUpdate, code int) (*Response, error) {
	refuse := func(*RawXMLValue, bool) error {
		return &HTTPError{Code: code}
	}
	return PatchProps(path, update, refuse, nil)
}

// PropPatchFunc processes a property of a PROPPATCH request: raw is the
// property element, to set or, if remove is true, to remove.
type PropPatchFunc func(raw *RawXMLValue, remove bool) error

// PatchProps returns the response to a PROPPATCH request for path, which is
// executed in full or not at all (RFC 4918 section 9.2).
//
// It calls f for each property of update, the removed ones first. If f fails
// for any of them, the response reports the status of each error, and 424
// Failed Dependency for the other properties. Otherwise it calls commit to
// apply the changes and the response reports 200 OK for every property; an
// error returned by commit fails the whole request.
func PatchProps(path string, update *PropertyUpdate, f PropPatchFunc, commit func() error) (*Response, error) {
	type instruction struct {
		name   xml.Name
		raw    *RawXMLValue
		remove bool
	}
	var instructions []instruction
	add := func(prop *Prop, remove bool) {
		for i := range prop.Raw {
			if name, ok := prop.Raw[i].XMLName(); ok {
				instructions = append(instructions, instruction{name, &prop.Raw[i], remove})
			}
		}
	}
	for i := range update.Remove {
		add(&update.Remove[i].Prop, true)
	}
	for i := range update.Set {
		add(&update.Set[i].Prop, false)
	}
	if len(instructions) == 0 {
		return nil, HTTPErrorf(http.StatusBadRequest, "webdav: request missing properties to update")
	}

	codes := make([]int, len(instructions))
	failed := false
	for i, ins := range instructions {
		codes[i] = http.StatusOK
		if err := f(ins.raw, ins.remove); err != nil {
			codes[i] = HTTPErrorFromError(err).Code
			failed = true
		}
	}

	if failed {
		for i := range codes {
			if codes[i] == http.StatusOK {
				codes[i] = http.StatusFailedDependency
			}
		}
	} else if err := commit(); err != nil {
		return nil, err
	}

	resp := &Response{Hrefs: []Href{{Path: path}}}
	for i, ins := range instructions {
		if err := resp.EncodeProp(codes[i], NewRawXMLElement(ins.name, nil, nil)); err != nil {
			return nil, err
		}
	}
	return resp, nil
}

func (h *Handler) handleProppatch(w http.ResponseWriter, r *http.Request) error {
	var update PropertyUpdate
	if err := DecodeXMLRequest(r, &update); err != nil {
		return err
	}

	resp, err := h.Backend.PropPatch(r, &update)
	if err != nil {
		return err
	}

	ms := NewMultiStatus(*resp)
	return ServeMultiStatus(w, ms)
}

func parseDestination(h http.Header) (*Href, error) {
	destHref := h.Get("Destination")
	if destHref == "" {
		return nil, HTTPErrorf(http.StatusBadRequest, "webdav: missing Destination header in MOVE request")
	}
	dest, err := url.Parse(destHref)
	if err != nil {
		return nil, HTTPErrorf(http.StatusBadRequest, "webdav: marlformed Destination header in MOVE request: %v", err)
	}
	return (*Href)(dest), nil
}

func (h *Handler) handleCopyMove(w http.ResponseWriter, r *http.Request) error {
	dest, err := parseDestination(r.Header)
	if err != nil {
		return err
	}

	overwrite := true
	if s := r.Header.Get("Overwrite"); s != "" {
		overwrite, err = ParseOverwrite(s)
		if err != nil {
			return err
		}
	}

	depth := DepthInfinity
	if s := r.Header.Get("Depth"); s != "" {
		depth, err = ParseDepth(s)
		if err != nil {
			return err
		}
	}

	var created bool
	if r.Method == "COPY" {
		var recursive bool
		switch depth {
		case DepthZero:
			recursive = false
		case DepthOne:
			return HTTPErrorf(http.StatusBadRequest, `webdav: "Depth: 1" is not supported in COPY request`)
		case DepthInfinity:
			recursive = true
		}

		created, err = h.Backend.Copy(r, dest, recursive, overwrite)
	} else {
		if depth != DepthInfinity {
			return HTTPErrorf(http.StatusBadRequest, `webdav: only "Depth: infinity" is accepted in MOVE request`)
		}
		created, err = h.Backend.Move(r, dest, overwrite)
	}
	if err != nil {
		return err
	}

	if created {
		w.WriteHeader(http.StatusCreated)
	} else {
		w.WriteHeader(http.StatusNoContent)
	}
	return nil
}
