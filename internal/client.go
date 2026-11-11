package internal

import (
	"bytes"
	"context"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"mime"
	"net"
	"net/http"
	"net/url"
	"path"
	"strings"
	"unicode"
)

// ErrNoServiceRecord is returned by DiscoverContextURL when the domain has no
// SRV record for the service, or one saying the service isn't available.
var ErrNoServiceRecord = errors.New("webdav: domain doesn't have an SRV record")

// dnsResolver performs the DNS lookups of DiscoverContextURL. It's
// implemented by *net.Resolver, and replaced in tests.
type dnsResolver interface {
	LookupSRV(ctx context.Context, service, proto, name string) (cname string, addrs []*net.SRV, err error)
	LookupTXT(ctx context.Context, name string) ([]string, error)
}

var resolver dnsResolver = &net.Resolver{}

// isDNSNotFound reports whether err is a DNS answer saying that the requested
// record doesn't exist.
func isDNSNotFound(err error) bool {
	var dnsErr *net.DNSError
	return errors.As(err, &dnsErr) && dnsErr.IsNotFound && !dnsErr.IsTemporary
}

// DiscoverContextURL performs a DNS-based CardDAV/CalDAV service discovery as
// described in RFC 6764. It returns the URL to the CardDAV/CalDAV server.
// Specifically it implements points 2 and 3 from the bootstrapping procedure
// defined in RFC 6764 section 6.
func DiscoverContextURL(ctx context.Context, service, domain string) (string, error) {
	// Only lookup TLS records, plaintext connections are insecure
	_, addrs, err := resolver.LookupSRV(ctx, service+"s", "tcp", domain)
	if isDNSNotFound(err) {
		return "", ErrNoServiceRecord
	} else if err != nil {
		return "", err
	}

	if len(addrs) == 0 {
		return "", ErrNoServiceRecord
	}
	addr := addrs[0]

	target := strings.TrimSuffix(addr.Target, ".")
	if target == "" {
		// A target of "." means the service is decidedly not available
		// (RFC 2782)
		return "", ErrNoServiceRecord
	}

	txtName := fmt.Sprintf("_%ss._tcp.%s", service, domain)
	txtRecords, err := resolver.LookupTXT(ctx, txtName)
	if isDNSNotFound(err) {
		// No TXT record, use the default path
		txtRecords = nil
	} else if err != nil {
		return "", err
	}

	var path string
	switch len(txtRecords) {
	case 0:
		path = "/.well-known/" + service
	case 1:
		record := txtRecords[0]
		if !strings.HasPrefix(record, "path=") {
			return "", fmt.Errorf("webdav: TXT record for %s does not contain the path key", txtName)
		}

		path = strings.TrimPrefix(record, "path=")
		if path == "" {
			return "", fmt.Errorf("webdav: empty path for %s TXT record", txtName)
		}
	default: // more than 1
		return "", fmt.Errorf("webdav: more than one entry found on %s discovery TXT record", txtName)
	}

	u := url.URL{
		Scheme: "https",
		Path:   path,
	}
	if addr.Port == 443 {
		u.Host = target
	} else {
		u.Host = fmt.Sprintf("%v:%v", target, addr.Port)
	}
	return u.String(), nil
}

// HTTPClient performs HTTP requests. It's implemented by *http.Client.
type HTTPClient interface {
	Do(req *http.Request) (*http.Response, error)
}

type Client struct {
	http     HTTPClient
	endpoint *url.URL
}

func NewClient(c HTTPClient, endpoint string) (*Client, error) {
	if c == nil {
		c = http.DefaultClient
	}

	u, err := url.Parse(endpoint)
	if err != nil {
		return nil, err
	}
	if u.Path == "" {
		// This is important to avoid issues with path.Join
		u.Path = "/"
	}
	return &Client{http: c, endpoint: u}, nil
}

// isHTTPURL reports whether s is an absolute http or https URL, rather than a
// path.
func isHTTPURL(s string) bool {
	scheme, _, ok := strings.Cut(s, "://")
	return ok && (strings.EqualFold(scheme, "http") || strings.EqualFold(scheme, "https"))
}

// ResolveHref returns the URL of the resource at p.
//
// p is usually a path: an absolute one is on the endpoint's host, a relative
// one is relative to the endpoint's path.
//
// p can also be an absolute http or https URL, which clients return for the
// resources on another host than the endpoint's (see HrefPath). It's used
// as is: requests for it are sent to its host. The credentials of the endpoint
// URL are only added to a URL with the same scheme and host; an HTTPClient
// adding credentials to its requests, such as the one returned by
// webdav.HTTPClientWithBasicAuth, adds them for any host.
func (c *Client) ResolveHref(p string) (*url.URL, error) {
	if isHTTPURL(p) {
		u, err := url.Parse(p)
		if err != nil {
			return nil, err
		}
		if u.User == nil && u.Scheme == c.endpoint.Scheme && strings.EqualFold(u.Host, c.endpoint.Host) {
			u.User = c.endpoint.User
		}
		return u, nil
	}

	hasTrailingSlash := strings.HasSuffix(p, "/") || (p == "" && strings.HasSuffix(c.endpoint.Path, "/"))

	if !strings.HasPrefix(p, "/") {
		p = path.Join(c.endpoint.Path, p)
	}

	if hasTrailingSlash && !strings.HasSuffix(p, "/") {
		p += "/"
	}

	return &url.URL{
		Scheme: c.endpoint.Scheme,
		User:   c.endpoint.User,
		Host:   c.endpoint.Host,
		Path:   p,
	}, nil
}

// onEndpointHost reports whether u is on the endpoint's host. Only the host
// names are compared: servers behind a reverse proxy often report URLs with
// the scheme or port of their own side of the proxy, and following them would
// e.g. downgrade requests to plain HTTP.
func (c *Client) onEndpointHost(u *url.URL) bool {
	return strings.EqualFold(u.Hostname(), c.endpoint.Hostname())
}

func defaultPort(scheme string) string {
	switch scheme {
	case "http":
		return "80"
	case "https":
		return "443"
	}
	return ""
}

// normalizeHref returns href, a reference received in a response to a request
// for base (the endpoint if nil), in the form returned by clients.
//
// A resource on the endpoint's host is referred to by its path, or kept as is
// if href is relative to a URL on the endpoint's host. A resource on another
// host is referred to by its absolute URL, resolved against base if href is
// relative. So is a resource whose href has a query, which a path can't carry.
func (c *Client) normalizeHref(base *url.URL, href Href) Href {
	if base == nil {
		base = c.endpoint
	}

	u := url.URL(href)
	if u.Scheme == "" && u.Host == "" {
		if u.RawQuery == "" && c.onEndpointHost(base) {
			return href
		}
		u = *base.ResolveReference(&u)
	}

	if u.Scheme != "http" && u.Scheme != "https" {
		// Not a URL requests can be sent to
		return Href{Path: u.Path, RawPath: u.RawPath}
	}
	if c.onEndpointHost(&u) {
		if u.RawQuery == "" {
			return Href{Path: u.Path, RawPath: u.RawPath}
		}
		u.Scheme, u.Host = c.endpoint.Scheme, c.endpoint.Host
	} else if port := u.Port(); port == defaultPort(u.Scheme) {
		u.Host = strings.TrimSuffix(u.Host, ":"+port)
	}
	u.User = nil
	u.Fragment, u.RawFragment = "", ""
	return Href(u)
}

// HrefPath returns the path of the resource at href, a reference received in a
// response to a request for base (the endpoint if nil), such as a Location
// header, in the form returned by clients: the path of a resource on the
// endpoint's host, but the absolute URL of one on another host or whose href
// has a query (see normalizeHref).
func (c *Client) HrefPath(base, href *url.URL) string {
	h := c.normalizeHref(base, Href(*href))
	return h.pathOrURL()
}

// RequestHrefs returns hrefs to the resources at paths, which are as accepted
// by ResolveHref, for the body of a request for target. A path is used as is,
// a URL on the host of target is reduced to its path, and any other URL is
// kept whole.
func (c *Client) RequestHrefs(target string, paths []string) ([]Href, error) {
	targetURL, err := c.ResolveHref(target)
	if err != nil {
		return nil, err
	}

	hrefs := make([]Href, len(paths))
	for i, p := range paths {
		if !isHTTPURL(p) {
			hrefs[i] = Href{Path: p}
			continue
		}
		u, err := url.Parse(p)
		if err != nil {
			return nil, err
		}
		if u.Scheme == targetURL.Scheme && strings.EqualFold(u.Host, targetURL.Host) {
			// More servers understand a path than a URL
			hrefs[i] = Href{Path: u.Path, RawPath: u.RawPath, RawQuery: u.RawQuery}
		} else {
			u.User = nil
			hrefs[i] = Href(*u)
		}
	}
	return hrefs, nil
}

func (c *Client) NewRequest(method string, path string, body io.Reader) (*http.Request, error) {
	u, err := c.ResolveHref(path)
	if err != nil {
		return nil, err
	}
	return http.NewRequest(method, u.String(), body)
}

func (c *Client) NewXMLRequest(method string, path string, v interface{}) (*http.Request, error) {
	var buf bytes.Buffer
	buf.WriteString(xml.Header)
	if err := xml.NewEncoder(&buf).Encode(v); err != nil {
		return nil, err
	}

	req, err := c.NewRequest(method, path, &buf)
	if err != nil {
		return nil, err
	}

	req.Header.Add("Content-Type", "text/xml; charset=\"utf-8\"")

	return req, nil
}

func (c *Client) Do(req *http.Request) (*http.Response, error) {
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode/100 != 2 {
		defer resp.Body.Close()

		contentType := resp.Header.Get("Content-Type")
		if contentType == "" {
			contentType = "text/plain"
		}

		var wrappedErr error
		t, _, _ := mime.ParseMediaType(contentType)
		if t == "application/xml" || t == "text/xml" {
			var davErr Error
			if err := xml.NewDecoder(resp.Body).Decode(&davErr); err != nil {
				wrappedErr = err
			} else {
				wrappedErr = &davErr
			}
		} else if strings.HasPrefix(t, "text/") {
			lr := io.LimitedReader{R: resp.Body, N: 1024}
			var buf bytes.Buffer
			io.Copy(&buf, &lr)
			resp.Body.Close()
			if s := strings.TrimSpace(buf.String()); s != "" {
				if lr.N == 0 {
					s += " […]"
				}
				wrappedErr = fmt.Errorf("%v", s)
			}
		}
		return nil, &HTTPError{Code: resp.StatusCode, Err: wrappedErr}
	}
	return resp, nil
}

func (c *Client) DoMultiStatus(req *http.Request) (*MultiStatus, error) {
	resp, err := c.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusMultiStatus {
		return nil, fmt.Errorf("HTTP multi-status request failed: %v", resp.Status)
	}

	// TODO: the response can be quite large, support streaming Response elements
	var ms MultiStatus
	if err := xml.NewDecoder(resp.Body).Decode(&ms); err != nil {
		return nil, err
	}

	c.normalizeMultiStatus(&ms, req, resp)
	return &ms, nil
}

// normalizeMultiStatus normalizes the hrefs of ms, received in resp to req,
// with normalizeHref, and records how to normalize those in its properties.
func (c *Client) normalizeMultiStatus(ms *MultiStatus, req *http.Request, resp *http.Response) {
	// Relative hrefs are relative to the URL of the request, after redirects
	base := req.URL
	if resp.Request != nil && resp.Request.URL != nil {
		base = resp.Request.URL
	}

	for i := range ms.Responses {
		r := &ms.Responses[i]
		for j, href := range r.Hrefs {
			r.Hrefs[j] = c.normalizeHref(base, href)
		}
		r.client, r.base = c, base
	}
}

// DoAndCheckMultiStatus performs a request which acts on a resource and, if
// it's a collection, on its members, such as DELETE, COPY or MOVE.
//
// The server may fail for some members and not for others, and report this
// with a 207 Multi-Status response listing the members which failed (RFC 4918
// sections 9.6.1, 9.8.8 and 9.9.4). Each failure is returned as an HrefError
// wrapping an HTTPError. Members with a 2xx status are not errors.
func (c *Client) DoAndCheckMultiStatus(req *http.Request) error {
	resp, err := c.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusMultiStatus {
		return nil
	}

	var ms MultiStatus
	if err := xml.NewDecoder(resp.Body).Decode(&ms); err != nil {
		return err
	}
	c.normalizeMultiStatus(&ms, req, resp)

	var errs []error
	for i := range ms.Responses {
		if err := ms.Responses[i].Err(); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

func (c *Client) PropFind(ctx context.Context, path string, depth Depth, propfind *PropFind) (*MultiStatus, error) {
	req, err := c.NewXMLRequest("PROPFIND", path, propfind)
	if err != nil {
		return nil, err
	}

	req.Header.Add("Depth", depth.String())

	return c.DoMultiStatus(req.WithContext(ctx))
}

// PropfindFlat performs a PROPFIND request with a zero depth.
func (c *Client) PropFindFlat(ctx context.Context, path string, propfind *PropFind) (*Response, error) {
	ms, err := c.PropFind(ctx, path, DepthZero, propfind)
	if err != nil {
		return nil, err
	}

	// If the client followed a redirect, the Href might be different from the request path
	if len(ms.Responses) != 1 {
		return nil, fmt.Errorf("PROPFIND with Depth: 0 returned %d responses", len(ms.Responses))
	}
	return &ms.Responses[0], nil
}

func parseCommaSeparatedSet(values []string, upper bool) map[string]bool {
	m := make(map[string]bool)
	for _, v := range values {
		fields := strings.FieldsFunc(v, func(r rune) bool {
			return unicode.IsSpace(r) || r == ','
		})
		for _, f := range fields {
			if upper {
				f = strings.ToUpper(f)
			} else {
				f = strings.ToLower(f)
			}
			m[f] = true
		}
	}
	return m
}

func (c *Client) Options(ctx context.Context, path string) (classes map[string]bool, methods map[string]bool, err error) {
	req, err := c.NewRequest(http.MethodOptions, path, nil)
	if err != nil {
		return nil, nil, err
	}

	resp, err := c.Do(req.WithContext(ctx))
	if err != nil {
		return nil, nil, err
	}
	resp.Body.Close()

	classes = parseCommaSeparatedSet(resp.Header["Dav"], false)
	if !classes["1"] {
		return nil, nil, fmt.Errorf("webdav: server doesn't support DAV class 1")
	}

	methods = parseCommaSeparatedSet(resp.Header["Allow"], true)
	return classes, methods, nil
}

// SyncCollection perform a `sync-collection` REPORT operation on a resource
func (c *Client) SyncCollection(ctx context.Context, path, syncToken string, level Depth, limit *Limit, prop *Prop) (*MultiStatus, error) {
	q := SyncCollectionQuery{
		SyncToken: syncToken,
		SyncLevel: level.String(),
		Limit:     limit,
		Prop:      prop,
	}

	req, err := c.NewXMLRequest("REPORT", path, &q)
	if err != nil {
		return nil, err
	}

	ms, err := c.DoMultiStatus(req.WithContext(ctx))
	if err != nil {
		return nil, err
	}

	return ms, nil
}
