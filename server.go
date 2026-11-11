package webdav

import (
	"context"
	"encoding/xml"
	"io"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"

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

// Handler handles WebDAV HTTP requests. It can be used to create a WebDAV
// server.
type Handler struct {
	FileSystem FileSystem
}

// ServeHTTP implements http.Handler.
func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if h.FileSystem == nil {
		http.Error(w, "webdav: no filesystem available", http.StatusInternalServerError)
		return
	}

	b := backend{h.FileSystem}
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
}

func (b *backend) Options(r *http.Request) (caps []string, allow []string, err error) {
	fi, err := b.FileSystem.Stat(r.Context(), r.URL.Path)
	if internal.IsNotFound(err) {
		return nil, []string{http.MethodOptions, http.MethodPut, "MKCOL"}, nil
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

	if !fi.IsDir {
		allow = append(allow, http.MethodHead, http.MethodGet, http.MethodPut)
	}

	return nil, allow, nil
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
			resp, err := b.propFindFile(propfind, &child)
			if err != nil {
				return nil, err
			}
			resps = append(resps, *resp)
		}
		resps = append(resps, errResps...)
	} else {
		resp, err := b.propFindFile(propfind, fi)
		if err != nil {
			return nil, err
		}

		resps = []internal.Response{*resp}
	}

	return internal.NewMultiStatus(resps...), nil
}

func (b *backend) propFindFile(propfind *internal.PropFind, fi *FileInfo) (*internal.Response, error) {
	props := make(map[xml.Name]internal.PropFindFunc)

	props[internal.ResourceTypeName] = func(*internal.RawXMLValue) (interface{}, error) {
		var types []xml.Name
		if fi.IsDir {
			types = append(types, internal.CollectionName)
		}
		return internal.NewResourceType(types...), nil
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

	return internal.NewPropFindResponse(fi.Path, propfind, props)
}

func (b *backend) PropPatch(r *http.Request, update *internal.PropertyUpdate) (*internal.Response, error) {
	fi, err := b.FileSystem.Stat(r.Context(), r.URL.Path)
	if err != nil {
		return nil, err
	}

	return internal.NewPropPatchResponse(fi.Path, update, http.StatusForbidden)
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
	return b.FileSystem.RemoveAll(r.Context(), r.URL.Path, &opts)
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
