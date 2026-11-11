package internal

import (
	"bufio"
	"compress/gzip"
	"compress/zlib"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"net/url"
	"path"
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
		// The header fields must be set before the status is written
		w.Header().Set("Content-Type", xmlContentType)
		w.WriteHeader(code)
		w.Write([]byte(xml.Header))
		xml.NewEncoder(w).Encode(errElt)
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
		// Keep the status of errors returned while reading the body, e.g.
		// HTTP 413 for a decompression bomb
		var httpErr *HTTPError
		if errors.As(err, &httpErr) {
			return err
		}
		return &HTTPError{http.StatusBadRequest, err}
	}
	return nil
}

// IsRequestBodyEmpty reports whether the body of r is empty, or missing.
//
// It finds out by reading the first byte of the body, since a read of zero
// bytes tells nothing: http.MaxBytesReader, for one, answers it without
// reading anything. It then replaces r.Body with a body which returns that
// byte again, followed by the rest of the original body, so that the whole
// body can still be read from r.Body. A body which fails to read isn't
// empty: reading it from r.Body fails with the same error.
func IsRequestBodyEmpty(r *http.Request) bool {
	switch body := r.Body.(type) {
	case nil:
		return true
	case *peekedBody:
		// Already peeked at
		return false
	default:
		if body == http.NoBody {
			return true
		}
	}

	var b [1]byte
	n, err := io.ReadFull(r.Body, b[:])
	if err == io.EOF {
		return true
	}
	r.Body = &peekedBody{ReadCloser: r.Body, b: b[:n], err: err}
	return false
}

// peekedBody is a request body whose first byte, or the error reading it, was
// read by IsRequestBodyEmpty, and is returned again.
type peekedBody struct {
	io.ReadCloser // original body
	b             []byte
	err           error
}

func (pb *peekedBody) Read(p []byte) (int, error) {
	if len(pb.b) > 0 {
		n := copy(p, pb.b)
		pb.b = pb.b[n:]
		return n, nil
	} else if pb.err != nil {
		return 0, pb.err
	}
	return pb.ReadCloser.Read(p)
}

// acceptedContentCodings lists the request content codings supported by
// DecodeRequestBody, besides "identity".
const acceptedContentCodings = "gzip, deflate"

const (
	// maxContentCodings is the maximum number of content codings, besides
	// "identity", applied to a request body. Each one adds a decoder to the
	// chain reading the body, so a header listing thousands of them would
	// exhaust memory and the stack.
	maxContentCodings = 4
	// minDecodedBodyLimit is the decoded request body size which is always
	// allowed, regardless of its compression ratio.
	minDecodedBodyLimit = 1 << 20 // 1 MiB
	// maxDecodedBodyRatio is the maximum ratio between the size of a decoded
	// request body and the number of encoded bytes read to produce it.
	maxDecodedBodyRatio = 100
)

// DecodeRequestBody undoes the content codings listed in the Content-Encoding
// header of a request (RFC 9110 section 8.4), so that the request body can be
// read as-is.
//
// The returned request is a shallow copy of r without the Content-Encoding
// header. If the body needs to be decoded, the Content-Length header is
// dropped as well, since it refers to the encoded body. If r has no
// Content-Encoding header, r itself is returned.
//
// If a content coding isn't supported, an HTTP 415 error is returned and an
// Accept-Encoding header listing the supported content codings is added to w
// (RFC 7694 section 3). More than 4 content codings besides "identity" are
// refused with an HTTP 415 error as well.
//
// Reading the decoded body fails with an HTTP 400 error if the encoded data is
// malformed, truncated or fails its checksum. Errors reading the encoded body
// itself, e.g. from http.MaxBytesReader, are returned as is.
//
// To defuse decompression bombs, reading the decoded body fails with an HTTP
// 413 error once it is larger than both 1 MiB and 100 times the number of
// bytes read so far from the encoded body. A limit on the size of the encoded
// body, e.g. set with http.MaxBytesReader, thus also bounds the decoded body.
func DecodeRequestBody(w http.ResponseWriter, r *http.Request) (*http.Request, error) {
	values := r.Header.Values("Content-Encoding")
	if len(values) == 0 {
		return r, nil
	}

	var codings []string
	for _, v := range values {
		for _, coding := range strings.Split(v, ",") {
			// Content codings are case-insensitive (RFC 9110 section 8.4.1)
			coding = strings.ToLower(strings.TrimSpace(coding))
			switch coding {
			case "", "identity":
				// Nothing to decode
			case "gzip", "x-gzip", "deflate":
				if len(codings) == maxContentCodings {
					return r, HTTPErrorf(http.StatusUnsupportedMediaType, "webdav: more than %d content codings in Content-Encoding", maxContentCodings)
				}
				codings = append(codings, coding)
			default:
				w.Header().Set("Accept-Encoding", acceptedContentCodings)
				return r, HTTPErrorf(http.StatusUnsupportedMediaType, "webdav: unsupported Content-Encoding %q", coding)
			}
		}
	}

	r2 := new(http.Request)
	*r2 = *r
	r2.Header = r.Header.Clone()
	r2.Header.Del("Content-Encoding")
	if len(codings) == 0 {
		return r2, nil
	}

	src := &countingBody{ReadCloser: r.Body}
	if src.ReadCloser == nil {
		src.ReadCloser = http.NoBody
	}
	var body io.Reader = src
	// Codings are listed in the order in which they were applied, so they
	// have to be undone in reverse order
	for i := len(codings) - 1; i >= 0; i-- {
		body = &contentDecoder{coding: codings[i], r: body}
	}
	r2.Body = &decodedBody{r: body, src: src}
	r2.ContentLength = -1
	r2.Header.Del("Content-Length")
	return r2, nil
}

// contentDecoder decodes a content coding. It doesn't read anything until the
// first Read call, so that handlers can reject a request before consuming its
// body.
type contentDecoder struct {
	coding string
	r      io.Reader
	dec    io.Reader
	err    error
}

func (d *contentDecoder) Read(p []byte) (int, error) {
	if d.dec == nil && d.err == nil {
		d.dec, d.err = newContentDecoder(d.coding, d.r)
	}
	if d.err != nil {
		return 0, d.err
	}
	return d.dec.Read(p)
}

func newContentDecoder(coding string, r io.Reader) (io.Reader, error) {
	br := bufio.NewReader(r)
	// Leave an empty body empty, even though it's not valid encoded data, so
	// that e.g. a PROPFIND request without a body is still accepted
	if _, err := br.Peek(1); err != nil {
		return nil, err
	}

	switch coding {
	case "gzip", "x-gzip":
		return gzip.NewReader(br)
	case "deflate":
		// "deflate" is the zlib format (RFC 9110 section 8.4.1.2)
		return zlib.NewReader(br)
	default:
		panic(fmt.Sprintf("webdav: unexpected content coding %q", coding))
	}
}

// countingBody counts the bytes read from a request body, and records the
// error reading it failed with, if any.
type countingBody struct {
	io.ReadCloser
	n   int64
	err error
}

func (c *countingBody) Read(p []byte) (int, error) {
	n, err := c.ReadCloser.Read(p)
	c.n += int64(n)
	if err != nil && err != io.EOF {
		c.err = err
	}
	return n, err
}

// decodedBody reads the decoded request body and closes the original one.
//
// It fails with an HTTP 413 error once more than minDecodedBodyLimit bytes and
// more than maxDecodedBodyRatio times the number of bytes read from the
// original body have been decoded, and with an HTTP 400 error if the encoded
// data is malformed. Errors reading the original body are returned as is.
type decodedBody struct {
	r   io.Reader     // decoded body
	src *countingBody // original body
	n   int64         // number of decoded bytes
	err error
}

func (b *decodedBody) Read(p []byte) (int, error) {
	if b.err != nil {
		return 0, b.err
	}

	n, err := b.r.Read(p)
	b.n += int64(n)
	if err != nil && err != io.EOF {
		if b.src.err != nil {
			// Reading the encoded body failed, e.g. because it's larger than
			// allowed: return that error as is
			err = b.src.err
		} else {
			// The encoded body is malformed, truncated or fails its checksum
			err = &HTTPError{Code: http.StatusBadRequest, Err: fmt.Errorf("webdav: malformed encoded request body: %w", err)}
		}
		b.err = err
		return n, err
	}
	limit := maxDecodedBodyRatio * b.src.n
	if limit < minDecodedBodyLimit {
		limit = minDecodedBodyLimit
	}
	if b.n > limit {
		b.err = HTTPErrorf(http.StatusRequestEntityTooLarge, "webdav: decoded request body too large (compression ratio over %d:1)", maxDecodedBodyRatio)
		return 0, b.err
	}
	return n, err
}

func (b *decodedBody) Close() error {
	return b.src.Close()
}

// xmlContentType is the media type of XML responses.
const xmlContentType = "application/xml; charset=\"utf-8\""

func ServeXML(w http.ResponseWriter) *xml.Encoder {
	w.Header().Set("Content-Type", xmlContentType)
	w.Write([]byte(xml.Header))
	return xml.NewEncoder(w)
}

// ServeMultiStatus serves a 207 Multi-Status response holding ms. Responses
// built one after the other are better served with StreamMultiStatus.
func ServeMultiStatus(w http.ResponseWriter, ms *MultiStatus) error {
	mw := NewMultiStatusWriter(w)
	mw.ResponseDescription = ms.ResponseDescription
	mw.SyncToken = ms.SyncToken
	if err := mw.WriteResponses(ms.Responses); err != nil {
		return err
	}
	return mw.Close()
}

// HrefErrorResponses splits err, an error a backend returned along with
// partial results, into per-resource error responses.
//
// If err is an *HrefError, or a multi-error (as built by errors.Join, or
// fmt.Errorf with several %w verbs) made only of *HrefErrors, possibly
// nested, the request partially succeeded: HrefErrorResponses returns one
// error response per *HrefError, built by NewErrorResponse, and a nil error.
// Otherwise err is a failure of the whole request, and is returned as is;
// this includes an *HrefError wrapped in another error.
func HrefErrorResponses(err error) ([]Response, error) {
	if err == nil {
		return nil, nil
	}
	hrefErrs := splitHrefErrors(err)
	if len(hrefErrs) == 0 {
		return nil, err
	}
	resps := make([]Response, len(hrefErrs))
	for i, hrefErr := range hrefErrs {
		resp := NewErrorResponse(hrefErr.Href.Path, hrefErr.Err)
		resp.Hrefs = []Href{Href(hrefErr.Href)}
		resps[i] = *resp
	}
	return resps, nil
}

// splitHrefErrors returns the *HrefErrors err is made of, or nil if any part
// of err is something else.
func splitHrefErrors(err error) []*HrefError {
	switch err := err.(type) {
	case *HrefError:
		if err == nil {
			return nil
		}
		return []*HrefError{err}
	case interface{ Unwrap() []error }:
		var l []*HrefError
		for _, e := range err.Unwrap() {
			if e == nil {
				continue
			}
			sub := splitHrefErrors(e)
			if sub == nil {
				return nil
			}
			l = append(l, sub...)
		}
		return l
	default:
		return nil
	}
}

// serveMemberErrors handles err, the error returned by the backend for a
// DELETE, COPY or MOVE request. If the request failed only for some members
// of the target collection, as reported by *HrefErrors (see
// HrefErrorResponses), these members are listed in a 207 Multi-Status
// response (RFC 4918 sections 9.6.1, 9.8.5 and 9.9.4). Members processed
// successfully aren't listed, and neither are their ancestors with a 424
// Failed Dependency.
//
// Otherwise, including when the only failed resources are the request target
// itself or, for COPY and MOVE, the destination, given in paths, err is
// returned to be served as the status of the whole request.
func serveMemberErrors(w http.ResponseWriter, err error, paths ...string) error {
	resps, plainErr := HrefErrorResponses(err)
	if plainErr != nil || onlyHrefs(resps, paths) {
		return err
	}
	return ServeMultiStatus(w, NewMultiStatus(resps...))
}

// onlyHrefs reports whether all responses are about the resources at paths.
func onlyHrefs(resps []Response, paths []string) bool {
	for _, resp := range resps {
		for _, href := range resp.Hrefs {
			if !containsPath(paths, href.Path) {
				return false
			}
		}
	}
	return true
}

// containsPath reports whether paths contains p, as compared by SamePath.
func containsPath(paths []string, p string) bool {
	for _, path := range paths {
		if SamePath(path, p) {
			return true
		}
	}
	return false
}

// Backend is a WebDAV server backend.
//
// Delete, Copy and Move can report the members of the target collection that
// failed while the others succeeded with *HrefErrors (see
// HrefErrorResponses): Handler then replies with a 207 Multi-Status response.
//
// PropFind writes the responses to mw as it builds them. An error it returns
// once it has written a response aborts the response (see
// StreamMultiStatus).
type Backend interface {
	Options(r *http.Request) (caps []string, allow []string, err error)
	HeadGet(w http.ResponseWriter, r *http.Request) error
	PropFind(r *http.Request, pf *PropFind, depth Depth, mw *MultiStatusWriter) error
	PropPatch(r *http.Request, pu *PropertyUpdate) (*Response, error)
	Put(w http.ResponseWriter, r *http.Request) error
	Delete(r *http.Request) error
	Mkcol(r *http.Request) error
	Copy(r *http.Request, dest *Href, recursive, overwrite bool) (created bool, err error)
	Move(r *http.Request, dest *Href, overwrite bool) (created bool, err error)
}

// LockBackend is a Backend supporting locks (RFC 4918 compliance class 2).
// Handler replies to LOCK and UNLOCK requests with 405 Method Not Allowed if
// its Backend isn't a LockBackend.
type LockBackend interface {
	Backend
	// Lock creates or refreshes a lock on the resource at the Request-URI.
	// It returns the token of the lock, the lockdiscovery property of the
	// resource, and whether the resource was created. Members of a
	// collection preventing a lock from being granted can be reported with
	// *HrefErrors, which Handler serves in a 207 Multi-Status response.
	Lock(r *http.Request, req *LockRequest) (token string, discovery *LockDiscovery, created bool, err error)
	// Unlock removes the lock with the given token.
	Unlock(r *http.Request, token string) error
}

// LockRequest is a LOCK request (RFC 4918 section 9.10).
type LockRequest struct {
	// Info describes the lock to create, nil for a refresh
	Info *LockInfo
	// Depth is the depth of the lock to create, 0 or infinity
	Depth Depth
	// Timeout is the timeout requested by the client, nil if none
	Timeout *Timeout
	// RefreshToken is the token of the lock to refresh
	RefreshToken string
}

type Handler struct {
	Backend Backend
}

func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	r, err := DecodeRequestBody(w, r)
	if err != nil {
		ServeError(w, err)
		return
	}

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
			err = h.Backend.Delete(r)
			if err == nil {
				w.WriteHeader(http.StatusNoContent)
			} else {
				err = serveMemberErrors(w, err, r.URL.Path)
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
		case "LOCK", "UNLOCK":
			lb, ok := h.Backend.(LockBackend)
			if !ok {
				err = HTTPErrorf(http.StatusMethodNotAllowed, "webdav: unsupported method")
			} else if r.Method == "LOCK" {
				err = h.handleLock(w, r, lb)
			} else {
				err = h.handleUnlock(w, r, lb)
			}
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
	// Check it before the backend looks for resources: otherwise the
	// request fails only if one is found
	if err := CheckPropFind(&propfind); err != nil {
		return err
	}

	depth := DepthInfinity
	if s := r.Header.Get("Depth"); s != "" {
		var err error
		depth, err = ParseDepth(s)
		if err != nil {
			return &HTTPError{http.StatusBadRequest, err}
		}
	}

	return StreamMultiStatus(w, r, func(mw *MultiStatusWriter) error {
		return h.Backend.PropFind(r, &propfind, depth, mw)
	})
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

// CheckPropFind returns the error NewPropFindResponse fails with for a request
// which doesn't ask for any properties: one with neither a propname, an
// allprop nor a prop element.
func CheckPropFind(propfind *PropFind) error {
	if propfind.PropName == nil && propfind.AllProp == nil && propfind.Prop == nil {
		return HTTPErrorf(http.StatusBadRequest, "webdav: request missing propname, allprop or prop element")
	}
	return nil
}

func NewPropFindResponse(path string, propfind *PropFind, props map[xml.Name]PropFindFunc) (*Response, error) {
	return NewPropFindResponseWithDeadProps(path, propfind, props, nil)
}

// NewPropFindResponseWithDeadProps is like NewPropFindResponse, for a resource
// which also has the dead properties deadProps. Unlike the live properties of
// props, an allprop request returns all of them (RFC 4918 section 9.1). A
// live property takes precedence over a dead property of the same name.
func NewPropFindResponseWithDeadProps(path string, propfind *PropFind, props, deadProps map[xml.Name]PropFindFunc) (*Response, error) {
	resp := &Response{Hrefs: []Href{Href{Path: path}}}

	if _, ok := props[ResourceTypeName]; !ok {
		props[ResourceTypeName] = PropFindValue(NewResourceType())
	}

	dead := make(map[xml.Name]bool, len(deadProps))
	for name, f := range deadProps {
		if _, ok := props[name]; !ok {
			props[name] = f
			dead[name] = true
		}
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
			if !dead[xmlName] && !isAllProp(xmlName, propfind.Include) {
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
		return nil, CheckPropFind(propfind)
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
// It calls f for each property of update in document order, so that a later
// instruction for a property overrides an earlier one; for an update without
// Instructions, built by hand rather than decoded, the removed properties come
// first. If f fails for any of them, the response reports the status of each
// error, along with the DAV:error element it wraps if any, and 424 Failed
// Dependency for the other properties. Otherwise it calls commit to apply the
// changes and the response reports 200 OK for every property; an error
// returned by commit fails the whole request. A property named by several
// instructions is reported once, with the status of the first one which
// failed.
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
	if len(update.Instructions) > 0 {
		for i := range update.Instructions {
			add(&update.Instructions[i].Prop, update.Instructions[i].Remove)
		}
	} else {
		for i := range update.Remove {
			add(&update.Remove[i].Prop, true)
		}
		for i := range update.Set {
			add(&update.Set[i].Prop, false)
		}
	}
	if len(instructions) == 0 {
		return nil, HTTPErrorf(http.StatusBadRequest, "webdav: request missing properties to update")
	}

	var names []xml.Name
	codes := make(map[xml.Name]int)
	errElts := make(map[xml.Name]*Error)
	failed := false
	for _, ins := range instructions {
		code := http.StatusOK
		var errElt *Error
		if err := f(ins.raw, ins.remove); err != nil {
			code = HTTPErrorFromError(err).Code
			errors.As(err, &errElt)
			failed = true
		}
		prev, seen := codes[ins.name]
		if !seen {
			names = append(names, ins.name)
		}
		if !seen || prev == http.StatusOK {
			codes[ins.name] = code
			errElts[ins.name] = errElt
		}
	}

	if failed {
		for name, code := range codes {
			if code == http.StatusOK {
				codes[name] = http.StatusFailedDependency
			}
		}
	} else if err := commit(); err != nil {
		return nil, err
	}

	resp := &Response{Hrefs: []Href{{Path: path}}}
	var errPropStats []PropStat
	for _, name := range names {
		prop := NewRawXMLElement(name, nil, nil)
		if errElt := errElts[name]; errElt != nil {
			// A DAV:error element describes all of the properties of its
			// propstat element: give this property its own
			errPropStats = append(errPropStats, PropStat{
				Prop:   Prop{Raw: []RawXMLValue{*prop}},
				Status: Status{Code: codes[name]},
				Error:  errElt,
			})
		} else if err := resp.EncodeProp(codes[name], prop); err != nil {
			return nil, err
		}
	}
	resp.PropStats = append(resp.PropStats, errPropStats...)
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
	// The destination path is the one the backend acts upon, whatever its
	// host. Copying or moving a resource onto itself would first delete it to
	// overwrite it (RFC 4918 sections 9.8.5 and 9.9.4).
	if path.Clean(dest.Path) == path.Clean(r.URL.Path) {
		return HTTPErrorf(http.StatusForbidden, "webdav: source and destination are the same resource")
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
		return serveMemberErrors(w, err, r.URL.Path, dest.Path)
	}

	if created {
		w.WriteHeader(http.StatusCreated)
	} else {
		w.WriteHeader(http.StatusNoContent)
	}
	return nil
}

// handleLock handles a LOCK request (RFC 4918 section 9.10): a request with
// a lockinfo body creates a lock, a request without body refreshes the lock
// named in the If header field.
func (h *Handler) handleLock(w http.ResponseWriter, r *http.Request, b LockBackend) error {
	req := LockRequest{Depth: DepthInfinity}
	if IsRequestBodyEmpty(r) {
		token, err := ParseSubmittedToken(r.Header)
		if err != nil {
			return err
		} else if token == "" {
			return HTTPErrorf(http.StatusBadRequest, "webdav: a LOCK request without body must name the lock to refresh in the If header field")
		}
		req.RefreshToken = token
	} else {
		// Some clients don't label the body
		if r.Header.Get("Content-Type") != "" && !isContentXML(r.Header) {
			return HTTPErrorf(http.StatusBadRequest, "webdav: expected application/xml request")
		}
		var info LockInfo
		if err := xml.NewDecoder(r.Body).Decode(&info); err != nil {
			return &HTTPError{http.StatusBadRequest, err}
		}
		if (info.LockScope.Exclusive == nil) == (info.LockScope.Shared == nil) {
			return HTTPErrorf(http.StatusBadRequest, "webdav: the lock scope must be either exclusive or shared")
		}
		if info.LockType.Write == nil {
			return HTTPErrorf(http.StatusBadRequest, "webdav: only write locks are supported")
		}
		req.Info = &info

		// The Depth header field of a refresh is ignored (RFC 4918 section
		// 9.10.2)
		if s := r.Header.Get("Depth"); s != "" {
			depth, err := ParseDepth(s)
			if err != nil {
				return &HTTPError{http.StatusBadRequest, err}
			} else if depth == DepthOne {
				return HTTPErrorf(http.StatusBadRequest, `webdav: "Depth: 1" is not allowed in LOCK requests`)
			}
			req.Depth = depth
		}
	}

	// The server needn't honour the Timeout header field (RFC 4918 section
	// 10.7), so an invalid one is ignored
	if s := r.Header.Get("Timeout"); s != "" {
		if timeout, err := ParseTimeout(s); err == nil {
			req.Timeout = &timeout
		}
	}

	token, discovery, created, err := b.Lock(r, &req)
	if err != nil {
		// A lock which can't be granted because of members of the
		// collection is refused with a multi-status response (RFC 4918
		// section 9.10.9)
		return serveMemberErrors(w, err, r.URL.Path)
	}
	prop, err := EncodeProp(discovery)
	if err != nil {
		return err
	}

	w.Header().Set("Content-Type", xmlContentType)
	if req.Info != nil {
		w.Header().Set("Lock-Token", FormatLockToken(token))
	}
	if created {
		w.WriteHeader(http.StatusCreated)
	} else {
		w.WriteHeader(http.StatusOK)
	}
	w.Write([]byte(xml.Header))
	return xml.NewEncoder(w).Encode(prop)
}

func (h *Handler) handleUnlock(w http.ResponseWriter, r *http.Request, b LockBackend) error {
	tokenHref, err := ParseLockToken(r.Header.Get("Lock-Token"))
	if err != nil {
		return &HTTPError{http.StatusBadRequest, err}
	}

	if err := b.Unlock(r, tokenHref); err != nil {
		return err
	}

	w.WriteHeader(http.StatusNoContent)
	return nil
}
