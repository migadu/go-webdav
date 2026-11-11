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

func IsRequestBodyEmpty(r *http.Request) bool {
	_, err := r.Body.Read(nil)
	return err == io.EOF
}

// acceptedContentCodings lists the request content codings supported by
// DecodeRequestBody, besides "identity".
const acceptedContentCodings = "gzip, deflate"

const (
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
// (RFC 7694 section 3).
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

// countingBody counts the bytes read from a request body.
type countingBody struct {
	io.ReadCloser
	n int64
}

func (c *countingBody) Read(p []byte) (int, error) {
	n, err := c.ReadCloser.Read(p)
	c.n += int64(n)
	return n, err
}

// decodedBody reads the decoded request body and closes the original one.
//
// It fails with an HTTP 413 error once more than minDecodedBodyLimit bytes and
// more than maxDecodedBodyRatio times the number of bytes read from the
// original body have been decoded.
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
		// TODO: add support for propfind.Include
		for xmlName, f := range props {
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
