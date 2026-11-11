package webdav

import (
	"context"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/emersion/go-webdav/internal"
)

// HTTPClient performs HTTP requests. It's implemented by *http.Client.
type HTTPClient interface {
	Do(req *http.Request) (*http.Response, error)
}

type basicAuthHTTPClient struct {
	c                  HTTPClient
	username, password string
}

func (c *basicAuthHTTPClient) Do(req *http.Request) (*http.Response, error) {
	req.SetBasicAuth(c.username, c.password)
	return c.c.Do(req)
}

// HTTPClientWithBasicAuth returns an HTTP client that adds basic
// authentication to all outgoing requests. If c is nil, http.DefaultClient is
// used.
func HTTPClientWithBasicAuth(c HTTPClient, username, password string) HTTPClient {
	if c == nil {
		c = http.DefaultClient
	}
	return &basicAuthHTTPClient{c, username, password}
}

// Client provides access to a remote WebDAV filesystem.
type Client struct {
	ic *internal.Client
}

// NewClient creates a new WebDAV client.
//
// If the HTTPClient is nil, http.DefaultClient is used.
//
// To use HTTP basic authentication, HTTPClientWithBasicAuth can be used.
//
// The paths the client returns, such as FileInfo.Path, are paths on the
// endpoint's host, except for the resources a server lists on another host:
// these are returned as absolute URLs, e.g.
// "https://p37-contacts.icloud.com/1234567/carddavhome/". So are the resources
// whose href has a query, which a path can't carry. The methods taking a path
// also take such a URL, and send the requests for it to its host: join it with
// url.JoinPath rather than path.Join, which would mangle its "//". Credentials
// in the endpoint URL are only sent to the endpoint's scheme and host, but an
// HTTPClient adding credentials to its requests, such as the one returned by
// HTTPClientWithBasicAuth, adds them for any host.
//
// http.Client follows a 301 Moved Permanently or 302 Found redirect with a GET
// request, which RFC 9110 only allows for a POST request. A request of the
// client redirected this way, e.g. a PROPFIND request for a collection without
// its trailing slash, is sent again to the redirect target with its own
// method, and the response to the GET request is discarded. The body of a file
// written with Create can't be sent again: such a redirect is returned by its
// Close, as an *HTTPError with the redirect status. A request for an https URL
// isn't sent again to an http one either, which would send its credentials
// and body in cleartext: the redirect is returned as an *HTTPError too.
func NewClient(c HTTPClient, endpoint string) (*Client, error) {
	ic, err := internal.NewClient(c, endpoint)
	if err != nil {
		return nil, err
	}
	return &Client{ic}, nil
}

// FindCurrentUserPrincipal finds the current user's principal path.
func (c *Client) FindCurrentUserPrincipal(ctx context.Context) (string, error) {
	propfind := internal.NewPropNamePropFind(internal.CurrentUserPrincipalName)

	// TODO: consider retrying on the root URI "/" if this fails, as suggested
	// by the RFC?
	resp, err := c.ic.PropFindFlat(ctx, "", propfind)
	if err != nil {
		return "", err
	}

	var prop internal.CurrentUserPrincipal
	if err := resp.DecodeProp(&prop); err != nil {
		return "", err
	}
	if prop.Unauthenticated != nil {
		return "", fmt.Errorf("webdav: unauthenticated")
	}

	return resp.HrefPath(&prop.Href), nil
}

var fileInfoPropFind = internal.NewPropNamePropFind(
	internal.ResourceTypeName,
	internal.GetContentLengthName,
	internal.GetLastModifiedName,
	internal.GetContentTypeName,
	internal.GetETagName,
)

func fileInfoFromResponse(resp *internal.Response) (*FileInfo, error) {
	path, err := resp.Path()
	if err != nil {
		return nil, err
	}

	fi := &FileInfo{Path: path}

	var resType internal.ResourceType
	if err := resp.DecodeProp(&resType); err != nil {
		return nil, err
	}

	if resType.Is(internal.CollectionName) {
		fi.IsDir = true
	} else {
		var getLen internal.GetContentLength
		if err := resp.DecodeProp(&getLen); err != nil {
			return nil, err
		}

		var getType internal.GetContentType
		if err := resp.DecodeProp(&getType); err != nil && !internal.IsNotFound(err) {
			return nil, err
		}

		var getETag internal.GetETag
		if err := resp.DecodeProp(&getETag); err != nil && !internal.IsNotFound(err) {
			return nil, err
		}

		fi.Size = getLen.Length
		fi.MIMEType = getType.Type
		fi.ETag = string(getETag.ETag)
	}

	var getMod internal.GetLastModified
	if err := resp.DecodeProp(&getMod); err != nil && !internal.IsNotFound(err) {
		return nil, err
	}
	fi.ModTime = time.Time(getMod.LastModified)

	return fi, nil
}

// Stat fetches a FileInfo for a single file.
func (c *Client) Stat(ctx context.Context, name string) (*FileInfo, error) {
	resp, err := c.ic.PropFindFlat(ctx, name, fileInfoPropFind)
	if err != nil {
		return nil, err
	}
	return fileInfoFromResponse(resp)
}

// Open fetches a file's contents.
func (c *Client) Open(ctx context.Context, name string) (io.ReadCloser, error) {
	req, err := c.ic.NewRequest(http.MethodGet, name, nil)
	if err != nil {
		return nil, err
	}

	resp, err := c.ic.Do(req.WithContext(ctx))
	if err != nil {
		return nil, err
	}

	return resp.Body, nil
}

// ReadDir lists files in a directory.
func (c *Client) ReadDir(ctx context.Context, name string, recursive bool) ([]FileInfo, error) {
	depth := internal.DepthOne
	if recursive {
		depth = internal.DepthInfinity
	}

	// Each response is converted as soon as it's decoded: only the FileInfo
	// is kept
	l := []FileInfo{}
	var errs []error
	_, err := c.ic.PropFindFunc(ctx, name, depth, fileInfoPropFind, func(resp *internal.Response) error {
		fi, err := fileInfoFromResponse(resp)
		if err != nil {
			errs = append(errs, err)
		} else {
			l = append(l, *fi)
		}
		return nil
	})
	if err != nil {
		// Not the files received so far: they may look like the whole
		// listing
		return nil, err
	}

	return l, errors.Join(errs...)
}

type fileWriter struct {
	pw *io.PipeWriter

	// done is closed when the request completes, after err is set
	done chan struct{}
	err  error
}

func (fw *fileWriter) Write(b []byte) (int, error) {
	n, err := fw.pw.Write(b)
	if err == io.ErrClosedPipe {
		// The request ended before the whole body was sent, e.g. because the
		// server refused it without reading it: report why
		if reqErr := fw.wait(); reqErr != nil {
			err = reqErr
		}
	}
	return n, err
}

func (fw *fileWriter) Close() error {
	if err := fw.pw.Close(); err != nil {
		return err
	}
	return fw.wait()
}

// wait waits for the request to complete and returns its error. It's safe to
// call concurrently, e.g. from a Write blocked when Close is called.
func (fw *fileWriter) wait() error {
	<-fw.done
	return fw.err
}

// Create writes a file's contents.
//
// A nil opts writes the file unconditionally. Otherwise, opts.IfMatch only
// overwrites the file if its ETag still matches, and opts.IfNoneMatch "*" only
// creates it if it doesn't exist yet. The values are sent as is, so they must
// be in header form: a quoted strong entity-tag such as "abc", a weak one such
// as W/"abc", a comma-separated list of these, or "*". If-Match uses the strong
// comparison, so a weak entity-tag never matches it. A failed precondition is
// returned by Close, as an *HTTPError with code 412 Precondition Failed.
func (c *Client) Create(ctx context.Context, name string, opts *CreateOptions) (io.WriteCloser, error) {
	pr, pw := io.Pipe()

	req, err := c.ic.NewRequest(http.MethodPut, name, pr)
	if err != nil {
		pw.Close()
		return nil, err
	}
	if opts != nil {
		if opts.IfMatch.IsSet() {
			req.Header.Set("If-Match", string(opts.IfMatch))
		}
		if opts.IfNoneMatch.IsSet() {
			req.Header.Set("If-None-Match", string(opts.IfNoneMatch))
		}
	}

	fw := &fileWriter{pw: pw, done: make(chan struct{})}
	go func() {
		defer close(fw.done)
		resp, err := c.ic.Do(req.WithContext(ctx))
		if err != nil {
			fw.err = err
			return
		}
		resp.Body.Close()
	}()

	return fw, nil
}

// RemoveAll deletes a file. If the file is a directory, all of its descendants
// are recursively deleted as well.
//
// If the server fails to delete some of the descendants, the returned error
// lists each of them.
func (c *Client) RemoveAll(ctx context.Context, name string) error {
	req, err := c.ic.NewRequest(http.MethodDelete, name, nil)
	if err != nil {
		return err
	}

	return c.ic.DoAndCheckMultiStatus(req.WithContext(ctx))
}

// Mkdir creates a new directory.
func (c *Client) Mkdir(ctx context.Context, name string) error {
	req, err := c.ic.NewRequest("MKCOL", name, nil)
	if err != nil {
		return err
	}

	resp, err := c.ic.Do(req.WithContext(ctx))
	if err != nil {
		return err
	}
	resp.Body.Close()
	return nil
}

// Copy copies a file.
//
// By default, if the file is a directory, all descendants are recursively
// copied as well. If the server fails to copy some of them, the returned error
// lists each of them.
func (c *Client) Copy(ctx context.Context, name, dest string, options *CopyOptions) error {
	if options == nil {
		options = new(CopyOptions)
	}

	req, err := c.ic.NewRequest("COPY", name, nil)
	if err != nil {
		return err
	}

	depth := internal.DepthInfinity
	if options.NoRecursive {
		depth = internal.DepthZero
	}

	destURL, err := c.ic.ResolveHref(dest)
	if err != nil {
		return err
	}

	req.Header.Set("Destination", destURL.String())
	req.Header.Set("Overwrite", internal.FormatOverwrite(!options.NoOverwrite))
	req.Header.Set("Depth", depth.String())

	return c.ic.DoAndCheckMultiStatus(req.WithContext(ctx))
}

// DeadProperties fetches the dead properties of a file named names. Those the
// file doesn't have are left out.
//
// If the server fails to return some of them, the others are returned along
// with an error listing each failed property.
func (c *Client) DeadProperties(ctx context.Context, name string, names ...xml.Name) ([]DeadProperty, error) {
	if len(names) == 0 {
		return nil, fmt.Errorf("webdav: no dead property names")
	}

	resp, err := c.ic.PropFindFlat(ctx, name, internal.NewPropNamePropFind(names...))
	if err != nil {
		return nil, err
	}
	if err := resp.Err(); err != nil {
		return nil, err
	}

	var props []DeadProperty
	var errs []error
	for _, propstat := range resp.PropStats {
		code := propstat.Status.Code
		for i := range propstat.Prop.Raw {
			raw := &propstat.Prop.Raw[i]
			switch {
			case code == http.StatusOK:
				var prop DeadProperty
				if err := raw.Decode(&prop); err != nil {
					errs = append(errs, err)
				} else {
					props = append(props, prop)
				}
			case code != http.StatusNotFound:
				errs = append(errs, propStatError(raw, &propstat))
			}
		}
	}
	return props, errors.Join(errs...)
}

// PatchDeadProperties sets and removes dead properties of a file with a
// PROPPATCH request, which the server applies in full or not at all. The
// instructions are applied in order: a later instruction for a property
// overrides an earlier one.
//
// If the server refuses the request, the returned error lists each property
// with its status: first those which failed, then those which got 424 Failed
// Dependency because of them, so that errors.As finds the HTTPError of a
// property which failed. Properties in the DAV: namespace are live
// properties, which servers generally refuse to modify.
func (c *Client) PatchDeadProperties(ctx context.Context, name string, patch []DeadPropertyPatch) error {
	if len(patch) == 0 {
		return nil
	}

	var update internal.PropertyUpdate
	for _, p := range patch {
		var raw *internal.RawXMLValue
		if p.Remove {
			raw = internal.NewRawXMLElement(p.Property.XMLName, nil, nil)
		} else {
			var err error
			if raw, err = p.Property.rawXMLValue(); err != nil {
				return err
			}
		}
		// Group consecutive instructions of the same kind
		if n := len(update.Instructions); n > 0 && update.Instructions[n-1].Remove == p.Remove {
			update.Instructions[n-1].Prop.Raw = append(update.Instructions[n-1].Prop.Raw, *raw)
		} else {
			update.Instructions = append(update.Instructions, internal.PropertyUpdateInstruction{
				Remove: p.Remove,
				Prop:   internal.Prop{Raw: []internal.RawXMLValue{*raw}},
			})
		}
	}

	req, err := c.ic.NewXMLRequest("PROPPATCH", name, &update)
	if err != nil {
		return err
	}
	ms, err := c.ic.DoMultiStatus(req.WithContext(ctx))
	if err != nil {
		return err
	}

	var errs, failedDeps []error
	for i := range ms.Responses {
		resp := &ms.Responses[i]
		if err := resp.Err(); err != nil {
			errs = append(errs, err)
			continue
		}
		for j := range resp.PropStats {
			propstat := &resp.PropStats[j]
			if propstat.Status.Code/100 == 2 {
				continue
			}
			for k := range propstat.Prop.Raw {
				err := propStatError(&propstat.Prop.Raw[k], propstat)
				if propstat.Status.Code == http.StatusFailedDependency {
					failedDeps = append(failedDeps, err)
				} else {
					errs = append(errs, err)
				}
			}
		}
	}
	return errors.Join(append(errs, failedDeps...)...)
}

// propStatError returns the error of a property of a propstat element with a
// failure status.
func propStatError(raw *internal.RawXMLValue, propstat *internal.PropStat) error {
	err := &internal.HTTPError{Code: propstat.Status.Code}
	if propstat.Error != nil {
		err.Err = propstat.Error
	}
	name, _ := raw.XMLName()
	return fmt.Errorf("webdav: property <%v %v>: %w", name.Space, name.Local, err)
}

// Move moves a file.
//
// If the file is a directory and the server fails to move some of its
// descendants, the returned error lists each of them.
func (c *Client) Move(ctx context.Context, name, dest string, options *MoveOptions) error {
	if options == nil {
		options = new(MoveOptions)
	}

	req, err := c.ic.NewRequest("MOVE", name, nil)
	if err != nil {
		return err
	}

	destURL, err := c.ic.ResolveHref(dest)
	if err != nil {
		return err
	}

	req.Header.Set("Destination", destURL.String())
	req.Header.Set("Overwrite", internal.FormatOverwrite(!options.NoOverwrite))

	return c.ic.DoAndCheckMultiStatus(req.WithContext(ctx))
}
