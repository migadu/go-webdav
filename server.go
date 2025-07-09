package webdav

import (
	"context"
	"encoding/xml"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"

	"github.com/emersion/go-webdav/internal"
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
	// properties, for each file it lists. Properties in the DAV: namespace
	// other than DAV:displayname are ignored. An error fails the whole
	// request.
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

	locks   map[string]*internal.Lock
	locksMu sync.Mutex
}

// ServeHTTP implements http.Handler.
func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if h.FileSystem == nil {
		http.Error(w, "webdav: no filesystem available", http.StatusInternalServerError)
		return
	}

	h.locksMu.Lock()
	if h.locks == nil {
		h.locks = make(map[string]*internal.Lock)
	}
	h.locksMu.Unlock()

	b := backend{FileSystem: h.FileSystem, locks: h.locks, locksMu: &h.locksMu}
	hh := internal.Handler{Backend: &b}
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
// fails the whole request, like any other error.
func NewHrefError(path string, err error) error {
	return &internal.HrefError{Href: url.URL{Path: path}, Err: err}
}

type backend struct {
	FileSystem FileSystem

	locks   map[string]*internal.Lock
	locksMu *sync.Mutex
}

func (b *backend) Options(r *http.Request) (caps []string, allow []string, err error) {
	caps = []string{"2"}

	fi, err := b.FileSystem.Stat(r.Context(), r.URL.Path)
	if internal.IsNotFound(err) {
		return caps, []string{http.MethodOptions, http.MethodPut, "MKCOL"}, nil
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

func (b *backend) PropFind(r *http.Request, propfind *internal.PropFind, depth internal.Depth) (*internal.MultiStatus, error) {
	fi, err := b.FileSystem.Stat(r.Context(), r.URL.Path)
	if err != nil {
		return nil, err
	}

	var resps []internal.Response
	if depth != internal.DepthZero && fi.IsDir {
		children, err := b.FileSystem.ReadDir(r.Context(), r.URL.Path, depth == internal.DepthInfinity)
		errResps, err := internal.HrefErrorResponses(err)
		if err != nil {
			return nil, err
		}

		resps = make([]internal.Response, 0, len(children)+len(errResps))
		for _, child := range children {
			resp, err := b.propFindFile(r.Context(), propfind, &child)
			if err != nil {
				return nil, err
			}
			resps = append(resps, *resp)
		}
		resps = append(resps, errResps...)
	} else {
		resp, err := b.propFindFile(r.Context(), propfind, fi)
		if err != nil {
			return nil, err
		}

		resps = []internal.Response{*resp}
	}

	return internal.NewMultiStatus(resps...), nil
}

func (b *backend) propFindFile(ctx context.Context, propfind *internal.PropFind, fi *FileInfo) (*internal.Response, error) {
	props := make(map[xml.Name]internal.PropFindFunc)

	props[internal.ResourceTypeName] = func(*internal.RawXMLValue) (interface{}, error) {
		var types []xml.Name
		if fi.IsDir {
			types = append(types, internal.CollectionName)
		}
		return internal.NewResourceType(types...), nil
	}

	props[internal.SupportedLockName] = internal.PropFindValue(&internal.SupportedLock{
		LockEntries: []internal.LockEntry{{
			LockScope: internal.LockScope{Exclusive: &struct{}{}},
			LockType:  internal.LockType{Write: &struct{}{}},
		}},
	})

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

var cannotModifyProtectedPropertyName = xml.Name{internal.Namespace, "cannot-modify-protected-property"}

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
			return &internal.HTTPError{
				Code: http.StatusForbidden,
				Err: &internal.Error{Raw: []internal.RawXMLValue{
					*internal.NewRawXMLElement(cannotModifyProtectedPropertyName, nil, nil),
				}},
			}
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
	ifNoneMatch := ConditionalMatch(r.Header.Get("If-None-Match"))
	ifMatch := ConditionalMatch(r.Header.Get("If-Match"))

	opts := RemoveAllOptions{
		IfNoneMatch: ifNoneMatch,
		IfMatch:     ifMatch,
	}
	err := b.FileSystem.RemoveAll(r.Context(), r.URL.Path, &opts)
	if err == nil {
		// URL became unmapped so delete lock if exists
		b.locksMu.Lock()
		defer b.locksMu.Unlock()
		delete(b.locks, r.URL.Path)
	}

	return err
}

func (b *backend) Mkcol(r *http.Request) error {
	if r.Header.Get("Content-Type") != "" {
		return internal.HTTPErrorf(http.StatusUnsupportedMediaType, "webdav: request body not supported in MKCOL request")
	}
	err := b.FileSystem.Mkdir(r.Context(), r.URL.Path)
	if internal.IsNotFound(err) {
		return &internal.HTTPError{Code: http.StatusConflict, Err: err}
	}
	return err
}

func (b *backend) Copy(r *http.Request, dest *internal.Href, recursive, overwrite bool) (created bool, err error) {
	options := CopyOptions{
		NoRecursive: !recursive,
		NoOverwrite: !overwrite,
	}
	created, err = b.FileSystem.Copy(r.Context(), r.URL.Path, dest.Path, &options)
	if os.IsExist(err) {
		return false, &internal.HTTPError{http.StatusPreconditionFailed, err}
	}
	return created, err
}

func (b *backend) Move(r *http.Request, dest *internal.Href, overwrite bool) (created bool, err error) {
	options := MoveOptions{
		NoOverwrite: !overwrite,
	}
	created, err = b.FileSystem.Move(r.Context(), r.URL.Path, dest.Path, &options)
	if os.IsExist(err) {
		return false, &internal.HTTPError{http.StatusPreconditionFailed, err}
	}
	if err == nil {
		// URL became unmapped so delete lock if exists
		b.locksMu.Lock()
		defer b.locksMu.Unlock()
		delete(b.locks, r.URL.Path)
	}
	return created, err
}

func (b *backend) Lock(r *http.Request, depth internal.Depth, timeout time.Duration, refreshToken string) (lock *internal.Lock, created bool, err error) {
	// TODO: locking unmapped URLs
	fi, err := b.FileSystem.Stat(r.Context(), r.URL.Path)
	if err != nil {
		return nil, false, err
	}
	if fi.IsDir {
		return nil, false, internal.HTTPErrorf(http.StatusBadRequest, "webdav: locking collections is not supported")
	}

	if refreshToken != "" {
		if lock := b.resourceLock(r.URL.Path); lock == nil || lock.Href != refreshToken {
			return nil, false, &internal.HTTPError{Code: http.StatusPreconditionFailed}
		} else {
			// Lock timeout is not supported so refresh is a no-op
			return lock, false, nil
		}
	}

	token := "opaquelocktoken:" + uuid.NewString()
	lock = &internal.Lock{Href: token, Root: fi.Path, Timeout: 0 /* infinity */}

	b.locksMu.Lock()
	defer b.locksMu.Unlock()
	if _, prs := b.locks[r.URL.Path]; prs {
		return nil, false, internal.HTTPErrorf(http.StatusLocked, "webdav: there is already a lock on this resource")
	}
	b.locks[lock.Root] = lock

	return lock, false, nil
}

func (b *backend) Unlock(r *http.Request, tokenHref string) error {
	if lock := b.resourceLock(r.URL.Path); lock == nil {
		return internal.HTTPErrorf(http.StatusConflict, "webdav: resource is not locked")
	} else if lock.Href != tokenHref {
		return internal.HTTPErrorf(http.StatusForbidden, "webdav: incorrect token")
	}

	b.locksMu.Lock()
	defer b.locksMu.Unlock()
	delete(b.locks, r.URL.Path)

	return nil
}

func (b *backend) resourceLock(path string) *internal.Lock {
	b.locksMu.Lock()
	defer b.locksMu.Unlock()

	lock, prs := b.locks[path]
	if !prs {
		return nil
	}

	return lock
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
	CurrentUserPrincipalPath string
	HomeSets                 []BackendSuppliedHomeSet
	Capabilities             []Capability
}

// ServePrincipal replies to requests for a principal URL.
func ServePrincipal(w http.ResponseWriter, r *http.Request, options *ServePrincipalOptions) {
	switch r.Method {
	case http.MethodOptions:
		caps := []string{"1", "3"}
		for _, c := range options.Capabilities {
			caps = append(caps, string(c))
		}
		allow := []string{http.MethodOptions, "PROPFIND", "REPORT", "DELETE", "MKCOL"}
		w.Header().Add("DAV", strings.Join(caps, ", "))
		w.Header().Add("Allow", strings.Join(allow, ", "))
		w.WriteHeader(http.StatusNoContent)
	case "PROPFIND":
		if err := servePrincipalPropfind(w, r, options); err != nil {
			internal.ServeError(w, err)
		}
	default:
		http.Error(w, "unsupported method", http.StatusMethodNotAllowed)
	}
}

func servePrincipalPropfind(w http.ResponseWriter, r *http.Request, options *ServePrincipalOptions) error {
	r, err := internal.DecodeRequestBody(w, r)
	if err != nil {
		return err
	}

	var propfind internal.PropFind
	if err := internal.DecodeXMLRequest(r, &propfind); err != nil {
		return err
	}
	props := map[xml.Name]internal.PropFindFunc{
		internal.ResourceTypeName: internal.PropFindValue(internal.NewResourceType(internal.PrincipalName)),
		internal.CurrentUserPrincipalName: internal.PropFindValue(&internal.CurrentUserPrincipal{
			Href: internal.Href{Path: options.CurrentUserPrincipalPath},
		}),
	}

	// TODO: handle Depth and more properties

	for _, homeSet := range options.HomeSets {
		props[homeSet.GetXMLName()] = internal.PropFindValue(homeSet)
	}

	resp, err := internal.NewPropFindResponse(r.URL.Path, &propfind, props)
	if err != nil {
		return err
	}

	ms := internal.NewMultiStatus(*resp)
	return internal.ServeMultiStatus(w, ms)
}
