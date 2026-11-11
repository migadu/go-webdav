package webdav

import (
	"context"
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

	return prop.Href.Path, nil
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

	ms, err := c.ic.PropFind(ctx, name, depth, fileInfoPropFind)
	if err != nil {
		return nil, err
	}

	l := make([]FileInfo, 0, len(ms.Responses))
	errs := make([]error, 0, len(ms.Responses))
	for _, resp := range ms.Responses {
		fi, err := fileInfoFromResponse(&resp)
		if err != nil {
			errs = append(errs, err)
		} else {
			l = append(l, *fi)
		}
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
func (c *Client) RemoveAll(ctx context.Context, name string) error {
	req, err := c.ic.NewRequest(http.MethodDelete, name, nil)
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
// copied as well.
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

	req.Header.Set("Destination", c.ic.ResolveHref(dest).String())
	req.Header.Set("Overwrite", internal.FormatOverwrite(!options.NoOverwrite))
	req.Header.Set("Depth", depth.String())

	resp, err := c.ic.Do(req.WithContext(ctx))
	if err != nil {
		return err
	}
	resp.Body.Close()
	return nil
}

// Move moves a file.
func (c *Client) Move(ctx context.Context, name, dest string, options *MoveOptions) error {
	if options == nil {
		options = new(MoveOptions)
	}

	req, err := c.ic.NewRequest("MOVE", name, nil)
	if err != nil {
		return err
	}

	req.Header.Set("Destination", c.ic.ResolveHref(dest).String())
	req.Header.Set("Overwrite", internal.FormatOverwrite(!options.NoOverwrite))

	resp, err := c.ic.Do(req.WithContext(ctx))
	if err != nil {
		return err
	}
	resp.Body.Close()
	return nil
}
