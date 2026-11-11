package carddav

import (
	"bytes"
	"context"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"net/url"
	"strconv"
	"time"

	"github.com/emersion/go-vcard"
	"github.com/emersion/go-webdav"
	"github.com/emersion/go-webdav/internal"
)

// ErrNoServiceRecord is returned by DiscoverContextURL when the domain has no
// SRV record for the service. RFC 6764 section 6 leaves it to the client to ask
// the user for the server, or to guess one, e.g. the domain itself with
// "https://" + domain + "/.well-known/carddav".
var ErrNoServiceRecord = internal.ErrNoServiceRecord

// DiscoverContextURL performs a DNS-based CardDAV service discovery as
// described in RFC 6352 section 11. It returns the URL to the CardDAV server.
func DiscoverContextURL(ctx context.Context, domain string) (string, error) {
	return internal.DiscoverContextURL(ctx, "carddav", domain)
}

// Client provides access to a remote CardDAV server.
type Client struct {
	*webdav.Client

	ic *internal.Client
}

// NewClient creates a new CardDAV client.
//
// The paths the client returns and takes are as described for
// webdav.NewClient: an address book home set, address book or address object
// on another host than the endpoint's, as with iCloud, is returned as an
// absolute URL.
func NewClient(c webdav.HTTPClient, endpoint string) (*Client, error) {
	wc, err := webdav.NewClient(c, endpoint)
	if err != nil {
		return nil, err
	}
	ic, err := internal.NewClient(c, endpoint)
	if err != nil {
		return nil, err
	}
	return &Client{wc, ic}, nil
}

func (c *Client) HasSupport(ctx context.Context) error {
	classes, _, err := c.ic.Options(ctx, "")
	if err != nil {
		return err
	}

	if !classes["addressbook"] {
		return fmt.Errorf("carddav: server doesn't support the DAV addressbook class")
	}
	return nil
}

func (c *Client) FindAddressBookHomeSet(ctx context.Context, principal string) (string, error) {
	propfind := internal.NewPropNamePropFind(addressBookHomeSetName)
	resp, err := c.ic.PropFindFlat(ctx, principal, propfind)
	if err != nil {
		return "", err
	}

	var prop addressbookHomeSet
	if err := resp.DecodeProp(&prop); err != nil {
		return "", err
	}

	return resp.HrefPath(&prop.Href), nil
}

func decodeSupportedAddressData(supported *supportedAddressData) []AddressDataType {
	l := make([]AddressDataType, len(supported.Types))
	for i, t := range supported.Types {
		l[i] = AddressDataType{t.ContentType, t.Version}
	}
	return l
}

var addressBookProps = []xml.Name{
	internal.ResourceTypeName,
	internal.DisplayNameName,
	addressBookDescriptionName,
	maxResourceSizeName,
	supportedAddressDataName,
	internal.SyncTokenName,
	internal.GetCTagName,
	internal.CurrentUserPrivilegeSetName,
}

// FindAddressBook fetches address book metadata without listing its members.
func (c *Client) FindAddressBook(ctx context.Context, path string) (*AddressBook, error) {
	propfind := internal.NewPropNamePropFind(addressBookProps...)
	resp, err := c.ic.PropFindFlat(ctx, path, propfind)
	if err != nil {
		return nil, err
	}
	p, err := resp.Path()
	if err != nil {
		return nil, err
	}

	var resType internal.ResourceType
	if err := resp.DecodeProp(&resType); err != nil {
		return nil, err
	}
	if !resType.Is(addressBookName) {
		return nil, fmt.Errorf("carddav: resource at %q is not an address book", p)
	}

	return decodeAddressBook(p, resp)
}

func (c *Client) FindAddressBooks(ctx context.Context, addressBookHomeSet string) ([]AddressBook, error) {
	propfind := internal.NewPropNamePropFind(addressBookProps...)
	ms, err := c.ic.PropFind(ctx, addressBookHomeSet, internal.DepthOne, propfind)
	if err != nil {
		return nil, err
	}

	l := make([]AddressBook, 0, len(ms.Responses))
	errs := make([]error, 0, len(ms.Responses))
	for _, resp := range ms.Responses {
		path, err := resp.Path()
		if err != nil {
			errs = append(errs, err)
			continue
		}

		var resType internal.ResourceType
		if err := resp.DecodeProp(&resType); err != nil {
			return nil, err
		}
		if !resType.Is(addressBookName) {
			continue
		}

		ab, err := decodeAddressBook(path, &resp)
		if err != nil {
			return nil, err
		}
		l = append(l, *ab)
	}

	return l, errors.Join(errs...)
}

func decodeAddressBook(path string, resp *internal.Response) (*AddressBook, error) {
	var desc addressbookDescription
	if err := resp.DecodeProp(&desc); err != nil && !internal.IsNotFound(err) {
		return nil, err
	}

	var dispName internal.DisplayName
	if err := resp.DecodeProp(&dispName); err != nil && !internal.IsNotFound(err) {
		return nil, err
	}

	var maxResSize maxResourceSize
	if err := resp.DecodeProp(&maxResSize); err != nil && !internal.IsNotFound(err) {
		return nil, err
	}
	if maxResSize.Size < 0 {
		return nil, fmt.Errorf("carddav: max-resource-size must be a positive integer")
	}

	var supported supportedAddressData
	if err := resp.DecodeProp(&supported); err != nil && !internal.IsNotFound(err) {
		return nil, err
	}

	var syncToken internal.SyncToken
	if err := resp.DecodeProp(&syncToken); err != nil && !internal.IsNotFound(err) {
		return nil, err
	}

	var ctag internal.GetCTag
	if err := resp.DecodeProp(&ctag); err != nil && !internal.IsNotFound(err) {
		return nil, err
	}

	var readOnly bool
	var privSet internal.CurrentUserPrivilegeSet
	if err := resp.DecodeProp(&privSet); err == nil {
		readOnly = !privSet.HasWrite()
	} else if !internal.IsNotFound(err) {
		return nil, err
	}

	return &AddressBook{
		Path:                 path,
		Name:                 dispName.Name,
		Description:          desc.Description,
		MaxResourceSize:      maxResSize.Size,
		SyncToken:            syncToken.Token,
		SupportedAddressData: decodeSupportedAddressData(&supported),
		ReadOnly:             readOnly,
		CTag:                 ctag.CTag,
	}, nil
}

func encodeAddressPropReq(req *AddressDataRequest) (*internal.Prop, error) {
	var addrDataReq addressDataReq
	if req.AllProp {
		addrDataReq.Allprop = &struct{}{}
	} else {
		for _, name := range req.Props {
			addrDataReq.Props = append(addrDataReq.Props, prop{Name: name})
		}
	}

	getLastModReq := internal.NewRawXMLElement(internal.GetLastModifiedName, nil, nil)
	getETagReq := internal.NewRawXMLElement(internal.GetETagName, nil, nil)
	getContentLengthReq := internal.NewRawXMLElement(internal.GetContentLengthName, nil, nil)
	return internal.EncodeProp(&addrDataReq, getLastModReq, getETagReq, getContentLengthReq)
}

func encodePropFilter(pf *PropFilter) (*propFilter, error) {
	el := &propFilter{Name: pf.Name, Test: filterTest(pf.Test)}
	if pf.IsNotDefined {
		if len(pf.TextMatches) > 0 || len(pf.Params) > 0 {
			return nil, fmt.Errorf("carddav: failed to encode PropFilter: IsNotDefined cannot be set with TextMatches or Params")
		}
		el.IsNotDefined = &struct{}{}
	}
	for _, tm := range pf.TextMatches {
		el.TextMatches = append(el.TextMatches, *encodeTextMatch(&tm))
	}
	for _, param := range pf.Params {
		paramEl, err := encodeParamFilter(&param)
		if err != nil {
			return nil, err
		}
		el.Params = append(el.Params, *paramEl)
	}
	return el, nil
}

func encodeParamFilter(pf *ParamFilter) (*paramFilter, error) {
	el := &paramFilter{Name: pf.Name}
	if pf.IsNotDefined {
		if pf.TextMatch != nil {
			return nil, fmt.Errorf("carddav: failed to encode ParamFilter: only one of IsNotDefined or TextMatch can be set")
		}
		el.IsNotDefined = &struct{}{}
	}
	if pf.TextMatch != nil {
		el.TextMatch = encodeTextMatch(pf.TextMatch)
	}
	return el, nil
}

func encodeTextMatch(tm *TextMatch) *textMatch {
	return &textMatch{
		Text:            tm.Text,
		Collation:       tm.Collation,
		NegateCondition: negateCondition(tm.NegateCondition),
		MatchType:       matchType(tm.MatchType),
	}
}

// doAddressList performs req, an addressbook-query or addressbook-multiget
// REPORT, and decodes the address object of each response as soon as the
// response is received.
func (c *Client) doAddressList(req *http.Request) ([]AddressObject, error) {
	addrs := []AddressObject{}
	var errs []error
	_, err := c.ic.DoMultiStatusFunc(req, func(resp *internal.Response) error {
		path, err := resp.Path()
		if err != nil {
			errs = append(errs, err)
			return nil
		}

		var addrData addressDataResp
		if err := resp.DecodeProp(&addrData); err != nil {
			return err
		}

		var getLastMod internal.GetLastModified
		if err := resp.DecodeProp(&getLastMod); err != nil && !internal.IsNotFound(err) {
			return err
		}

		var getETag internal.GetETag
		if err := resp.DecodeProp(&getETag); err != nil && !internal.IsNotFound(err) {
			return err
		}

		var getContentLength internal.GetContentLength
		if err := resp.DecodeProp(&getContentLength); err != nil && !internal.IsNotFound(err) {
			return err
		}

		r := bytes.NewReader(addrData.Data)
		card, err := vcard.NewDecoder(r).Decode()
		if err != nil {
			return err
		}

		addrs = append(addrs, AddressObject{
			Path:          path,
			ModTime:       time.Time(getLastMod.LastModified),
			ContentLength: getContentLength.Length,
			ETag:          string(getETag.ETag),
			Card:          card,
		})
		return nil
	})
	if err != nil {
		return nil, err
	}

	return addrs, errors.Join(errs...)
}

func (c *Client) QueryAddressBook(ctx context.Context, addressBook string, query *AddressBookQuery) ([]AddressObject, error) {
	propReq, err := encodeAddressPropReq(&query.DataRequest)
	if err != nil {
		return nil, err
	}

	addressbookQuery := addressbookQuery{Prop: propReq}
	addressbookQuery.Filter.Test = filterTest(query.FilterTest)
	for _, pf := range query.PropFilters {
		el, err := encodePropFilter(&pf)
		if err != nil {
			return nil, err
		}
		addressbookQuery.Filter.Props = append(addressbookQuery.Filter.Props, *el)
	}
	if query.Limit > 0 {
		addressbookQuery.Limit = &limit{NResults: uint(query.Limit)}
	}

	req, err := c.ic.NewXMLRequest("REPORT", addressBook, &addressbookQuery)
	if err != nil {
		return nil, err
	}

	req.Header.Add("Depth", "1")

	return c.doAddressList(req.WithContext(ctx))
}

func (c *Client) MultiGetAddressBook(ctx context.Context, path string, multiGet *AddressBookMultiGet) ([]AddressObject, error) {
	propReq, err := encodeAddressPropReq(&multiGet.DataRequest)
	if err != nil {
		return nil, err
	}

	paths := multiGet.Paths
	if len(paths) == 0 {
		paths = []string{path}
	}
	hrefs, err := c.ic.RequestHrefs(path, paths)
	if err != nil {
		return nil, err
	}

	addressbookMultiget := addressbookMultiget{Prop: propReq, Hrefs: hrefs}

	req, err := c.ic.NewXMLRequest("REPORT", path, &addressbookMultiget)
	if err != nil {
		return nil, err
	}

	req.Header.Add("Depth", "1")

	return c.doAddressList(req.WithContext(ctx))
}

func (c *Client) populateAddressObject(ao *AddressObject, resp *http.Response) error {
	h := resp.Header
	if loc := h.Get("Location"); loc != "" {
		u, err := url.Parse(loc)
		if err != nil {
			return err
		}
		var base *url.URL
		if resp.Request != nil {
			base = resp.Request.URL
		}
		ao.Path = c.ic.HrefPath(base, u)
	}
	if s := h.Get("ETag"); s != "" {
		var etag internal.ETag
		if err := etag.UnmarshalText([]byte(s)); err != nil {
			return err
		}
		ao.ETag = string(etag)
	}
	if contentLength := h.Get("Content-Length"); contentLength != "" {
		n, err := strconv.ParseInt(contentLength, 10, 64)
		if err != nil {
			return err
		}
		ao.ContentLength = n
	}
	if lastModified := h.Get("Last-Modified"); lastModified != "" {
		t, err := http.ParseTime(lastModified)
		if err != nil {
			return err
		}
		ao.ModTime = t
	}

	return nil
}

// isVCardMediaType reports whether mediaType, as returned by
// mime.ParseMediaType, is acceptable for a vCard sent in a response. Besides
// text/vcard, this accepts the legacy text/x-vcard, and text/plain or no
// Content-Type at all, which some servers such as iCloud send. A body which
// isn't a vCard is still rejected when it is decoded.
func isVCardMediaType(mediaType string) bool {
	switch mediaType {
	case vcard.MIMEType, "text/x-vcard", "text/plain", "":
		return true
	default:
		return false
	}
}

func (c *Client) GetAddressObject(ctx context.Context, path string) (*AddressObject, error) {
	req, err := c.ic.NewRequest(http.MethodGet, path, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", vcard.MIMEType)

	resp, err := c.ic.Do(req.WithContext(ctx))
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	var mediaType string
	if h := resp.Header.Get("Content-Type"); h != "" {
		mediaType, _, err = mime.ParseMediaType(h)
		if err != nil {
			return nil, err
		}
	}
	if !isVCardMediaType(mediaType) {
		return nil, fmt.Errorf("carddav: expected Content-Type %q, got %q", vcard.MIMEType, mediaType)
	}

	card, err := vcard.NewDecoder(resp.Body).Decode()
	if err == io.EOF {
		// The body was empty or had no content line at all
		err = io.ErrUnexpectedEOF
	}
	if err != nil {
		return nil, fmt.Errorf("carddav: failed to decode vCard in response with Content-Type %q: %w", mediaType, err)
	}

	ao := &AddressObject{
		Path: c.ic.HrefPath(nil, resp.Request.URL),
		Card: card,
	}
	if err := c.populateAddressObject(ao, resp); err != nil {
		return nil, err
	}
	return ao, nil
}

// PutAddressObject stores card as the address object resource at path. A nil
// opts performs an unconditional PUT; a non-nil opts may carry an If-Match
// (overwrite only if the resource still has this ETag) or If-None-Match
// (create only if absent) precondition. A failed precondition surfaces as an
// *webdav.HTTPError with code 412 Precondition Failed.
//
// The values of opts are sent as is, so they must be in header form: a quoted
// strong entity-tag such as "abc", a weak one such as W/"abc", a
// comma-separated list of these, or "*". If-Match uses the strong comparison,
// so a weak entity-tag never matches it.
func (c *Client) PutAddressObject(ctx context.Context, path string, card vcard.Card, opts *PutAddressObjectOptions) (*AddressObject, error) {
	// TODO: some servers want a Content-Length header, so we can't stream the
	// request body here. See the Radicale issue:
	// https://github.com/Kozea/Radicale/issues/1016

	var buf bytes.Buffer
	if err := vcard.NewEncoder(&buf).Encode(card); err != nil {
		return nil, err
	}

	req, err := c.ic.NewRequest(http.MethodPut, path, &buf)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", vcard.MIMEType)
	if opts != nil {
		if opts.IfMatch.IsSet() {
			req.Header.Set("If-Match", string(opts.IfMatch))
		}
		if opts.IfNoneMatch.IsSet() {
			req.Header.Set("If-None-Match", string(opts.IfNoneMatch))
		}
	}

	resp, err := c.ic.Do(req.WithContext(ctx))
	if err != nil {
		return nil, err
	}
	resp.Body.Close()

	ao := &AddressObject{Path: path}
	if err := c.populateAddressObject(ao, resp); err != nil {
		return nil, err
	}
	return ao, nil
}

// SyncCollection performs a collection synchronization operation on the
// specified resource, as defined in RFC 6578.
func (c *Client) SyncCollection(ctx context.Context, path string, query *SyncQuery) (*SyncResponse, error) {
	var limit *internal.Limit
	if query.Limit > 0 {
		limit = &internal.Limit{NResults: uint(query.Limit)}
	}

	propReq, err := encodeAddressPropReq(&query.DataRequest)
	if err != nil {
		return nil, err
	}

	u, err := c.ic.ResolveHref(path)
	if err != nil {
		return nil, err
	}
	collectionPath := c.ic.HrefPath(nil, u)

	ret := &SyncResponse{}
	var errs []error
	ms, err := c.ic.SyncCollection(ctx, path, query.SyncToken, internal.DepthOne, limit, propReq, func(resp *internal.Response) error {
		p, err := resp.Path()
		if err != nil {
			// The status error is wrapped in an internal.HrefError.
			var httpErr *internal.HTTPError
			switch {
			case !errors.As(err, &httpErr):
				errs = append(errs, err)
			case httpErr.Code == http.StatusNotFound && len(resp.Hrefs) > 0:
				ret.Deleted = append(ret.Deleted, resp.Paths()...)
			case httpErr.Code == http.StatusInsufficientStorage &&
				(len(resp.Hrefs) == 0 || (len(resp.Hrefs) == 1 && internal.SamePath(p, collectionPath))):
				// The server reports a truncated result on the request-URI
				// (RFC 6578 section 3.6).
				ret.Truncated = true
			default:
				errs = append(errs, err)
			}
			return nil
		}

		if internal.SamePath(p, collectionPath) {
			return nil
		}

		var getLastMod internal.GetLastModified
		if err := resp.DecodeProp(&getLastMod); err != nil && !internal.IsNotFound(err) {
			return err
		}

		var getETag internal.GetETag
		if err := resp.DecodeProp(&getETag); err != nil && !internal.IsNotFound(err) {
			return err
		}

		var getContentLength internal.GetContentLength
		if err := resp.DecodeProp(&getContentLength); err != nil && !internal.IsNotFound(err) {
			return err
		}

		o := AddressObject{
			Path:          p,
			ModTime:       time.Time(getLastMod.LastModified),
			ContentLength: getContentLength.Length,
			ETag:          string(getETag.ETag),
		}

		var addrData addressDataResp
		if err := resp.DecodeProp(&addrData); err == nil {
			card, err := vcard.NewDecoder(bytes.NewReader(addrData.Data)).Decode()
			if err != nil {
				return err
			}
			o.Card = card
		} else if !internal.IsNotFound(err) {
			return err
		}

		ret.Updated = append(ret.Updated, o)
		return nil
	})
	if err != nil {
		return nil, err
	}

	ret.SyncToken = ms.SyncToken
	return ret, errors.Join(errs...)
}
