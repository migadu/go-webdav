package webdav

import (
	"context"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path"
	"strconv"
	"strings"

	"github.com/emersion/go-webdav/internal"
	"github.com/emersion/go-webdav/internal/push"
)

// FileSystem is a WebDAV server backend.
//
// The methods acting on several files (ReadDir, RemoveAll, Copy and Move) can
// report that some of these files failed while the others succeeded, by
// returning an error made only of errors created by NewHrefError: either a
// single one, or several combined with errors.Join. The server then replies
// with a 207 Multi-Status response listing each failed file with its own
// status, instead of failing the whole request. Any other error fails the
// whole request.
type FileSystem interface {
	Open(ctx context.Context, name string) (io.ReadCloser, error)
	Stat(ctx context.Context, name string) (*FileInfo, error)
	// ReadDir lists the files in a directory. Files that couldn't be read
	// can be reported with NewHrefError, along with the other files.
	ReadDir(ctx context.Context, name string, recursive bool) ([]FileInfo, error)
	Create(ctx context.Context, name string, body io.ReadCloser, opts *CreateOptions) (fileInfo *FileInfo, created bool, err error)
	// RemoveAll deletes a file, and all of its descendants if it's a
	// directory. Descendants that couldn't be deleted can be reported with
	// NewHrefError; as per RFC 4918 section 9.6.1, the descendants that were
	// deleted and the directories that couldn't be deleted because of them
	// shouldn't be reported.
	RemoveAll(ctx context.Context, name string, opts *RemoveAllOptions) error
	Mkdir(ctx context.Context, name string) error
	// Copy copies a file, and its descendants if it's a directory and
	// options.NoRecursive isn't set. Descendants that couldn't be copied can
	// be reported with NewHrefError (RFC 4918 section 9.8.5).
	Copy(ctx context.Context, name, dest string, options *CopyOptions) (created bool, err error)
	// Move moves a file, and its descendants if it's a directory.
	// Descendants that couldn't be moved can be reported with NewHrefError
	// (RFC 4918 section 9.9.4).
	Move(ctx context.Context, name, dest string, options *MoveOptions) (created bool, err error)
}

// DeadPropertyFileSystem is an optional interface a FileSystem can implement
// to store dead properties (see DeadProperty), and let clients set and remove
// them with PROPPATCH. Otherwise the server refuses to modify any property.
//
// Properties in the DAV: namespace are live properties, which the server
// computes and which clients cannot modify, except DAV:displayname, which RFC
// 4918 section 15.2 says shouldn't be protected and which is a dead property.
//
// Dead properties belong to their file: Copy should copy them, Move should
// move them and RemoveAll should delete them (RFC 4918 sections 9.8.2 and
// 9.9.1).
type DeadPropertyFileSystem interface {
	FileSystem

	// DeadProperties returns the dead properties of a file, in any order.
	// The server calls it when a PROPFIND request may return dead
	// properties, for each file it lists, and sends the response for a file
	// before calling it for the next one. Properties in the DAV: namespace
	// other than DAV:displayname are ignored. An error fails the whole
	// request: once responses have been sent, their status can't change
	// anymore, and the response is aborted instead, so that the client sees
	// it's incomplete.
	DeadProperties(ctx context.Context, name string) ([]DeadProperty, error)
	// PatchDeadProperties applies all of the instructions of a PROPPATCH
	// request to the dead properties of a file, or none of them (RFC 4918
	// section 9.2). The instructions are in document order, the order they
	// must be applied in: a later instruction for a property overrides an
	// earlier one. Removing a property which doesn't exist isn't an error.
	//
	// An error fails the whole request: NewHTTPError can give it a status
	// code, such as 507 Insufficient Storage.
	PatchDeadProperties(ctx context.Context, name string, patch []DeadPropertyPatch) error
}

// Handler handles WebDAV HTTP requests. It can be used to create a WebDAV
// server.
type Handler struct {
	FileSystem FileSystem
	// LockSystem, if set, enables locking (RFC 4918 compliance class 2): the
	// Handler then advertises DAV class 2, supports LOCK and UNLOCK requests,
	// evaluates the If header field and enforces locks. Otherwise, LOCK and
	// UNLOCK requests are refused with 405 Method Not Allowed and the If
	// header field is ignored.
	//
	// Locks apply to the paths of the request URLs, and so do the
	// resources tagged in the If header field, which clients name with
	// absolute URLs: as for the Destination header field of COPY and MOVE,
	// the Handler must see the full paths, rather than paths stripped of a
	// prefix with http.StripPrefix.
	//
	// An If header field with more than 100 conditions is refused with 400
	// Bad Request, and a lock whose DAV:owner element is larger than 4 KiB
	// with 413 Content Too Large.
	LockSystem LockSystem
}

// ServeHTTP implements http.Handler.
func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if h.FileSystem == nil {
		http.Error(w, "webdav: no filesystem available", http.StatusInternalServerError)
		return
	}

	b := &backend{FileSystem: h.FileSystem, locks: h.LockSystem}
	var ib internal.Backend = b
	if b.locks != nil {
		// Decode the body first, to tell lock refreshes apart
		var err error
		r, err = internal.DecodeRequestBody(w, r)
		if err == nil {
			err = b.evaluateIf(r)
		}
		if err != nil {
			internal.ServeError(w, err)
			return
		}
		ib = lockingBackend{b}
	}
	hh := internal.Handler{Backend: ib}
	hh.ServeHTTP(w, r)
}

// HTTPError is an error associated with an HTTP status code. NewHTTPError
// returns one, and a Client returns one for a request the server answered
// with a status outside 2xx, so a caller can read that status with errors.As.
type HTTPError = internal.HTTPError

// NewHTTPError creates a new error that is associated with an HTTP status code
// and optionally an error that lead to it. Backends can use this functions to
// return errors that convey some semantics (e.g. 404 not found, 403 access
// denied, etc.) while also providing an (optional) arbitrary error context
// (intended for humans).
func NewHTTPError(statusCode int, cause error) error {
	return &internal.HTTPError{Code: statusCode, Err: cause}
}

// HrefError is an error about a single resource. NewHrefError returns one,
// and a Client returns one, wrapping an HTTPError, for each resource a server
// reported as failed in a multi-status response.
type HrefError = internal.HrefError

// NewHrefError creates a new error about the resource at path, caused by err.
//
// Backend methods acting on several resources return such errors, alone or
// combined with errors.Join, to report the resources that failed while the
// others succeeded (see FileSystem, caldav.Backend and carddav.Backend). The
// server then lists each of these resources in a 207 Multi-Status response,
// with the status of an HTTPError wrapped in err (see NewHTTPError), or else
// 500 Internal Server Error, and a description of err.
//
// An error created by NewHrefError that is wrapped in another error (e.g. by
// fmt.Errorf with a single %w verb), or joined with errors of other kinds,
// fails the whole request, like any other error. So does one whose path isn't
// absolute, such as an empty one: an href is resolved against the request
// URI, so it would be about the request target.
func NewHrefError(path string, err error) error {
	return &internal.HrefError{Href: url.URL{Path: path}, Err: err}
}

type backend struct {
	FileSystem FileSystem
	locks      LockSystem // nil if locking is disabled

	// submitted holds the lock tokens submitted by the request
	submitted map[string]bool
}

func (b *backend) Options(r *http.Request) (caps []string, allow []string, err error) {
	if b.locks != nil {
		caps = []string{"2"}
	}

	fi, err := b.FileSystem.Stat(r.Context(), r.URL.Path)
	if internal.IsNotFound(err) {
		allow = []string{http.MethodOptions, http.MethodPut, "MKCOL"}
		if b.locks != nil {
			// Locking an unmapped URL creates a resource
			allow = append(allow, "LOCK")
		}
		return caps, allow, nil
	} else if err != nil {
		return nil, nil, err
	}

	allow = []string{
		http.MethodOptions,
		http.MethodDelete,
		"PROPFIND",
		"COPY",
		"MOVE",
	}

	if _, ok := b.FileSystem.(DeadPropertyFileSystem); ok {
		allow = append(allow, "PROPPATCH")
	}

	if !fi.IsDir {
		allow = append(allow, http.MethodHead, http.MethodGet, http.MethodPut)
	}

	if b.locks != nil {
		allow = append(allow, "LOCK", "UNLOCK")
	}

	return caps, allow, nil
}

func (b *backend) HeadGet(w http.ResponseWriter, r *http.Request) error {
	fi, err := b.FileSystem.Stat(r.Context(), r.URL.Path)
	if err != nil {
		return err
	}
	if fi.IsDir {
		return &internal.HTTPError{Code: http.StatusMethodNotAllowed}
	}

	f, err := b.FileSystem.Open(r.Context(), r.URL.Path)
	if err != nil {
		return err
	}
	defer f.Close()

	w.Header().Set("Content-Length", strconv.FormatInt(fi.Size, 10))
	if fi.MIMEType != "" {
		w.Header().Set("Content-Type", fi.MIMEType)
	}
	if !fi.ModTime.IsZero() {
		w.Header().Set("Last-Modified", fi.ModTime.UTC().Format(http.TimeFormat))
	}
	if fi.ETag != "" {
		w.Header().Set("ETag", internal.ETag(fi.ETag).String())
	}

	if rs, ok := f.(io.ReadSeeker); ok {
		// If it's an io.Seeker, use http.ServeContent which supports ranges
		http.ServeContent(w, r, r.URL.Path, fi.ModTime, rs)
	} else {
		if r.Method != http.MethodHead {
			io.Copy(w, f)
		}
	}
	return nil
}

func (b *backend) PropFind(r *http.Request, propfind *internal.PropFind, depth internal.Depth, mw *internal.MultiStatusWriter) error {
	fi, err := b.FileSystem.Stat(r.Context(), r.URL.Path)
	if err != nil {
		return err
	}

	var locks *propFindLocks
	if b.locks != nil {
		locks = &propFindLocks{
			b:       b,
			r:       r,
			path:    path.Clean(r.URL.Path),
			members: depth != internal.DepthZero && fi.IsDir,
		}
	}

	if depth == internal.DepthZero || !fi.IsDir {
		resp, err := b.propFindFile(r.Context(), propfind, fi, locks)
		if err != nil {
			return err
		}
		return mw.WriteResponse(resp)
	}

	children, err := b.FileSystem.ReadDir(r.Context(), r.URL.Path, depth == internal.DepthInfinity)
	errResps, err := internal.HrefErrorResponses(err)
	if err != nil {
		return err
	}
	for i := range children {
		resp, err := b.propFindFile(r.Context(), propfind, &children[i], locks)
		if err != nil {
			return err
		}
		if err := mw.WriteResponse(resp); err != nil {
			return err
		}
	}
	return mw.WriteResponses(errResps)
}

// propFindFile returns the properties of a file. locks is nil if locking is
// disabled.
func (b *backend) propFindFile(ctx context.Context, propfind *internal.PropFind, fi *FileInfo, locks *propFindLocks) (*internal.Response, error) {
	props := make(map[xml.Name]internal.PropFindFunc)

	props[internal.ResourceTypeName] = func(*internal.RawXMLValue) (interface{}, error) {
		var types []xml.Name
		if fi.IsDir {
			types = append(types, internal.CollectionName)
		}
		return internal.NewResourceType(types...), nil
	}

	if locks != nil {
		props[internal.SupportedLockName] = internal.PropFindValue(&internal.SupportedLock{
			LockEntries: []internal.LockEntry{
				{
					LockScope: internal.LockScope{Exclusive: &struct{}{}},
					LockType:  internal.LockType{Write: &struct{}{}},
				},
				{
					LockScope: internal.LockScope{Shared: &struct{}{}},
					LockType:  internal.LockType{Write: &struct{}{}},
				},
			},
		})
		props[internal.LockDiscoveryName] = func(*internal.RawXMLValue) (interface{}, error) {
			return locks.discovery(fi)
		}
	}

	if !fi.IsDir {
		props[internal.GetContentLengthName] = internal.PropFindValue(&internal.GetContentLength{
			Length: fi.Size,
		})

		if !fi.ModTime.IsZero() {
			props[internal.GetLastModifiedName] = internal.PropFindValue(&internal.GetLastModified{
				LastModified: internal.Time(fi.ModTime),
			})
		}

		if fi.MIMEType != "" {
			props[internal.GetContentTypeName] = internal.PropFindValue(&internal.GetContentType{
				Type: fi.MIMEType,
			})
		}

		if fi.ETag != "" {
			props[internal.GetETagName] = internal.PropFindValue(&internal.GetETag{
				ETag: internal.ETag(fi.ETag),
			})
		}
	}

	var deadProps map[xml.Name]internal.PropFindFunc
	if fs, ok := b.FileSystem.(DeadPropertyFileSystem); ok && needsDeadProps(propfind, props) {
		l, err := fs.DeadProperties(ctx, fi.Path)
		if err != nil {
			return nil, err
		}
		deadProps = make(map[xml.Name]internal.PropFindFunc, len(l))
		for i := range l {
			prop := &l[i]
			if prop.XMLName.Local == "" {
				return nil, fmt.Errorf("webdav: dead property without a name")
			} else if isProtectedProp(prop.XMLName) {
				continue
			}
			// Check the value now, rather than fail in the middle of the
			// response
			if raw, err := prop.rawXMLValue(); err != nil {
				deadProps[prop.XMLName] = func(*internal.RawXMLValue) (interface{}, error) {
					return nil, err
				}
			} else {
				deadProps[prop.XMLName] = internal.PropFindValue(raw)
			}
		}
	}

	return internal.NewPropFindResponseWithDeadProps(fi.Path, propfind, props, deadProps)
}

// isProtectedProp reports whether a property is a live property which
// clients cannot modify: the properties in the DAV: namespace, which WebDAV
// specifications define, except DAV:displayname (RFC 4918 section 15.2).
func isProtectedProp(name xml.Name) bool {
	return name.Space == internal.Namespace && name != internal.DisplayNameName
}

// needsDeadProps reports whether a PROPFIND request may return dead
// properties, besides the live properties props.
func needsDeadProps(propfind *internal.PropFind, props map[xml.Name]internal.PropFindFunc) bool {
	if propfind.Prop == nil {
		// allprop or propname
		return true
	}
	for _, raw := range propfind.Prop.Raw {
		name, ok := raw.XMLName()
		if !ok || isProtectedProp(name) {
			continue
		}
		if _, live := props[name]; !live {
			return true
		}
	}
	return false
}

func (b *backend) PropPatch(r *http.Request, update *internal.PropertyUpdate) (*internal.Response, error) {
	if b.locks != nil {
		if err := b.checkLocks(r, resourceTarget(r.URL.Path)); err != nil {
			return nil, err
		}
	}

	fi, err := b.FileSystem.Stat(r.Context(), r.URL.Path)
	if err != nil {
		return nil, err
	}

	fs, ok := b.FileSystem.(DeadPropertyFileSystem)
	if !ok {
		return internal.NewPropPatchResponse(fi.Path, update, http.StatusForbidden)
	}

	var patch []DeadPropertyPatch
	f := func(raw *internal.RawXMLValue, remove bool) error {
		name, _ := raw.XMLName()
		if isProtectedProp(name) {
			// RFC 4918 section 9.2.1
			return internal.NewProtectedPropError()
		}
		p := DeadPropertyPatch{Remove: remove, Property: DeadProperty{XMLName: name}}
		if !remove {
			prop, err := newDeadProperty(raw)
			if err != nil {
				return err
			}
			p.Property = *prop
		}
		patch = append(patch, p)
		return nil
	}
	commit := func() error {
		return fs.PatchDeadProperties(r.Context(), r.URL.Path, patch)
	}
	return internal.PatchProps(fi.Path, update, f, commit)
}

func (b *backend) Put(w http.ResponseWriter, r *http.Request) error {
	if b.locks != nil {
		if err := b.checkWriteLocks(r, r.URL.Path); err != nil {
			return err
		}
	}

	ifNoneMatch := ConditionalMatch(r.Header.Get("If-None-Match"))
	ifMatch := ConditionalMatch(r.Header.Get("If-Match"))

	opts := CreateOptions{
		IfNoneMatch: ifNoneMatch,
		IfMatch:     ifMatch,
	}
	fi, created, err := b.FileSystem.Create(r.Context(), r.URL.Path, r.Body, &opts)
	if err != nil {
		return err
	}

	if fi.MIMEType != "" {
		w.Header().Set("Content-Type", fi.MIMEType)
	}
	if !fi.ModTime.IsZero() {
		w.Header().Set("Last-Modified", fi.ModTime.UTC().Format(http.TimeFormat))
	}
	if fi.ETag != "" {
		w.Header().Set("ETag", internal.ETag(fi.ETag).String())
	}

	if created {
		w.WriteHeader(http.StatusCreated)
	} else {
		w.WriteHeader(http.StatusNoContent)
	}

	return nil
}

func (b *backend) Delete(r *http.Request) error {
	var locks []Lock
	if b.locks != nil {
		if err := b.checkLocks(r, treeTarget(r.URL.Path), parentTarget(r.URL.Path)); err != nil {
			return err
		}
		var err error
		if locks, err = b.treeLocks(r, r.URL.Path); err != nil {
			return err
		}
	}

	ifNoneMatch := ConditionalMatch(r.Header.Get("If-None-Match"))
	ifMatch := ConditionalMatch(r.Header.Get("If-Match"))

	opts := RemoveAllOptions{
		IfNoneMatch: ifNoneMatch,
		IfMatch:     ifMatch,
	}
	err := b.FileSystem.RemoveAll(r.Context(), r.URL.Path, &opts)
	if b.locks != nil {
		b.removeLocks(r, locks, "", err)
	}
	return err
}

func (b *backend) Mkcol(r *http.Request) error {
	// FileSystem.Mkdir creates an empty collection: any request body is one
	// the server doesn't understand (RFC 4918 section 9.3). Whether there's
	// one doesn't depend on the Content-Type header, which a client may send
	// without a body, or omit along with one.
	if !internal.IsRequestBodyEmpty(r) {
		return internal.HTTPErrorf(http.StatusUnsupportedMediaType, "webdav: request body not supported in MKCOL request")
	}
	if b.locks != nil {
		if err := b.checkLocks(r, resourceTarget(r.URL.Path), parentTarget(r.URL.Path)); err != nil {
			return err
		}
	}
	err := b.FileSystem.Mkdir(r.Context(), r.URL.Path)
	if internal.IsNotFound(err) {
		return &internal.HTTPError{Code: http.StatusConflict, Err: err}
	}
	return err
}

func (b *backend) Copy(r *http.Request, dest *internal.Href, recursive, overwrite bool) (created bool, err error) {
	var destLocks []Lock
	if b.locks != nil {
		// The destination is overwritten as if deleted first (RFC 4918
		// section 9.8.4)
		if err := b.checkLocks(r, treeTarget(dest.Path), parentTarget(dest.Path)); err != nil {
			return false, err
		}
		if destLocks, err = b.treeLocks(r, dest.Path); err != nil {
			return false, err
		}
	}

	options := CopyOptions{
		NoRecursive: !recursive,
		NoOverwrite: !overwrite,
	}
	created, err = b.FileSystem.Copy(r.Context(), r.URL.Path, dest.Path, &options)
	if os.IsExist(err) {
		return false, &internal.HTTPError{http.StatusPreconditionFailed, err}
	}
	if b.locks != nil {
		// Locks aren't copied, and those of the members of an overwritten
		// destination are gone with them
		b.removeLocks(r, destLocks, path.Clean(dest.Path), err)
	}
	return created, err
}

func (b *backend) Move(r *http.Request, dest *internal.Href, overwrite bool) (created bool, err error) {
	var srcLocks, destLocks []Lock
	if b.locks != nil {
		// The source is removed from its collection, and the destination is
		// overwritten as if deleted first (RFC 4918 section 9.9.3)
		err := b.checkLocks(r,
			treeTarget(r.URL.Path), parentTarget(r.URL.Path),
			treeTarget(dest.Path), parentTarget(dest.Path))
		if err != nil {
			return false, err
		}
		if srcLocks, err = b.treeLocks(r, r.URL.Path); err != nil {
			return false, err
		}
		if destLocks, err = b.treeLocks(r, dest.Path); err != nil {
			return false, err
		}
	}

	options := MoveOptions{
		NoOverwrite: !overwrite,
	}
	created, err = b.FileSystem.Move(r.Context(), r.URL.Path, dest.Path, &options)
	if os.IsExist(err) {
		return false, &internal.HTTPError{http.StatusPreconditionFailed, err}
	}
	if b.locks != nil {
		// Locks aren't moved with the resources (RFC 4918 section 7.6), and
		// those of the members of an overwritten destination are gone with
		// them
		b.removeLocks(r, srcLocks, "", err)
		b.removeLocks(r, destLocks, path.Clean(dest.Path), err)
	}
	return created, err
}

// BackendSuppliedHomeSet represents either a CalDAV calendar-home-set or a
// CardDAV addressbook-home-set. It should only be created via
// caldav.NewCalendarHomeSet or carddav.NewAddressBookHomeSet. Only to
// be used server-side, for listing a user's home sets as determined by the
// (external) backend.
type BackendSuppliedHomeSet interface {
	GetXMLName() xml.Name
}

// UserPrincipalBackend can determine the current user's principal URL for a
// given request context.
type UserPrincipalBackend interface {
	CurrentUserPrincipal(ctx context.Context) (string, error)
}

// Capability indicates the features that a server supports.
type Capability string

// ServePrincipalOptions holds options for ServePrincipal.
type ServePrincipalOptions struct {
	// CurrentUserPrincipalPath is the path of the principal of the current
	// user, reported in DAV:current-user-principal. It needn't be the
	// principal served, which is the one at the request URL. If empty, the
	// user isn't authenticated: DAV:current-user-principal is then
	// DAV:unauthenticated (RFC 5397 section 3).
	CurrentUserPrincipalPath string
	HomeSets                 []BackendSuppliedHomeSet
	Capabilities             []Capability
	// DisplayName is the DAV:displayname of the principal. If empty, the
	// last segment of the principal's path is used: RFC 3744 section 4
	// requires principals to have a non-empty display name.
	DisplayName string
	// Push, if set, supports WebDAV-Push (draft-bitfire-webdav-push-00) for
	// the principal, so that clients can subscribe to changes of its
	// properties, such as DAV:displayname: the draft recommends it (section
	// 5). It can be the push backend of the CalDAV and CardDAV servers, see
	// PushBackend.
	//
	// If Push.PushInfo returns information for the principal, ServePrincipal
	// advertises the webdav-push capability and the POST method in OPTIONS
	// responses, serves the transports, topic and supported-triggers
	// properties, and registers push subscriptions, POST requests with a
	// push-register body, with Push.RegisterPush. It also offers every
	// DELETE request to Push.UnregisterPush, which removes the registration
	// if the request is for a registration URL: the server can route the
	// registration URLs to ServePrincipal, or to a caldav.Handler or
	// carddav.Handler with the same push backend. The methods are given the
	// path of the request URL.
	Push PushBackend
}

// ServePrincipal replies to requests for a principal URL. The principal
// served is the one at the request URL, which may be a principal other than
// the current user's. Its DAV:principal-URL is the path of the request URL,
// or CurrentUserPrincipalPath if they only differ by a trailing slash.
//
// Along with the principal properties of RFC 3744 section 4, the principal
// reports the access control properties DAV:owner, itself,
// DAV:principal-collection-set, the collection holding it, and
// DAV:supported-privilege-set (RFC 3744 section 5).
//
// The principal has no members and no content: it supports the OPTIONS and
// PROPFIND methods only, and POST if it supports WebDAV-Push (see
// ServePrincipalOptions.Push). A nil options is the same as empty options.
func ServePrincipal(w http.ResponseWriter, r *http.Request, options *ServePrincipalOptions) {
	if options == nil {
		options = &ServePrincipalOptions{}
	}

	var err error
	switch r.Method {
	case http.MethodOptions:
		err = servePrincipalOptions(w, r, options)
	case "PROPFIND":
		err = servePrincipalPropfind(w, r, options)
	default:
		var handled bool
		if handled, err = servePrincipalPush(w, r, options); err == nil && !handled {
			err = servePrincipalMethodNotAllowed(w, r, options)
		}
	}
	if err != nil {
		internal.ServeError(w, err)
	}
}

// principalAllow returns the methods the principal at the request URL
// supports, for the Allow header field (RFC 9110 section 10.2.1), and its
// push information: nil if it doesn't support WebDAV-Push.
func principalAllow(r *http.Request, options *ServePrincipalOptions) (string, *PushInfo, error) {
	methods := []string{http.MethodOptions, "PROPFIND"}
	var info *PushInfo
	if options.Push != nil {
		// With push information that can't be served as the draft
		// requires, the principal doesn't support WebDAV-Push: the push
		// properties and registrations get the error
		var err error
		if info, err = push.Info(r.Context(), options.Push, r.URL.Path); err != nil && !errors.Is(err, push.ErrInvalidInfo) {
			return "", nil, err
		}
	}
	if info != nil {
		methods = append(methods, http.MethodPost)
	}
	return strings.Join(methods, ", "), info, nil
}

func servePrincipalOptions(w http.ResponseWriter, r *http.Request, options *ServePrincipalOptions) error {
	allow, info, err := principalAllow(r, options)
	if err != nil {
		return err
	}
	caps := []string{"1", "3"}
	for _, c := range options.Capabilities {
		caps = append(caps, string(c))
	}
	if info != nil {
		// draft-bitfire-webdav-push section 2
		caps = append(caps, string(CapabilityPush))
	}
	w.Header().Add("DAV", strings.Join(caps, ", "))
	w.Header().Add("Allow", allow)
	w.WriteHeader(http.StatusNoContent)
	return nil
}

func servePrincipalMethodNotAllowed(w http.ResponseWriter, r *http.Request, options *ServePrincipalOptions) error {
	allow, _, err := principalAllow(r, options)
	if err != nil {
		return err
	}
	// A 405 response lists the supported methods (RFC 9110 section 15.5.6)
	w.Header().Set("Allow", allow)
	http.Error(w, "unsupported method", http.StatusMethodNotAllowed)
	return nil
}

// servePrincipalPush serves the WebDAV-Push requests, if options.Push is
// set: POST requests, which register push subscriptions for the principal,
// and DELETE requests for registration URLs. It reports false for other
// requests.
func servePrincipalPush(w http.ResponseWriter, r *http.Request, options *ServePrincipalOptions) (bool, error) {
	if options.Push == nil {
		return false, nil
	}
	switch r.Method {
	case http.MethodPost:
		r, err := internal.DecodeRequestBody(w, r)
		if err != nil {
			return true, err
		}
		info, err := push.Info(r.Context(), options.Push, r.URL.Path)
		if err != nil {
			return true, err
		}
		return true, push.ServeRegister(w, r, options.Push, info)
	case http.MethodDelete:
		return push.ServeUnregister(w, r, options.Push)
	}
	return false, nil
}

func servePrincipalPropfind(w http.ResponseWriter, r *http.Request, options *ServePrincipalOptions) error {
	r, err := internal.DecodeRequestBody(w, r)
	if err != nil {
		return err
	}

	var propfind internal.PropFind
	if internal.IsRequestBodyEmpty(r) {
		// An empty body is an allprop request, whatever its Content-Type
		// (RFC 4918 section 9.1)
		propfind.AllProp = &struct{}{}
	} else if err := internal.DecodeXMLRequest(r, &propfind); err != nil {
		return err
	}

	// The principal has no members, so every depth returns the principal
	// alone, but an invalid one is refused, as by Handler
	if s := r.Header.Get("Depth"); s != "" {
		if _, err := internal.ParseDepth(s); err != nil {
			return &internal.HTTPError{Code: http.StatusBadRequest, Err: err}
		}
	}

	// The principal served is the one at the request URL, which needn't be
	// the current user's: its DAV:principal-URL and DAV:owner are its own
	// path (RFC 3744 sections 4.2 and 5.1). For the current user's
	// principal, requested with or without its trailing slash, that's
	// CurrentUserPrincipalPath: DAV:current-user-principal must be one of
	// its URLs (RFC 5397 section 3).
	principalPath := r.URL.Path
	if internal.SamePath(principalPath, options.CurrentUserPrincipalPath) {
		principalPath = options.CurrentUserPrincipalPath
	}

	props := map[xml.Name]internal.PropFindFunc{
		internal.ResourceTypeName:         internal.PropFindValue(internal.NewResourceType(internal.PrincipalName)),
		internal.CurrentUserPrincipalName: internal.PropFindValue(internal.NewCurrentUserPrincipal(options.CurrentUserPrincipalPath)),
	}

	internal.AddPrincipalProps(props, principalPath, options.DisplayName)
	// The principal owns itself
	internal.AddACLProps(props, func() (string, error) {
		return principalPath, nil
	})

	for _, homeSet := range options.HomeSets {
		props[homeSet.GetXMLName()] = internal.PropFindValue(homeSet)
	}

	if options.Push != nil {
		push.AddProps(r.Context(), options.Push, &propfind, r.URL.Path, props)
	}

	resp, err := internal.NewPropFindResponse(r.URL.Path, &propfind, props)
	if err != nil {
		return err
	}

	ms := internal.NewMultiStatus(*resp)
	return internal.ServeMultiStatus(w, ms)
}
