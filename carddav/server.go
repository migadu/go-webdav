package carddav

import (
	"bytes"
	"context"
	"encoding/xml"
	"errors"
	"fmt"
	"mime"
	"net/http"
	"path"
	"slices"
	"strconv"
	"strings"

	"github.com/emersion/go-vcard"
	"github.com/emersion/go-webdav"
	"github.com/emersion/go-webdav/internal"
	"github.com/emersion/go-webdav/internal/push"
)

type PutAddressObjectOptions struct {
	// IfNoneMatch indicates that the client does not want to overwrite
	// an existing resource. Check it with MatchETagWeak.
	IfNoneMatch webdav.ConditionalMatch
	// IfMatch provides the ETag of the resource that the client intends
	// to overwrite, can be "". Check it with MatchETag.
	IfMatch webdav.ConditionalMatch
}

// Backend is a CardDAV server backend.
//
// ListAddressBooks, ListAddressObjects and QueryAddressObjects can report
// resources that couldn't be loaded along with the others, and
// DeleteAddressBook can report the address objects it couldn't delete, by
// returning an error made only of errors created by webdav.NewHrefError (a
// single one, or several combined with errors.Join). The server then replies
// with a 207 Multi-Status response listing each failed resource with its own
// status, instead of failing the whole request. Any other error fails the
// whole request.
//
// The responses to PROPFIND and REPORT requests are streamed: each resource
// is written to the client as soon as it's built. An error failing the whole
// request once some resources have been written, such as ListAddressObjects
// failing for an address book of a Depth: infinity PROPFIND after the
// previous address books were listed, can't change the status of the
// response anymore: the response is aborted instead, so that the client sees
// it's incomplete.
type Backend interface {
	AddressBookHomeSetPath(ctx context.Context) (string, error)
	ListAddressBooks(ctx context.Context) ([]AddressBook, error)
	GetAddressBook(ctx context.Context, path string) (*AddressBook, error)
	CreateAddressBook(ctx context.Context, addressBook *AddressBook) error
	// DeleteAddressBook deletes an address book and its address objects.
	// Address objects that couldn't be deleted can be reported with
	// webdav.NewHrefError; as per RFC 4918 section 9.6.1, the address
	// objects that were deleted shouldn't be reported.
	DeleteAddressBook(ctx context.Context, path string) error
	GetAddressObject(ctx context.Context, path string, req *AddressDataRequest) (*AddressObject, error)
	ListAddressObjects(ctx context.Context, path string, req *AddressDataRequest) ([]AddressObject, error)
	QueryAddressObjects(ctx context.Context, path string, query *AddressBookQuery) ([]AddressObject, error)
	PutAddressObject(ctx context.Context, path string, card vcard.Card, opts *PutAddressObjectOptions) (*AddressObject, error)
	DeleteAddressObject(ctx context.Context, path string) error

	webdav.UserPrincipalBackend
}

// AddressBookUpdater is an optional interface a Backend can implement to let
// clients modify the properties of address books with PROPPATCH.
type AddressBookUpdater interface {
	// UpdateAddressBook applies all of update to the address book at path,
	// or none of it.
	UpdateAddressBook(ctx context.Context, path string, update *AddressBookUpdate) error
}

// CopyAddressObjectOptions holds the options of a COPY request for an address
// object.
type CopyAddressObjectOptions struct {
	// NoOverwrite indicates that the client doesn't want an address object
	// already at the destination to be replaced ("Overwrite: F"): the copy
	// must then fail with 412 Precondition Failed.
	NoOverwrite bool
}

// MoveAddressObjectOptions holds the options of a MOVE request for an address
// object.
type MoveAddressObjectOptions struct {
	// NoOverwrite indicates that the client doesn't want an address object
	// already at the destination to be replaced ("Overwrite: F"): the move
	// must then fail with 412 Precondition Failed.
	NoOverwrite bool
}

// AddressObjectCopyMover is an optional interface a Backend can implement to
// let clients copy and move address objects with COPY and MOVE, for instance
// to move a contact from an address book to another. Address books cannot be
// copied or moved.
//
// The Handler calls these methods only for src and dst paths of address
// objects, that is, of resources in an address book, and dst is never src.
// created is true if there was no address object at dst, false if one was
// replaced. If there is no address object at src, they fail with 404 Not
// Found, and if there is no address book to hold dst, with 409 Conflict (RFC
// 4918 sections 9.8.5 and 9.9.4).
//
// The preconditions of RFC 6352 section 6.3.2.1 apply to the destination, and
// can be reported with NewPreconditionError: for instance no-uid-conflict, if
// the address book of dst already holds another address object with the same
// UID, or if dst is an address object with another UID.
type AddressObjectCopyMover interface {
	// CopyAddressObject copies the address object at src to dst.
	CopyAddressObject(ctx context.Context, src, dst string, opts *CopyAddressObjectOptions) (created bool, err error)
	// MoveAddressObject moves the address object at src to dst.
	MoveAddressObject(ctx context.Context, src, dst string, opts *MoveAddressObjectOptions) (created bool, err error)
}

// MeCardBackend is an optional interface a Backend can implement to report
// which address object holds the current user's own contact, the "me card" of
// Apple's address book clients. The Handler serves it as the CalendarServer
// me-card property of the address book home set, where these clients look
// for it (see https://sabre.io/dav/carddav-me-card/), to the PROPFIND
// requests naming the property: it isn't returned for allprop. The property
// can't be modified with PROPPATCH.
type MeCardBackend interface {
	// MeCardPath returns the path of the address object holding the current
	// user's own contact, or an empty string if there's none.
	MeCardPath(ctx context.Context) (string, error)
}

// Handler handles CardDAV HTTP requests. It can be used to create a CardDAV
// server.
//
// The principal, the address book home set, the address books and the
// address objects report the access control properties of RFC 3744 section 5
// clients ask for: the current user principal is their DAV:owner, the
// collection holding it their DAV:principal-collection-set, and
// DAV:supported-privilege-set describes the privileges reported in
// DAV:current-user-privilege-set, which the ReadOnly fields control. DAV:acl
// isn't reported, and can't be modified with the ACL method.
type Handler struct {
	Backend Backend
	Prefix  string
}

func (h *Handler) newBackend() backend {
	p := h.Prefix
	if p != "" {
		p = path.Clean(p)
		if !strings.HasPrefix(p, "/") {
			p = "/" + p
		}
		p = strings.TrimSuffix(p, "/")
	}
	return backend{
		Backend: h.Backend,
		Prefix:  p,
	}
}

// ServeHTTP implements http.Handler.
func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if h.Backend == nil {
		http.Error(w, "carddav: no backend available", http.StatusInternalServerError)
		return
	}

	b := h.newBackend()
	if internal.SamePath(r.URL.Path, "/.well-known/carddav") {
		principalPath, err := h.Backend.CurrentUserPrincipal(r.Context())
		if err != nil {
			// An HTTPError keeps its status: the server may require
			// authentication here (RFC 6764 section 5)
			internal.ServeError(w, fmt.Errorf("carddav: failed to determine current user principal: %w", err))
			return
		}
		if principalPath == "" {
			// Without a current user, redirect to the context path of the
			// service, the root of the handler (RFC 6764 section 5), rather
			// than resolve an empty path to "/.well-known/"
			principalPath = b.Prefix + "/"
		}

		// The location depends on the user, so caches must check back
		// before reusing the redirect (RFC 6764 section 5)
		w.Header().Set("Cache-Control", "no-cache")
		http.Redirect(w, r, principalPath, http.StatusPermanentRedirect)
		return
	}

	if !b.inPrefix(r.URL.Path) {
		if r.Method == http.MethodDelete {
			if pb, ok := h.Backend.(PushBackend); ok {
				if handled, err := h.servePush(w, r, pb); handled {
					if err != nil {
						internal.ServeError(w, err)
					}
					return
				}
			}
		}
		internal.ServeError(w, internal.HTTPErrorf(http.StatusNotFound, "carddav: resource outside handler prefix"))
		return
	}

	r, err := internal.DecodeRequestBody(w, r)
	if err != nil {
		internal.ServeError(w, err)
		return
	}

	switch r.Method {
	case "REPORT":
		err = h.handleReport(w, r)
	default:
		if pb, ok := h.Backend.(PushBackend); ok {
			var handled bool
			if handled, err = h.servePush(w, r, pb); handled {
				break
			}
		}
		hh := internal.Handler{Backend: &b}
		hh.ServeHTTP(w, r)
	}

	if err != nil {
		internal.ServeError(w, err)
	}
}

func (h *Handler) handleReport(w http.ResponseWriter, r *http.Request) error {
	var report reportReq
	if err := internal.DecodeXMLRequest(r, &report); err != nil {
		return err
	}

	if report.Query != nil {
		return h.handleQuery(r, w, report.Query)
	} else if report.Multiget != nil {
		return h.handleMultiget(r, w, report.Multiget)
	} else if report.SyncCollection != nil {
		return h.handleSyncCollection(r, w, report.SyncCollection)
	}
	return internal.HTTPErrorf(http.StatusBadRequest, "carddav: expected addressbook-query, addressbook-multiget or sync-collection element in REPORT request")
}

func decodePropFilter(el *propFilter) (*PropFilter, error) {
	// RFC 6352 section 10.5.1 requires a name
	if el.Name == "" {
		return nil, fmt.Errorf("carddav: failed to parse prop-filter: missing name")
	}
	pf := &PropFilter{Name: el.Name, Test: FilterTest(el.Test)}
	if el.IsNotDefined != nil {
		if len(el.TextMatches) > 0 || len(el.Params) > 0 {
			return nil, fmt.Errorf("carddav: failed to parse prop-filter: if is-not-defined is provided, text-match or param-filter can't be provided")
		}
		pf.IsNotDefined = true
	}
	for _, tm := range el.TextMatches {
		decodedTM, err := decodeTextMatch(&tm)
		if err != nil {
			return nil, err
		}
		pf.TextMatches = append(pf.TextMatches, *decodedTM)
	}
	for _, paramEl := range el.Params {
		param, err := decodeParamFilter(&paramEl)
		if err != nil {
			return nil, err
		}
		pf.Params = append(pf.Params, *param)
	}
	return pf, nil
}

func decodeParamFilter(el *paramFilter) (*ParamFilter, error) {
	// RFC 6352 section 10.5.2: (is-not-defined | text-match)?, and a name
	if el.Name == "" {
		return nil, fmt.Errorf("carddav: failed to parse param-filter: missing name")
	}
	if len(el.TextMatch) > 1 {
		return nil, fmt.Errorf("carddav: failed to parse param-filter: more than one text-match")
	}
	pf := &ParamFilter{Name: el.Name}
	if el.IsNotDefined != nil {
		if len(el.TextMatch) > 0 {
			return nil, fmt.Errorf("carddav: failed to parse param-filter: if is-not-defined is provided, text-match can't be provided")
		}
		pf.IsNotDefined = true
	}
	if len(el.TextMatch) > 0 {
		decodedTM, err := decodeTextMatch(&el.TextMatch[0])
		if err != nil {
			return nil, err
		}
		pf.TextMatch = decodedTM
	}
	return pf, nil
}

func decodeTextMatch(tm *textMatch) (*TextMatch, error) {
	if !internal.IsSupportedCollation(tm.Collation) {
		return nil, newPreconditionError(http.StatusForbidden, PreconditionSupportedCollation)
	}
	collation := tm.Collation
	// "default" names the default collation (RFC 6352 section 8.3), which
	// backends get as an empty Collation
	if collation == internal.CollationDefault {
		collation = ""
	}
	return &TextMatch{
		Text:            tm.Text,
		Collation:       collation,
		NegateCondition: bool(tm.NegateCondition),
		MatchType:       MatchType(tm.MatchType),
	}, nil
}

func decodeAddressDataReq(addressData *addressDataReq) (*AddressDataRequest, error) {
	if addressData.Allprop != nil && len(addressData.Props) > 0 {
		return nil, internal.HTTPErrorf(http.StatusBadRequest, "carddav: only one of allprop or prop can be specified in address-data")
	}

	// RFC 6352 section 10.4: without prop elements, address object resources
	// are returned in their entirety.
	req := &AddressDataRequest{AllProp: addressData.Allprop != nil || len(addressData.Props) == 0}
	for _, p := range addressData.Props {
		req.Props = append(req.Props, p.Name)
		if !p.NoValue {
			continue
		}
		// A property requested both with and without novalue is returned
		// with its value
		withValue := slices.ContainsFunc(addressData.Props, func(q prop) bool {
			return !bool(q.NoValue) && strings.EqualFold(q.Name, p.Name)
		})
		if !withValue {
			req.NoValue = append(req.NoValue, p.Name)
		}
	}
	return req, nil
}

// decodeAddressDataProp decodes the CARDDAV:address-data element of the
// DAV:prop element of a REPORT request.
func decodeAddressDataProp(prop *internal.Prop) (*AddressDataRequest, error) {
	var addressData addressDataReq
	if err := prop.Decode(&addressData); internal.IsNotFound(err) {
		return &AddressDataRequest{}, nil
	} else if err != nil {
		return nil, &internal.HTTPError{Code: http.StatusBadRequest, Err: err}
	}
	return decodeAddressDataReq(&addressData)
}

func (h *Handler) handleQuery(r *http.Request, w http.ResponseWriter, query *addressbookQuery) error {
	propfind := internal.PropFind{
		Prop:     query.Prop,
		AllProp:  query.AllProp,
		PropName: query.PropName,
	}
	if err := internal.CheckReportPropFind(&propfind); err != nil {
		return err
	}

	var q AddressBookQuery
	if query.Prop != nil {
		req, err := decodeAddressDataProp(query.Prop)
		if err != nil {
			return err
		}
		q.DataRequest = *req
	}
	q.FilterTest = FilterTest(query.Filter.Test)
	for _, el := range query.Filter.Props {
		pf, err := decodePropFilter(&el)
		if err != nil {
			var httpErr *internal.HTTPError
			if errors.As(err, &httpErr) {
				return err
			}
			return &internal.HTTPError{Code: http.StatusBadRequest, Err: err}
		}
		q.PropFilters = append(q.PropFilters, *pf)
	}
	if query.Limit != nil {
		q.Limit = int(query.Limit.NResults)
		if q.Limit <= 0 {
			return internal.ServeMultiStatus(w, internal.NewMultiStatus())
		}
	}

	aos, err := h.Backend.QueryAddressObjects(r.Context(), r.URL.Path, &q)
	errResps, err := internal.HrefErrorResponses(err)
	if err != nil {
		return err
	}

	b := h.newBackend()
	return internal.StreamMultiStatus(w, r, func(mw *internal.MultiStatusWriter) error {
		for i := range aos {
			resp, err := b.propFindAddressObject(r.Context(), &propfind, &q.DataRequest, &aos[i])
			if err != nil {
				return err
			}
			if err := mw.WriteResponse(resp); err != nil {
				return err
			}
		}
		return mw.WriteResponses(errResps)
	})
}

// AddressObjectsBackend is an optional interface a Backend can implement to
// fetch the address objects of a addressbook-multiget REPORT in a single call,
// rather than calling GetAddressObject for each of them, in which case the
// response for each object is sent before the next one is fetched.
type AddressObjectsBackend interface {
	// GetAddressObjects returns the address objects found at paths, in any order.
	// The Path of each must be the path it was requested by: paths without
	// an object are reported as 404 Not Found, or with the per-resource error
	// reported via webdav.NewHrefError. Any other error fails the whole request.
	GetAddressObjects(ctx context.Context, paths []string, req *AddressDataRequest) ([]AddressObject, error)
}

// handleMultiget serves an addressbook-multiget REPORT. Without
// AddressObjectsBackend, the response for each address object is written as
// soon as the object is fetched, before the next one is.
func (h *Handler) handleMultiget(r *http.Request, w http.ResponseWriter, multiget *addressbookMultiget) error {
	ctx := r.Context()
	propfind := internal.PropFind{
		Prop:     multiget.Prop,
		AllProp:  multiget.AllProp,
		PropName: multiget.PropName,
	}
	if err := internal.CheckReportPropFind(&propfind); err != nil {
		return err
	}

	var dataReq AddressDataRequest
	if multiget.Prop != nil {
		decoded, err := decodeAddressDataProp(multiget.Prop)
		if err != nil {
			return err
		}
		dataReq = *decoded
	}

	b := h.newBackend()
	// The hrefs must name members of the resource at the Request-URI
	// (RFC 4791 section 7.9, RFC 6352 section 8.7): others are answered
	// like missing ones, without reaching the backend
	inScope := func(p string) bool {
		return b.inPrefix(p) && internal.IsInScope(r.URL.Path, p)
	}

	var objects map[string]*AddressObject
	var objErrResps map[string]internal.Response
	if mb, ok := h.Backend.(AddressObjectsBackend); ok {
		paths := make([]string, 0, len(multiget.Hrefs))
		for _, href := range multiget.Hrefs {
			if inScope(href.Path) {
				paths = append(paths, href.Path)
			}
		}
		objects = make(map[string]*AddressObject, len(paths))
		if len(paths) > 0 {
			l, err := mb.GetAddressObjects(ctx, paths, &dataReq)
			errResps, err := internal.HrefErrorResponses(err)
			if err != nil {
				return err
			}
			for i := range l {
				objects[l[i].Path] = &l[i]
			}
			objErrResps = make(map[string]internal.Response, len(errResps))
			for _, resp := range errResps {
				if len(resp.Hrefs) > 0 {
					objErrResps[resp.Hrefs[0].Path] = resp
				}
			}
		}
	}

	return internal.StreamMultiStatus(w, r, func(mw *internal.MultiStatusWriter) error {
		for _, href := range multiget.Hrefs {
			if !inScope(href.Path) {
				resp := internal.NewErrorResponse(href.Path, internal.HTTPErrorf(http.StatusNotFound, "carddav: address object not found"))
				if err := mw.WriteResponse(resp); err != nil {
					return err
				}
				continue
			}

			var ao *AddressObject
			var err error
			if objects != nil {
				if errResp, ok := objErrResps[href.Path]; ok {
					if err := mw.WriteResponse(&errResp); err != nil {
						return err
					}
					continue
				}
				if ao = objects[href.Path]; ao == nil {
					err = internal.HTTPErrorf(http.StatusNotFound, "carddav: address object not found")
				}
			} else {
				ao, err = h.Backend.GetAddressObject(ctx, href.Path, &dataReq)
			}

			var resp *internal.Response
			if err != nil {
				resp = internal.NewErrorResponse(href.Path, err)
			} else if resp, err = b.propFindAddressObject(ctx, &propfind, &dataReq, ao); err != nil {
				return err
			}
			if err := mw.WriteResponse(resp); err != nil {
				return err
			}
		}
		return nil
	})
}

type backend struct {
	Backend Backend
	Prefix  string
}

type resourceType int

const (
	resourceTypeRoot resourceType = iota
	resourceTypeUserPrincipal
	resourceTypeAddressBookHomeSet
	resourceTypeAddressBook
	resourceTypeAddressObject
)

const resourceTypeInvalid resourceType = -1

func (b *backend) resourceTypeAtPath(reqPath string) resourceType {
	p := path.Clean(reqPath)
	if !b.inPrefix(p) {
		return resourceTypeInvalid
	}
	p = strings.TrimPrefix(p, b.Prefix)
	if !strings.HasPrefix(p, "/") {
		p = "/" + p
	}
	if p == "/" {
		return resourceTypeRoot
	}
	return resourceType(len(strings.Split(p, "/")) - 1)
}

func (b *backend) Options(r *http.Request) (caps []string, allow []string, err error) {
	resType := b.resourceTypeAtPath(r.URL.Path)
	if resType == resourceTypeInvalid {
		return nil, nil, internal.HTTPErrorf(http.StatusNotFound, "carddav: resource outside handler prefix")
	}

	caps = []string{"addressbook"}

	if resType != resourceTypeAddressObject {
		// Note: some clients assume the address book is read-only when
		// DELETE/MKCOL are missing
		allow = []string{http.MethodOptions, "PROPFIND", "REPORT", "DELETE", "MKCOL"}
		// MKCOL creates collections with extended MKCOL only (RFC 5689
		// section 3.1)
		caps = append(caps, "extended-mkcol")
		if _, ok := b.Backend.(AddressBookUpdater); ok {
			allow = append(allow, "PROPPATCH")
		}
		// Advertise WebDAV-Push for the collections supporting it
		// (draft-bitfire-webdav-push section 2). With push information
		// that can't be served as the draft requires, they don't: the
		// push properties and registrations get the error.
		if info, err := b.pushInfo(r.Context(), r.URL.Path); err != nil && !errors.Is(err, push.ErrInvalidInfo) {
			return nil, nil, err
		} else if info != nil {
			caps = append(caps, string(webdav.CapabilityPush))
			allow = append(allow, http.MethodPost)
		}
		return caps, allow, nil
	}

	var dataReq AddressDataRequest
	_, err = b.Backend.GetAddressObject(r.Context(), r.URL.Path, &dataReq)
	if internal.IsNotFound(err) {
		return caps, []string{http.MethodOptions, http.MethodPut}, nil
	} else if err != nil {
		return nil, nil, err
	}

	allow = []string{
		http.MethodOptions,
		http.MethodHead,
		http.MethodGet,
		http.MethodPut,
		http.MethodDelete,
		"PROPFIND",
	}
	if _, ok := b.Backend.(AddressObjectCopyMover); ok {
		allow = append(allow, "COPY", "MOVE")
	}
	return caps, allow, nil
}

func (b *backend) HeadGet(w http.ResponseWriter, r *http.Request) error {
	var dataReq AddressDataRequest
	if r.Method != http.MethodHead {
		dataReq.AllProp = true
	}
	ao, err := b.Backend.GetAddressObject(r.Context(), r.URL.Path, &dataReq)
	if err != nil {
		return err
	}

	w.Header().Set("Content-Type", vcard.MIMEType)
	if ao.ContentLength > 0 {
		w.Header().Set("Content-Length", strconv.FormatInt(ao.ContentLength, 10))
	}
	if ao.ETag != "" {
		w.Header().Set("ETag", internal.ETag(ao.ETag).String())
	}
	if !ao.ModTime.IsZero() {
		w.Header().Set("Last-Modified", ao.ModTime.UTC().Format(http.TimeFormat))
	}

	if r.Method != http.MethodHead {
		return vcard.NewEncoder(w).Encode(ao.Card)
	}
	return nil
}

// PropFind writes the responses to a PROPFIND request to mw. The members of a
// collection are listed before the response for the collection is written:
// for a Depth: 0 or 1 request, any error listing resources is served as the
// status of the request (see internal.StreamMultiStatus).
func (b *backend) PropFind(r *http.Request, propfind *internal.PropFind, depth internal.Depth, mw *internal.MultiStatusWriter) error {
	ctx := r.Context()
	resType := b.resourceTypeAtPath(r.URL.Path)

	var dataReq AddressDataRequest

	switch resType {
	case resourceTypeInvalid:
		return internal.HTTPErrorf(http.StatusNotFound, "carddav: resource outside handler prefix")
	case resourceTypeRoot:
		resp, err := b.propFindRoot(ctx, r.URL.Path, propfind)
		if err != nil {
			return err
		}
		return mw.WriteResponse(resp)
	case resourceTypeUserPrincipal:
		principalPath, err := b.Backend.CurrentUserPrincipal(ctx)
		if err != nil {
			return err
		}
		if principalPath == "" || !internal.SamePath(r.URL.Path, principalPath) {
			return internal.HTTPErrorf(http.StatusNotFound, "carddav: principal not found")
		}
		resp, err := b.propFindUserPrincipal(ctx, propfind)
		if err != nil {
			return err
		}
		if depth == internal.DepthZero {
			return mw.WriteResponse(resp)
		}
		homeSetResp, err := b.propFindHomeSet(ctx, propfind)
		if err != nil {
			return err
		}
		if depth != internal.DepthInfinity {
			if err := mw.WriteResponse(resp); err != nil {
				return err
			}
			return mw.WriteResponse(homeSetResp)
		}
		return b.propFindAllAddressBooks(ctx, mw, propfind, true, resp, homeSetResp)
	case resourceTypeAddressBookHomeSet:
		homeSetPath, err := b.Backend.AddressBookHomeSetPath(ctx)
		if err != nil {
			return err
		}
		if homeSetPath == "" || !internal.SamePath(r.URL.Path, homeSetPath) {
			return internal.HTTPErrorf(http.StatusNotFound, "carddav: address book home set not found")
		}
		resp, err := b.propFindHomeSet(ctx, propfind)
		if err != nil {
			return err
		}
		if depth == internal.DepthZero {
			return mw.WriteResponse(resp)
		}
		recurse := depth == internal.DepthInfinity
		return b.propFindAllAddressBooks(ctx, mw, propfind, recurse, resp)
	case resourceTypeAddressBook:
		ab, err := b.Backend.GetAddressBook(ctx, r.URL.Path)
		if err != nil {
			return err
		}
		resp, err := b.propFindAddressBook(ctx, propfind, ab)
		if err != nil {
			return err
		}
		if depth == internal.DepthZero {
			return mw.WriteResponse(resp)
		}
		return b.propFindAllAddressObjects(ctx, mw, propfind, ab, resp)
	case resourceTypeAddressObject:
		ao, err := b.Backend.GetAddressObject(ctx, r.URL.Path, &dataReq)
		if err != nil {
			return err
		}
		resp, err := b.propFindAddressObject(ctx, propfind, nil, ao)
		if err != nil {
			return err
		}
		return mw.WriteResponse(resp)
	}
	return internal.HTTPErrorf(http.StatusNotFound, "carddav: resource not found")
}

func (b *backend) propFindRoot(ctx context.Context, rootPath string, propfind *internal.PropFind) (*internal.Response, error) {
	principalPath, err := b.Backend.CurrentUserPrincipal(ctx)
	if err != nil {
		return nil, err
	}

	props := map[xml.Name]internal.PropFindFunc{
		internal.CurrentUserPrincipalName: internal.PropFindValue(internal.NewCurrentUserPrincipal(principalPath)),
		internal.ResourceTypeName:         internal.PropFindValue(internal.NewResourceType(internal.CollectionName)),
	}
	return internal.NewPropFindResponse(rootPath, propfind, props)
}

func (b *backend) propFindUserPrincipal(ctx context.Context, propfind *internal.PropFind) (*internal.Response, error) {
	principalPath, err := b.Backend.CurrentUserPrincipal(ctx)
	if err != nil {
		return nil, err
	}

	props := map[xml.Name]internal.PropFindFunc{
		internal.CurrentUserPrincipalName: internal.PropFindValue(internal.NewCurrentUserPrincipal(principalPath)),
		addressBookHomeSetName: func(*internal.RawXMLValue) (interface{}, error) {
			homeSetPath, err := b.Backend.AddressBookHomeSetPath(ctx)
			if err != nil {
				return nil, err
			}
			return &addressbookHomeSet{Hrefs: []internal.Href{{Path: homeSetPath}}}, nil
		},
		internal.ResourceTypeName: internal.PropFindValue(internal.NewResourceType(internal.CollectionName, internal.PrincipalName)),
	}
	internal.AddPrincipalProps(props, principalPath, "")
	// The principal owns itself
	internal.AddACLProps(props, func() (string, error) { return principalPath, nil })
	b.addPushProps(ctx, propfind, principalPath, props)
	return internal.NewPropFindResponse(principalPath, propfind, props)
}

// addACLProps adds to props the access control properties of a resource of
// the current user, see internal.AddACLProps.
func (b *backend) addACLProps(ctx context.Context, props map[xml.Name]internal.PropFindFunc) {
	internal.AddACLProps(props, func() (string, error) {
		return b.Backend.CurrentUserPrincipal(ctx)
	})
}

func (b *backend) propFindHomeSet(ctx context.Context, propfind *internal.PropFind) (*internal.Response, error) {
	homeSetPath, err := b.Backend.AddressBookHomeSetPath(ctx)
	if err != nil {
		return nil, err
	}

	// TODO anything else to return here?
	props := map[xml.Name]internal.PropFindFunc{
		internal.CurrentUserPrincipalName: func(*internal.RawXMLValue) (interface{}, error) {
			principalPath, err := b.Backend.CurrentUserPrincipal(ctx)
			if err != nil {
				return nil, err
			}
			return internal.NewCurrentUserPrincipal(principalPath), nil
		},
		internal.ResourceTypeName: internal.PropFindValue(internal.NewResourceType(internal.CollectionName)),
	}
	b.addACLProps(ctx, props)
	b.addPushProps(ctx, propfind, homeSetPath, props)
	b.addMeCardProp(ctx, propfind, props)
	return internal.NewPropFindResponse(homeSetPath, propfind, props)
}

// addMeCardProp adds the me-card property to props, the properties of the
// address book home set, if the backend implements MeCardBackend and reports a
// me card. The backend is only asked for it if the PROPFIND request may
// return the property.
func (b *backend) addMeCardProp(ctx context.Context, propfind *internal.PropFind, props map[xml.Name]internal.PropFindFunc) {
	mb, ok := b.Backend.(MeCardBackend)
	if !ok || !propFindMayReturn(propfind, meCardName) {
		return
	}
	p, err := mb.MeCardPath(ctx)
	if err != nil {
		// A propname response lists names without calling the PropFindFuncs,
		// so it can't carry the error, and whether there's a me card is
		// unknown: leave the property out
		if propfind.PropName != nil {
			return
		}
		props[meCardName] = func(*internal.RawXMLValue) (interface{}, error) {
			return nil, err
		}
	} else if p != "" {
		props[meCardName] = internal.PropFindValue(&meCard{Href: &internal.Href{Path: p}})
	}
}

// propFindMayReturn reports whether propfind may return the property name,
// which allprop doesn't return: the request names it, or asks for the names
// of all the properties.
func propFindMayReturn(propfind *internal.PropFind, name xml.Name) bool {
	if propfind.PropName != nil {
		return true
	}
	var raws []internal.RawXMLValue
	if propfind.Prop != nil {
		raws = propfind.Prop.Raw
	} else if propfind.AllProp != nil && propfind.Include != nil {
		raws = propfind.Include.Raw
	}
	for i := range raws {
		if n, ok := raws[i].XMLName(); ok && n == name {
			return true
		}
	}
	return false
}

func (b *backend) propFindAddressBook(ctx context.Context, propfind *internal.PropFind, ab *AddressBook) (*internal.Response, error) {
	props := map[xml.Name]internal.PropFindFunc{
		internal.CurrentUserPrincipalName: func(*internal.RawXMLValue) (interface{}, error) {
			path, err := b.Backend.CurrentUserPrincipal(ctx)
			if err != nil {
				return nil, err
			}
			return internal.NewCurrentUserPrincipal(path), nil
		},
		internal.ResourceTypeName: internal.PropFindValue(internal.NewResourceType(internal.CollectionName, addressBookName)),
		supportedAddressDataName: internal.PropFindValue(&supportedAddressData{
			Types: []addressDataType{
				{ContentType: vcard.MIMEType, Version: "3.0"},
				{ContentType: vcard.MIMEType, Version: "4.0"},
			},
		}),
		// addressbook-query, the REPORT which does text matching, is served
		// for address books (RFC 6352 section 8.3)
		supportedCollationSetName: internal.PropFindValue(&supportedCollationSet{
			Collations: internal.SupportedCollations(),
		}),
		internal.CurrentUserPrivilegeSetName: internal.PropFindValue(internal.NewCurrentUserPrivilegeSet(ab.ReadOnly)),
		internal.SupportedReportSetName:      internal.PropFindValue(b.supportedReportSet()),
	}

	if ab.SyncToken != "" {
		props[internal.SyncTokenName] = internal.PropFindValue(&internal.SyncToken{
			Token: ab.SyncToken,
		})
	}
	if ab.Name != "" {
		props[internal.DisplayNameName] = internal.PropFindValue(&internal.DisplayName{
			Name: ab.Name,
		})
	}
	if ab.Description != "" {
		props[addressBookDescriptionName] = internal.PropFindValue(&addressbookDescription{
			Description: ab.Description,
		})
	}
	if ab.MaxResourceSize > 0 {
		props[maxResourceSizeName] = internal.PropFindValue(&maxResourceSize{
			Size: ab.MaxResourceSize,
		})
	}
	if ab.CTag != "" {
		props[internal.GetCTagName] = internal.PropFindValue(&internal.GetCTag{CTag: ab.CTag})
	}

	b.addACLProps(ctx, props)
	b.addPushProps(ctx, propfind, ab.Path, props)
	return internal.NewPropFindResponse(ab.Path, propfind, props)
}

// propFindAllAddressBooks writes resps, which end with the response for the
// address book home set, followed by the responses for the address books,
// along with their address objects if recurse is set. The address books are
// listed before resps are written.
func (b *backend) propFindAllAddressBooks(ctx context.Context, mw *internal.MultiStatusWriter, propfind *internal.PropFind, recurse bool, resps ...*internal.Response) error {
	abs, err := b.Backend.ListAddressBooks(ctx)
	errResps, err := internal.HrefErrorResponses(err)
	if err != nil {
		return err
	}

	for _, resp := range resps {
		if err := mw.WriteResponse(resp); err != nil {
			return err
		}
	}
	for i := range abs {
		resp, err := b.propFindAddressBook(ctx, propfind, &abs[i])
		if err != nil {
			return err
		}
		if recurse {
			err = b.propFindAllAddressObjects(ctx, mw, propfind, &abs[i], resp)
		} else {
			err = mw.WriteResponse(resp)
		}
		if err != nil {
			return err
		}
	}
	return mw.WriteResponses(errResps)
}

// propFindAddressObject returns the response for ao. dataReq is the
// address-data of a REPORT request, or nil for a PROPFIND request.
func (b *backend) propFindAddressObject(ctx context.Context, propfind *internal.PropFind, dataReq *AddressDataRequest, ao *AddressObject) (*internal.Response, error) {
	// Unlike a PROPFIND request, an addressbook-query or addressbook-multiget
	// REPORT doesn't have to ask for properties (RFC 6352 sections 10.3 and
	// 10.7). Each object found still gets a response, with only a status
	// (RFC 4918 section 14.24).
	if dataReq != nil && !propfind.AsksForProps() {
		return internal.NewOKResponse(ao.Path), nil
	}

	props := map[xml.Name]internal.PropFindFunc{
		internal.CurrentUserPrincipalName: func(*internal.RawXMLValue) (interface{}, error) {
			path, err := b.Backend.CurrentUserPrincipal(ctx)
			if err != nil {
				return nil, err
			}
			return internal.NewCurrentUserPrincipal(path), nil
		},
		internal.CurrentUserPrivilegeSetName: internal.PropFindValue(internal.NewCurrentUserPrivilegeSet(ao.ReadOnly)),
		internal.GetContentTypeName: internal.PropFindValue(&internal.GetContentType{
			Type: vcard.MIMEType,
		}),
	}

	// address-data is not a property: it is only returned by REPORT
	// requests, not by PROPFIND (RFC 6352 section 10.4)
	if dataReq != nil {
		props[addressDataName] = func(*internal.RawXMLValue) (interface{}, error) {
			// Only the requested properties are returned (RFC 6352
			// section 10.4), whatever the backend returned. Filtering
			// again what Filter already filtered changes nothing.
			card := filterProperties(*dataReq, *ao).Card
			var buf bytes.Buffer
			if err := vcard.NewEncoder(&buf).Encode(card); err != nil {
				return nil, err
			}

			return &addressDataResp{Data: buf.Bytes()}, nil
		}
	}

	if ao.ContentLength > 0 {
		props[internal.GetContentLengthName] = internal.PropFindValue(&internal.GetContentLength{
			Length: ao.ContentLength,
		})
	}
	if !ao.ModTime.IsZero() {
		props[internal.GetLastModifiedName] = internal.PropFindValue(&internal.GetLastModified{
			LastModified: internal.Time(ao.ModTime),
		})
	}

	if ao.ETag != "" {
		props[internal.GetETagName] = internal.PropFindValue(&internal.GetETag{
			ETag: internal.ETag(ao.ETag),
		})
	}

	b.addACLProps(ctx, props)
	return internal.NewPropFindResponse(ao.Path, propfind, props)
}

// propFindAllAddressObjects writes resp, the response for the address book
// ab, followed by the responses for its address objects. The address objects
// are listed before resp is written.
func (b *backend) propFindAllAddressObjects(ctx context.Context, mw *internal.MultiStatusWriter, propfind *internal.PropFind, ab *AddressBook, resp *internal.Response) error {
	var dataReq AddressDataRequest
	aos, err := b.Backend.ListAddressObjects(ctx, ab.Path, &dataReq)
	errResps, err := internal.HrefErrorResponses(err)
	if err != nil {
		return err
	}

	if err := mw.WriteResponse(resp); err != nil {
		return err
	}
	for i := range aos {
		resp, err := b.propFindAddressObject(ctx, propfind, nil, &aos[i])
		if err != nil {
			return err
		}
		if err := mw.WriteResponse(resp); err != nil {
			return err
		}
	}
	return mw.WriteResponses(errResps)
}

func (b *backend) PropPatch(r *http.Request, update *internal.PropertyUpdate) (*internal.Response, error) {
	// Make sure the resource exists before reporting on its properties
	switch b.resourceTypeAtPath(r.URL.Path) {
	case resourceTypeAddressBook:
		if _, err := b.Backend.GetAddressBook(r.Context(), r.URL.Path); err != nil {
			return nil, err
		}
		if updater, ok := b.Backend.(AddressBookUpdater); ok {
			return b.propPatchAddressBook(r, updater, update)
		}
	case resourceTypeAddressObject:
		var dataReq AddressDataRequest
		if _, err := b.Backend.GetAddressObject(r.Context(), r.URL.Path, &dataReq); err != nil {
			return nil, err
		}
	}

	return internal.NewPropPatchResponse(r.URL.Path, update, http.StatusForbidden)
}

func (b *backend) propPatchAddressBook(r *http.Request, updater AddressBookUpdater, update *internal.PropertyUpdate) (*internal.Response, error) {
	var abUpdate AddressBookUpdate
	patch := func(raw *internal.RawXMLValue, remove bool) error {
		name, _ := raw.XMLName()
		switch name {
		case internal.DisplayNameName:
			var v internal.DisplayName
			if !remove {
				if err := raw.Decode(&v); err != nil {
					return &internal.HTTPError{Code: http.StatusConflict, Err: err}
				}
			}
			abUpdate.Name = &v.Name
		case addressBookDescriptionName:
			var v addressbookDescription
			if !remove {
				if err := raw.Decode(&v); err != nil {
					return &internal.HTTPError{Code: http.StatusConflict, Err: err}
				}
			}
			abUpdate.Description = &v.Description
		default:
			return internal.HTTPErrorf(http.StatusForbidden, "carddav: property cannot be modified")
		}
		return nil
	}
	commit := func() error {
		return updater.UpdateAddressBook(r.Context(), r.URL.Path, &abUpdate)
	}
	return internal.PatchProps(r.URL.Path, update, patch, commit)
}

func (b *backend) Put(w http.ResponseWriter, r *http.Request) error {
	ifNoneMatch := webdav.ConditionalMatch(r.Header.Get("If-None-Match"))
	ifMatch := webdav.ConditionalMatch(r.Header.Get("If-Match"))

	opts := PutAddressObjectOptions{
		IfNoneMatch: ifNoneMatch,
		IfMatch:     ifMatch,
	}

	t, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil {
		return internal.HTTPErrorf(http.StatusBadRequest, "carddav: malformed Content-Type: %v", err)
	}
	if t != vcard.MIMEType {
		// TODO: send CARDDAV:supported-address-data error
		return internal.HTTPErrorf(http.StatusBadRequest, "carddav: unsupporetd Content-Type %q", t)
	}

	// TODO: check CARDDAV:max-resource-size precondition
	card, err := vcard.NewDecoder(r.Body).Decode()
	if err != nil {
		// TODO: send CARDDAV:valid-address-data error
		return internal.HTTPErrorf(http.StatusBadRequest, "carddav: failed to parse vCard: %v", err)
	}

	// TODO: add support for the CARDDAV:no-uid-conflict error
	ao, err := b.Backend.PutAddressObject(r.Context(), r.URL.Path, card, &opts)
	if err != nil {
		return err
	}
	if ao.ETag != "" {
		w.Header().Set("ETag", internal.ETag(ao.ETag).String())
	}
	if !ao.ModTime.IsZero() {
		w.Header().Set("Last-Modified", ao.ModTime.UTC().Format(http.TimeFormat))
	}
	if ao.Path != "" {
		w.Header().Set("Location", ao.Path)
	}

	// TODO: http.StatusNoContent if the resource already existed
	w.WriteHeader(http.StatusCreated)

	return nil
}

func (b *backend) Delete(r *http.Request) error {
	switch b.resourceTypeAtPath(r.URL.Path) {
	case resourceTypeAddressBook:
		return b.Backend.DeleteAddressBook(r.Context(), r.URL.Path)
	case resourceTypeAddressObject:
		return b.Backend.DeleteAddressObject(r.Context(), r.URL.Path)
	}
	return internal.HTTPErrorf(http.StatusForbidden, "carddav: cannot delete resource at given location")
}

// Mkcol handles extended MKCOL requests, which create address books (RFC 6352
// section 6.3.1).
func (b *backend) Mkcol(r *http.Request) error {
	m, err := internal.DecodeMkcolRequest(r)
	if err != nil {
		return err
	}
	// The Backend only creates address books: not the ordinary collection of
	// a MKCOL request without a body, for instance
	if err := m.CheckResourceType(addressBookName); err != nil {
		return err
	}
	if b.resourceTypeAtPath(r.URL.Path) != resourceTypeAddressBook {
		// RFC 6352 defines no precondition for MKCOL: this is the one of
		// COPY and MOVE for the same condition (section 6.3.2.1), as CalDAV
		// uses calendar-collection-location-ok for MKCALENDAR
		return newPreconditionError(http.StatusForbidden, PreconditionAddressBookCollectionLocationOk)
	}

	var prop mkcolProp
	if err := m.DecodeProp(&prop); err != nil {
		return err
	}
	return b.Backend.CreateAddressBook(r.Context(), &AddressBook{
		Path:        r.URL.Path,
		Name:        prop.DisplayName,
		Description: prop.Description.Description,
	})
}

// inPrefix reports whether p is under the prefix of the Handler.
func (b *backend) inPrefix(p string) bool {
	p = path.Clean(p)
	return b.Prefix == "" || p == b.Prefix || strings.HasPrefix(p, b.Prefix+"/")
}

// checkCopyMove checks that a COPY or MOVE request is for an address object,
// to be copied or moved into an address book.
func (b *backend) checkCopyMove(src, dst string) error {
	if b.resourceTypeAtPath(src) != resourceTypeAddressObject {
		return internal.HTTPErrorf(http.StatusForbidden, "carddav: only address objects can be copied or moved")
	}
	if !b.inPrefix(dst) {
		// RFC 4918 sections 9.8.5 and 9.9.4
		return internal.HTTPErrorf(http.StatusBadGateway, "carddav: destination is outside of the CardDAV namespace")
	}
	if b.resourceTypeAtPath(dst) != resourceTypeAddressObject {
		return internal.HTTPErrorf(http.StatusForbidden, "carddav: destination must be an address object in an address book")
	}
	return nil
}

func (b *backend) Copy(r *http.Request, dest *internal.Href, recursive, overwrite bool) (created bool, err error) {
	cm, ok := b.Backend.(AddressObjectCopyMover)
	if !ok {
		return false, internal.HTTPErrorf(http.StatusNotImplemented, "carddav: Copy not implemented")
	}
	if err := b.checkCopyMove(r.URL.Path, dest.Path); err != nil {
		return false, err
	}
	// recursive doesn't matter: address objects have no members
	opts := CopyAddressObjectOptions{NoOverwrite: !overwrite}
	return cm.CopyAddressObject(r.Context(), r.URL.Path, dest.Path, &opts)
}

func (b *backend) Move(r *http.Request, dest *internal.Href, overwrite bool) (created bool, err error) {
	cm, ok := b.Backend.(AddressObjectCopyMover)
	if !ok {
		return false, internal.HTTPErrorf(http.StatusNotImplemented, "carddav: Move not implemented")
	}
	if err := b.checkCopyMove(r.URL.Path, dest.Path); err != nil {
		return false, err
	}
	opts := MoveAddressObjectOptions{NoOverwrite: !overwrite}
	return cm.MoveAddressObject(r.Context(), r.URL.Path, dest.Path, &opts)
}

// PreconditionType as defined in https://tools.ietf.org/rfcmarkup?doc=6352#section-6.3.2.1
type PreconditionType string

const (
	PreconditionNoUIDConflict                   PreconditionType = "no-uid-conflict"
	PreconditionSupportedAddressData            PreconditionType = "supported-address-data"
	PreconditionValidAddressData                PreconditionType = "valid-address-data"
	PreconditionAddressBookCollectionLocationOk PreconditionType = "addressbook-collection-location-ok"
	PreconditionMaxResourceSize                 PreconditionType = "max-resource-size"
	PreconditionSupportedCollation              PreconditionType = "supported-collation"
)

func NewPreconditionError(err PreconditionType) error {
	return newPreconditionError(http.StatusConflict, err)
}

func newPreconditionError(code int, err PreconditionType) error {
	name := xml.Name{Space: "urn:ietf:params:xml:ns:carddav", Local: string(err)}
	elem := internal.NewRawXMLElement(name, nil, nil)
	return &internal.HTTPError{
		Code: code,
		Err: &internal.Error{
			Raw: []internal.RawXMLValue{*elem},
		},
	}
}
