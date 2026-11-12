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
	"slices"
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

	var txtStrings []string
	for _, record := range txtRecords {
		txtStrings = append(txtStrings, splitTXT(record)...)
	}

	var urlPath string
	var foundPath bool
	for _, s := range txtStrings {
		k, v, ok := strings.Cut(s, "=")
		if !ok || !strings.EqualFold(k, "path") {
			continue
		}
		if foundPath {
			return "", fmt.Errorf("webdav: multiple path entries found on %s discovery TXT record", txtName)
		}
		if v == "" {
			return "", fmt.Errorf("webdav: empty path for %s TXT record", txtName)
		}
		if !strings.HasPrefix(v, "/") {
			v = "/" + v
		}
		urlPath = v
		foundPath = true
	}
	if !foundPath {
		urlPath = "/.well-known/" + service
	}

	u := url.URL{
		Scheme: "https",
		Path:   urlPath,
	}
	if addr.Port == 443 {
		u.Host = target
	} else {
		u.Host = fmt.Sprintf("%v:%v", target, addr.Port)
	}
	return u.String(), nil
}

// txtKeys are the keys, followed by "=", of the strings of the TXT record of
// RFC 6764 section 4: path, and txtvers, which RFC 6763 section 6.7
// recommends.
var txtKeys = []string{"path=", "txtvers="}

// splitTXT returns the strings of record, a DNS TXT record as returned by
// net.Resolver.LookupTXT. A record can hold several strings (RFC 6763 section
// 6.1), which LookupTXT joins without a separator: TXT "txtvers=1"
// "path=/dav/" is returned as "txtvers=1path=/dav/". The strings are found by
// their keys: a key of txtKeys, in any case, starts a new string once the
// current one has reached its value. Since a key can't hold "=", "xpath=/a/"
// is a single string with the key xpath. A string with another key can't be
// told apart from the end of the previous value.
func splitTXT(record string) []string {
	var strs []string
	start, inValue := 0, false
	for i := 0; i < len(record); i++ {
		if inValue {
			for _, key := range txtKeys {
				if len(record)-i >= len(key) && strings.EqualFold(record[i:i+len(key)], key) {
					strs = append(strs, record[start:i])
					start, inValue = i, false
					break
				}
			}
		}
		if record[i] == '=' {
			inValue = true
		}
	}
	return append(strs, record[start:])
}

// HTTPClient performs HTTP requests. It's implemented by *http.Client.
type HTTPClient interface {
	Do(req *http.Request) (*http.Response, error)
}

// DefaultHTTPClient returns the HTTP client to use when none is given: a copy
// of http.DefaultClient which doesn't follow a redirect from an https URL to
// an http one, and otherwise follows 10 redirects at most, as http.Client does
// by default. The error returned for such a redirect wraps an *HTTPError with
// its status.
//
// http.Client sends the request following a redirect with the header fields
// of the first one, Authorization and Cookie included, if it's for the same
// domain, whatever the scheme. The credentials, e.g. those added by
// webdav.HTTPClientWithBasicAuth, would go in cleartext.
//
// It's a copy, rather than a client of its own, so that the Transport, Jar
// and Timeout of http.DefaultClient still apply, as in webdav.SendWebPush.
func DefaultHTTPClient() *http.Client {
	c := *http.DefaultClient
	c.CheckRedirect = checkRedirect
	return &c
}

// checkRedirect is the CheckRedirect function of DefaultHTTPClient.
func checkRedirect(req *http.Request, via []*http.Request) error {
	if len(via) >= maxRedirects {
		return fmt.Errorf("stopped after %v redirects", maxRedirects)
	}
	if via[len(via)-1].URL.Scheme == "https" && req.URL.Scheme == "http" {
		return &HTTPError{
			Code: req.Response.StatusCode,
			Err:  errors.New("webdav: won't follow a redirect from an https URL to an http one"),
		}
	}
	return nil
}

type Client struct {
	http     HTTPClient
	endpoint *url.URL
}

// NewClient returns a client for endpoint, sending its requests with c. If c
// is nil, DefaultHTTPClient is used.
func NewClient(c HTTPClient, endpoint string) (*Client, error) {
	if c == nil {
		c = DefaultHTTPClient()
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
// A resource on the endpoint's host is referred to by its path, resolved
// against base if href is a relative reference other than a path. A resource
// on another host is referred to by its absolute URL, resolved against base if
// href is relative. So is a resource whose href has a query, which a path
// can't carry.
func (c *Client) normalizeHref(base *url.URL, href Href) Href {
	known := base != nil
	if !known {
		base = c.endpoint
	}

	u := url.URL(href)
	if u.Scheme == "" && u.Host == "" {
		// A path on the endpoint's host is already in the form returned by
		// clients. Other relative references are resolved against base (RFC
		// 3986 section 5.2), the URL of the request: "a.vcf" is a member of
		// a collection, and an empty reference is base itself (section 4.4).
		// Without base, a reference is kept as is, as clients resolve paths
		// against the endpoint (see ResolveHref).
		if u.RawQuery == "" && c.onEndpointHost(base) && (strings.HasPrefix(u.Path, "/") || !known) {
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

// maxRedirects is the number of redirects a request follows at most, as with
// http.Client.
const maxRedirects = 10

// sensitiveHeaders are the request header fields http.Client doesn't copy to
// the request following a redirect to another domain than the one of the
// request, or than one of its subdomains.
var sensitiveHeaders = []string{
	"Authorization",
	"Www-Authenticate",
	"Cookie",
	"Cookie2",
	"Proxy-Authorization",
	"Proxy-Authenticate",
}

// isDomainOrSubdomain reports whether host is the domain parent or one of
// its subdomains, as http.Client checks it to copy the sensitive header
// fields to the request following a redirect. An IP address only matches
// itself.
func isDomainOrSubdomain(host, parent string) bool {
	host, parent = strings.ToLower(host), strings.ToLower(parent)
	if host == parent {
		return true
	}
	if strings.ContainsAny(host, ":%") {
		return false
	}
	return strings.HasSuffix(host, "."+parent)
}

// Do sends req with the HTTP client, and returns its response, or an
// *HTTPError if its status isn't 2xx.
//
// http.Client follows a 301 Moved Permanently or 302 Found redirect with a GET
// request, whatever the method of the request, but RFC 9110 sections 15.4.2
// and 15.4.3 only allow changing POST to GET. Apache, for instance, redirects
// a PROPFIND request for a collection without its trailing slash this way. Do
// discards the response to such a GET request, and sends req again to its URL
// with the method, header fields and body of req. The body is sent again with
// req.GetBody, which http.NewRequest sets for in-memory bodies: without it, Do
// returns an *HTTPError with the redirect status, as http.Client does for a
// 307 or 308 redirect. As with http.Client, at most 10 redirects are followed,
// and the sensitive header fields, such as Authorization and Cookie, are left
// out once a redirect leaves the domain of req and its subdomains; an HTTP
// client adding credentials to every request, such as the one returned by
// webdav.HTTPClientWithBasicAuth, adds them anyway. For that reason, and
// because of the body, a request for an https URL isn't sent again to an http
// URL: Do returns an *HTTPError with the redirect status. The requests the HTTP
// client sends itself to follow a redirect, such as that GET request, are up
// to its CheckRedirect: http.Client sends them to an http URL on the same
// domain with the sensitive header fields, but DefaultHTTPClient, used when
// NewClient is given none, doesn't follow a redirect from https to http. A 303
// See Other redirect, which asks for a GET request, and a POST request are
// left to the HTTP client.
func (c *Client) Do(req *http.Request) (*http.Response, error) {
	resp, err := c.send(req)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode/100 != 2 {
		defer resp.Body.Close()
		return nil, responseError(resp)
	}
	return resp, nil
}

// isXMLMediaType reports whether the Content-Type of h is an XML one.
func isXMLMediaType(h http.Header) bool {
	t, _, _ := mime.ParseMediaType(h.Get("Content-Type"))
	return t == "application/xml" || t == "text/xml"
}

// responseError returns the error of resp, a response with a status outside
// 2xx: an *HTTPError with its status, wrapping the DAV:error element or the
// text of its body. The caller closes the body.
func responseError(resp *http.Response) error {
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
		if s := strings.TrimSpace(buf.String()); s != "" {
			if lr.N == 0 {
				s += " […]"
			}
			wrappedErr = fmt.Errorf("%v", s)
		}
	}
	return &HTTPError{Code: resp.StatusCode, Err: wrappedErr}
}

// send sends req with the HTTP client, keeping its method across 301 and 302
// redirects (see Do).
func (c *Client) send(req *http.Request) (*http.Response, error) {
	switch req.Method {
	case "", http.MethodGet, http.MethodHead, http.MethodPost:
		return c.http.Do(req)
	}

	r := req
	redirects := 0
	stripSensitive := false
	for {
		resp, err := c.http.Do(r)
		if err != nil {
			return nil, err
		}

		// The requests the HTTP client sent to follow redirects, in order,
		// and the first one with another method than req
		hops := redirectHops(resp)
		i := slices.IndexFunc(hops, func(hop *http.Request) bool {
			return hop.Method != req.Method
		})
		// No method change, or e.g. 303 See Other: the response is to be
		// fetched with GET
		if i < 0 || (hops[i].Response.StatusCode != http.StatusMovedPermanently && hops[i].Response.StatusCode != http.StatusFound) {
			// The redirects the HTTP client followed for a request sent
			// again, such as 307 ones, count with those before
			if redirects > 0 && redirects+len(hops) > maxRedirects {
				discardResponse(resp)
				return nil, tooManyRedirects(req)
			}
			return resp, nil
		}
		hop := hops[i]
		discardResponse(resp)

		redirects += i + 1
		if redirects > maxRedirects {
			return nil, tooManyRedirects(req)
		}
		for _, h := range hops[:i+1] {
			if !isDomainOrSubdomain(h.URL.Hostname(), req.URL.Hostname()) {
				stripSensitive = true
			}
		}

		r, err = redirectedRequest(req, hop, stripSensitive)
		if err != nil {
			return nil, err
		}
	}
}

func tooManyRedirects(req *http.Request) error {
	return fmt.Errorf("webdav: %v %v: stopped after %v redirects", req.Method, req.URL.Redacted(), maxRedirects)
}

// redirectHops returns the requests an HTTP client sent to follow redirects
// before getting resp, in order: none if resp is the response to the request
// it was given. They're found with Request.Response, which http.Client sets.
func redirectHops(resp *http.Response) []*http.Request {
	var hops []*http.Request
	for r := resp.Request; r != nil && r.Response != nil; r = r.Response.Request {
		hops = append(hops, r)
	}
	slices.Reverse(hops)
	return hops
}

// discardResponse closes the body of resp, reading a small one first so that
// the connection can be reused, as http.Client does with a redirect.
func discardResponse(resp *http.Response) {
	const maxBodySlurpSize = 2 << 10
	if resp.ContentLength == -1 || resp.ContentLength <= maxBodySlurpSize {
		io.CopyN(io.Discard, resp.Body, maxBodySlurpSize)
	}
	resp.Body.Close()
}

// redirectedRequest returns a copy of req, with a new body, to send to the URL
// of hop, the request an HTTP client sent with another method than req to
// follow a redirect. If stripSensitive is true, the sensitive header fields of
// req are left out.
func redirectedRequest(req, hop *http.Request, stripSensitive bool) (*http.Request, error) {
	u := *hop.URL
	if req.URL.Scheme == "https" && u.Scheme == "http" {
		// Leaving out the sensitive header fields wouldn't be enough: an
		// HTTP client may add credentials to every request, and the body
		// would be sent in cleartext too
		return nil, &HTTPError{
			Code: hop.Response.StatusCode,
			Err:  fmt.Errorf("webdav: won't send the %v request for an https URL again over plain HTTP to follow the redirect to %v", req.Method, u.Redacted()),
		}
	}
	r := req.Clone(req.Context())
	r.URL = &u
	r.Host = hop.Host
	if req.Body != nil && req.Body != http.NoBody {
		if req.GetBody == nil {
			return nil, &HTTPError{
				Code: hop.Response.StatusCode,
				Err:  fmt.Errorf("webdav: can't send the body of the %v request again to follow the redirect to %v", req.Method, u.Redacted()),
			}
		}
		body, err := req.GetBody()
		if err != nil {
			return nil, err
		}
		r.Body = body
	}
	if stripSensitive {
		for _, k := range sensitiveHeaders {
			r.Header.Del(k)
		}
	}
	return r, nil
}

func (c *Client) DoMultiStatus(req *http.Request) (*MultiStatus, error) {
	var resps []Response
	ms, err := c.DoMultiStatusFunc(req, func(resp *Response) error {
		resps = append(resps, *resp)
		return nil
	})
	if err != nil {
		return nil, err
	}
	ms.Responses = resps
	return ms, nil
}

// DoMultiStatusFunc performs a request answered with a 207 Multi-Status
// response, as DoMultiStatus does, but calls f with each of its responses as
// soon as it's decoded, rather than once all of them are: a response which
// isn't kept by f doesn't stay in memory while the next ones are received.
// The returned multistatus holds the rest of the response, without the
// responses. An error returned by f stops reading the response, and is
// returned.
func (c *Client) DoMultiStatusFunc(req *http.Request, f func(resp *Response) error) (*MultiStatus, error) {
	resp, err := c.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusMultiStatus {
		return nil, fmt.Errorf("HTTP multi-status request failed: %v", resp.Status)
	}

	base := responseBase(req, resp)
	return decodeMultiStatus(resp.Body, func(r *Response) error {
		c.normalizeResponse(r, base)
		return f(r)
	})
}

// decodeMultiStatus decodes a multistatus element from r as xml.Decoder.Decode
// does, except that it calls f with each response as soon as it's decoded,
// and returns the multistatus without its responses. An error returned by f
// stops decoding, and is returned.
func decodeMultiStatus(r io.Reader, f func(resp *Response) error) (*MultiStatus, error) {
	d := xml.NewDecoder(r)
	var start xml.StartElement
	for {
		tok, err := d.Token()
		if err != nil {
			return nil, err
		}
		if s, ok := tok.(xml.StartElement); ok {
			start = s
			break
		}
	}
	if start.Name != multiStatusName {
		return nil, fmt.Errorf("webdav: expected element <%v %v>, got <%v %v>", multiStatusName.Space, multiStatusName.Local, start.Name.Space, start.Name.Local)
	}

	ms := &MultiStatus{XMLName: start.Name}
	for {
		tok, err := d.Token()
		if err != nil {
			return nil, err
		}
		switch tok := tok.(type) {
		case xml.StartElement:
			// The elements are matched by their local name, as the struct
			// tags of MultiStatus match them: decoding a response in another
			// namespace than the DAV: one of Response.XMLName fails
			switch {
			case tok.Name.Local == "response":
				var resp Response
				if err = d.DecodeElement(&resp, &tok); err == nil {
					err = f(&resp)
				}
			case tok.Name.Local == "responsedescription":
				err = d.DecodeElement(&ms.ResponseDescription, &tok)
			case tok.Name.Local == "sync-token":
				err = d.DecodeElement(&ms.SyncToken, &tok)
			default:
				err = d.Skip()
			}
			if err != nil {
				return nil, err
			}
		case xml.EndElement:
			return ms, nil
		}
	}
}

// responseBase returns the URL the relative hrefs of resp, the response to
// req, are relative to: the URL of the request, after redirects.
func responseBase(req *http.Request, resp *http.Response) *url.URL {
	if resp.Request != nil && resp.Request.URL != nil {
		return resp.Request.URL
	}
	return req.URL
}

// normalizeResponse normalizes the hrefs of r, a response received for base,
// with normalizeHref, and records how to normalize those in its properties.
func (c *Client) normalizeResponse(r *Response, base *url.URL) {
	for j, href := range r.Hrefs {
		r.Hrefs[j] = c.normalizeHref(base, href)
	}
	r.client, r.base = c, base
}

// normalizeMultiStatus normalizes the responses of ms, received in resp to
// req, with normalizeResponse.
func (c *Client) normalizeMultiStatus(ms *MultiStatus, req *http.Request, resp *http.Response) {
	base := responseBase(req, resp)
	for i := range ms.Responses {
		c.normalizeResponse(&ms.Responses[i], base)
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

	// Only the errors are kept, not the responses
	base := responseBase(req, resp)
	var errs []error
	_, err = decodeMultiStatus(resp.Body, func(r *Response) error {
		c.normalizeResponse(r, base)
		if err := r.Err(); err != nil {
			errs = append(errs, err)
		}
		return nil
	})
	if err != nil {
		return err
	}
	return errors.Join(errs...)
}

// PropPatch performs a PROPPATCH request for path, which the server applies
// in full or not at all (RFC 4918 section 9.2).
//
// If the server refuses the request, the returned error lists each property
// with its status: first those which failed, then those which got 424 Failed
// Dependency because of them, so that errors.As finds the HTTPError of a
// property which failed.
func (c *Client) PropPatch(ctx context.Context, path string, update *PropertyUpdate) error {
	req, err := c.NewXMLRequest("PROPPATCH", path, update)
	if err != nil {
		return err
	}

	var errs propErrors
	_, err = c.DoMultiStatusFunc(req.WithContext(ctx), func(resp *Response) error {
		errs.addResponse(resp)
		return nil
	})
	if err != nil {
		return err
	}
	return errs.err()
}

// maxCreateErrorSize is the size of the largest error response body
// DoCreateCollection reads.
const maxCreateErrorSize = 1 << 20

// DoCreateCollection performs req, a request creating a collection and
// setting its properties all at once: an extended MKCOL (RFC 5689) or a
// MKCALENDAR (RFC 4791 section 5.3.1) request.
//
// If the server refuses some of the properties, the returned error lists each
// of them with its status, as for PropPatch. The server reports them in a 207
// Multi-Status response, or with an error status and a response body such as
// DAV:mkcol-response (RFC 5689 section 3.5): the HTTPError of that status then
// wraps them. Any other response with a status outside 2xx is returned as Do
// returns it.
func (c *Client) DoCreateCollection(req *http.Request) error {
	resp, err := c.send(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	switch {
	case resp.StatusCode == http.StatusMultiStatus:
		base := responseBase(req, resp)
		var errs propErrors
		_, err := decodeMultiStatus(resp.Body, func(r *Response) error {
			c.normalizeResponse(r, base)
			errs.addResponse(r)
			return nil
		})
		if err != nil {
			return err
		}
		return errs.err()
	case resp.StatusCode/100 == 2:
		return nil
	case !isXMLMediaType(resp.Header):
		return responseError(resp)
	}

	body, err := io.ReadAll(io.LimitReader(resp.Body, maxCreateErrorSize))
	if err != nil {
		return &HTTPError{Code: resp.StatusCode, Err: err}
	}
	// Any root element holding propstat elements is accepted: RFC 5689 calls
	// it DAV:mkcol-response, and RFC 4791 CALDAV:mkcalendar-response. A
	// DAV:error element holds none.
	var propStats struct {
		PropStats []PropStat `xml:"DAV: propstat"`
	}
	if xml.Unmarshal(body, &propStats) == nil {
		var errs propErrors
		errs.addPropStats(propStats.PropStats)
		if err := errs.err(); err != nil {
			return &HTTPError{Code: resp.StatusCode, Err: err}
		}
	}
	resp.Body = io.NopCloser(bytes.NewReader(body))
	return responseError(resp)
}

// propErrors collects the errors of the properties of a PROPPATCH or a
// collection creation request the server refused. The properties which got
// 424 Failed Dependency are kept apart, to be listed after the others.
type propErrors struct {
	errs, failedDeps []error
}

// addResponse adds the error of resp, or else those of its properties.
func (pe *propErrors) addResponse(resp *Response) {
	if err := resp.Err(); err != nil {
		pe.errs = append(pe.errs, err)
		return
	}
	pe.addPropStats(resp.PropStats)
}

// addPropStats adds the errors of the properties of propStats with a status
// outside 2xx.
func (pe *propErrors) addPropStats(propStats []PropStat) {
	for i := range propStats {
		propstat := &propStats[i]
		if propstat.Status.Code/100 == 2 {
			continue
		}
		for j := range propstat.Prop.Raw {
			err := PropStatError(&propstat.Prop.Raw[j], propstat)
			if propstat.Status.Code == http.StatusFailedDependency {
				pe.failedDeps = append(pe.failedDeps, err)
			} else {
				pe.errs = append(pe.errs, err)
			}
		}
	}
}

func (pe *propErrors) err() error {
	return errors.Join(append(pe.errs, pe.failedDeps...)...)
}

// PropStatError returns the error of raw, a property of propstat, a propstat
// element with a status outside 2xx: an HTTPError with that status, wrapping
// the DAV:error element of propstat if any.
func PropStatError(raw *RawXMLValue, propstat *PropStat) error {
	err := &HTTPError{Code: propstat.Status.Code}
	if propstat.Error != nil {
		err.Err = propstat.Error
	}
	name, _ := raw.XMLName()
	return fmt.Errorf("webdav: property <%v %v>: %w", name.Space, name.Local, err)
}

func (c *Client) PropFind(ctx context.Context, path string, depth Depth, propfind *PropFind) (*MultiStatus, error) {
	var resps []Response
	ms, err := c.PropFindFunc(ctx, path, depth, propfind, func(resp *Response) error {
		resps = append(resps, *resp)
		return nil
	})
	if err != nil {
		return nil, err
	}
	ms.Responses = resps
	return ms, nil
}

// PropFindFunc performs a PROPFIND request, as PropFind does, but calls f
// with each response as soon as it's decoded (see DoMultiStatusFunc). The
// returned multistatus holds the rest of the response, without the
// responses.
func (c *Client) PropFindFunc(ctx context.Context, path string, depth Depth, propfind *PropFind, f func(resp *Response) error) (*MultiStatus, error) {
	req, err := c.NewXMLRequest("PROPFIND", path, propfind)
	if err != nil {
		return nil, err
	}

	req.Header.Add("Depth", depth.String())

	return c.DoMultiStatusFunc(req.WithContext(ctx), f)
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

// SyncCollection perform a `sync-collection` REPORT operation on a resource,
// calling f with each response as soon as it's decoded (see
// DoMultiStatusFunc). The returned multistatus holds the sync token, without
// the responses.
func (c *Client) SyncCollection(ctx context.Context, path, syncToken string, level Depth, limit *Limit, prop *Prop, f func(resp *Response) error) (*MultiStatus, error) {
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

	return c.DoMultiStatusFunc(req.WithContext(ctx), f)
}
