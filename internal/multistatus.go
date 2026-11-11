package internal

import (
	"encoding/xml"
	"errors"
	"net/http"
)

var multiStatusName = xml.Name{Space: Namespace, Local: "multistatus"}

var errMultiStatusClosed = errors.New("webdav: multistatus already closed")

// MultiStatusWriter writes a 207 Multi-Status response (RFC 4918 section 13)
// one DAV:response element at a time, so that each response is sent as soon
// as it's built, rather than once all of them are, and doesn't need to be
// kept in memory until then.
//
// Nothing is written until the first call to WriteResponse or Close, so that
// the request can still fail with another status until then (see Started).
// Each response is encoded straight to the http.ResponseWriter, whose own
// buffering decides when the bytes leave the server. The encoding is the same
// as the one of a whole MultiStatus with the same responses.
//
// Once a write fails, the writer doesn't write anything more, and returns the
// error again.
type MultiStatusWriter struct {
	// ResponseDescription and SyncToken are written by Close, after the
	// responses, unless they're empty.
	ResponseDescription string
	SyncToken           string

	w      http.ResponseWriter
	enc    *xml.Encoder // set once the 207 status is written
	closed bool
	err    error
}

// NewMultiStatusWriter returns a MultiStatusWriter writing to w.
func NewMultiStatusWriter(w http.ResponseWriter) *MultiStatusWriter {
	return &MultiStatusWriter{w: w}
}

// Started reports whether the 207 status has been written, after which the
// request can't fail with another status anymore.
func (mw *MultiStatusWriter) Started() bool {
	return mw.enc != nil
}

// begin writes the 207 status and the start of the multistatus element,
// unless they've already been written.
func (mw *MultiStatusWriter) begin() error {
	if mw.err != nil {
		return mw.err
	} else if mw.closed {
		return errMultiStatusClosed
	} else if mw.enc != nil {
		return nil
	}
	mw.w.WriteHeader(http.StatusMultiStatus)
	mw.enc = ServeXML(mw.w)
	return mw.fail(mw.enc.EncodeToken(xml.StartElement{Name: multiStatusName}))
}

// fail records err, if any, and returns it.
func (mw *MultiStatusWriter) fail(err error) error {
	if err != nil && mw.err == nil {
		mw.err = err
	}
	return err
}

// WriteResponse writes resp. The first call writes the 207 status and the
// start of the multistatus element first.
func (mw *MultiStatusWriter) WriteResponse(resp *Response) error {
	if err := mw.begin(); err != nil {
		return err
	}
	// Encode flushes its buffer to the http.ResponseWriter
	return mw.fail(mw.enc.Encode(resp))
}

// WriteResponses writes each of resps, as WriteResponse does.
func (mw *MultiStatusWriter) WriteResponses(resps []Response) error {
	for i := range resps {
		if err := mw.WriteResponse(&resps[i]); err != nil {
			return err
		}
	}
	return nil
}

// Close writes the end of the multistatus: the response description and the
// sync token, and the end of the multistatus element. If no response was
// written, it writes the 207 status and the start of the multistatus element
// first. Calling Close again does nothing.
func (mw *MultiStatusWriter) Close() error {
	if mw.closed {
		return mw.err
	}
	if err := mw.begin(); err != nil {
		return err
	}
	mw.closed = true

	// As for a whole MultiStatus, these elements get no xmlns attribute:
	// they're in the default DAV: namespace of the multistatus element
	for _, elt := range []struct{ name, value string }{
		{"responsedescription", mw.ResponseDescription},
		{"sync-token", mw.SyncToken},
	} {
		if elt.value == "" {
			continue
		}
		start := xml.StartElement{Name: xml.Name{Local: elt.name}}
		if err := mw.enc.EncodeElement(elt.value, start); err != nil {
			return mw.fail(err)
		}
	}
	if err := mw.enc.EncodeToken(xml.EndElement{Name: multiStatusName}); err != nil {
		return mw.fail(err)
	}
	return mw.fail(mw.enc.Flush())
}

// StreamMultiStatus serves a 207 Multi-Status response: f writes the
// responses to mw as they're built, and StreamMultiStatus closes mw. f must
// not close mw itself.
//
// If f fails before writing any response, StreamMultiStatus returns its
// error, to be served as the status of the request as usual.
//
// Once a response has been written, the 207 status has been sent and an error
// can't change it anymore. Nothing more is written then: the multistatus
// element is left unterminated, so that the client can't take the responses
// it got for a complete result, which it could act upon, e.g. by deleting
// its copy of the resources missing from a listing. Under an http.Server,
// the response is also aborted with http.ErrAbortHandler, as by
// httputil.ReverseProxy, so that the client gets an incomplete HTTP message:
// the connection is closed before the end of the body, or the HTTP/2 stream
// is reset. Otherwise, e.g. when ServeHTTP is called with an
// httptest.ResponseRecorder, StreamMultiStatus returns nil.
func StreamMultiStatus(w http.ResponseWriter, r *http.Request, f func(mw *MultiStatusWriter) error) error {
	mw := NewMultiStatusWriter(w)
	err := f(mw)
	if err == nil {
		err = mw.Close()
	}
	if err == nil || !mw.Started() {
		return err
	}
	// The http.Server recovers this panic, and doesn't log it
	if r.Context().Value(http.ServerContextKey) != nil {
		panic(http.ErrAbortHandler)
	}
	return nil
}
