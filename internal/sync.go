package internal

import (
	"encoding/xml"
	"fmt"
	"net/http"
	"strings"
)

// Collection synchronization (RFC 6578) and supported reports (RFC 3253).

var (
	SupportedReportSetName = xml.Name{Space: Namespace, Local: "supported-report-set"}
	SyncTokenName          = xml.Name{Space: Namespace, Local: "sync-token"}
	SyncCollectionName     = xml.Name{Space: Namespace, Local: "sync-collection"}

	// Pre- and postcondition codes, see RFC 3253 section 3.6 and RFC 6578
	// sections 3.2 and 3.6.
	SupportedReportName             = xml.Name{Space: Namespace, Local: "supported-report"}
	ValidSyncTokenName              = xml.Name{Space: Namespace, Local: "valid-sync-token"}
	NumberOfMatchesWithinLimitsName = xml.Name{Space: Namespace, Local: "number-of-matches-within-limits"}
)

// NewConditionError returns an error that is served with the given HTTP status
// code and a DAV:error body holding a single empty pre- or postcondition
// element, as described in RFC 4918 section 16.
func NewConditionError(code int, name xml.Name) *HTTPError {
	return &HTTPError{
		Code: code,
		Err:  &Error{Raw: []RawXMLValue{*NewRawXMLElement(name, nil, nil)}},
	}
}

// https://tools.ietf.org/html/rfc6578#section-4
type SyncToken struct {
	XMLName xml.Name `xml:"DAV: sync-token"`
	Token   string   `xml:",chardata"`
}

// https://tools.ietf.org/html/rfc3253#section-3.1.5
type SupportedReportSet struct {
	XMLName         xml.Name          `xml:"DAV: supported-report-set"`
	SupportedReport []SupportedReport `xml:"supported-report"`
}

// https://tools.ietf.org/html/rfc3253#section-3.1.5
type SupportedReport struct {
	XMLName xml.Name `xml:"DAV: supported-report"`
	Report  Report   `xml:"report"`
}

// https://tools.ietf.org/html/rfc3253#section-3.1.5
type Report struct {
	XMLName xml.Name      `xml:"DAV: report"`
	Raw     []RawXMLValue `xml:",any"`
}

// NewSupportedReportSet returns a DAV:supported-report-set listing the
// reports with the given root element names.
func NewSupportedReportSet(names ...xml.Name) *SupportedReportSet {
	reports := make([]SupportedReport, len(names))
	for i, name := range names {
		reports[i].Report.Raw = []RawXMLValue{*NewRawXMLElement(name, nil, nil)}
	}
	return &SupportedReportSet{SupportedReport: reports}
}

// CheckSyncCollection validates a sync-collection REPORT request (RFC 6578
// section 3.2) addressed to a collection without sub-collections, such as a
// calendar or an address book, and returns its sync token. For such
// collections the sync-level values "1" and "infinite" are equivalent; a
// missing sync-level is accepted as "1", and the Depth spelling "infinity" as
// "infinite".
func CheckSyncCollection(q *SyncCollectionQuery) (syncToken string, err error) {
	switch strings.TrimSpace(q.SyncLevel) {
	case "", "1", "infinite", "infinity":
	default:
		return "", HTTPErrorf(http.StatusBadRequest, "webdav: unsupported sync-level %q", q.SyncLevel)
	}
	if q.Prop == nil {
		return "", HTTPErrorf(http.StatusBadRequest, "webdav: missing prop element in sync-collection request")
	}
	return strings.TrimSpace(q.SyncToken), nil
}

// SyncErrorResponses returns the responses for the members a backend failed
// to report in a sync-collection REPORT on the collection at collectionPath,
// given by errResps as HrefErrorResponses returns them. updated and deleted
// are the paths of the members the backend did report, as changed and as
// removed, and prop holds the requested properties.
//
// RFC 6578 section 3.2 lists each member URL once, and only lets the
// response for a removed member carry a DAV:status. So an error about a
// member already reported, in updated, deleted or an earlier error, is left
// out; a 404 Not Found error reports the member as removed; and any other
// error reports the requested properties of a changed member in a propstat
// with the status, DAV:error and description of the error. If prop is empty,
// so is the prop element of that propstat, which RFC 4918 section 14.22
// allows.
//
// An error about the collection itself fails the whole request: it is
// returned, with its status, DAV:error and description. The collection isn't
// one of its members, and the only response for the request-URI is the 507
// Insufficient Storage of a truncated result (section 3.6), which doesn't
// tell the client about an error.
func SyncErrorResponses(collectionPath string, errResps []Response, prop *Prop, updated, deleted []string) ([]Response, error) {
	var names []RawXMLValue
	for _, raw := range prop.Raw {
		if name, ok := raw.XMLName(); ok {
			names = append(names, *NewRawXMLElement(name, nil, nil))
		}
	}

	// Paths are compared as by SamePath, through a map since updated can
	// list a whole collection
	reported := make(map[string]bool, len(updated)+len(deleted))
	for _, p := range updated {
		reported[strings.TrimSuffix(p, "/")] = true
	}
	for _, p := range deleted {
		reported[strings.TrimSuffix(p, "/")] = true
	}

	var resps []Response
	for _, resp := range errResps {
		if SamePath(resp.Hrefs[0].Path, collectionPath) {
			return nil, &HTTPError{
				Code: resp.Status.Code,
				Err:  describedError(resp.Error, resp.ResponseDescription),
			}
		}
		key := strings.TrimSuffix(resp.Hrefs[0].Path, "/")
		if reported[key] {
			continue
		}
		reported[key] = true
		if resp.Status.Code != http.StatusNotFound {
			resp.PropStats = []PropStat{{
				Prop:                Prop{Raw: names},
				Status:              *resp.Status,
				Error:               resp.Error,
				ResponseDescription: resp.ResponseDescription,
			}}
			resp.Status, resp.Error, resp.ResponseDescription = nil, nil, ""
		}
		resps = append(resps, resp)
	}
	return resps, nil
}

// SyncChangedResponse returns resp, the response built by NewPropFindResponse
// for a changed member in a sync-collection REPORT, in the form RFC 6578
// section 3.2 requires: with at least one propstat and no status. The 200 OK
// status of a request for no property, with an empty prop element, is moved
// to a propstat with an empty prop element.
func SyncChangedResponse(resp *Response) *Response {
	if resp.Status != nil && len(resp.PropStats) == 0 {
		resp.PropStats = []PropStat{{Status: *resp.Status}}
		resp.Status = nil
	}
	return resp
}

// ServeSyncCollection serves the response to a sync-collection REPORT on the
// collection at r.URL.Path (RFC 6578 section 3.2), with the new sync token
// syncToken. writeUpdated writes the responses for the updated members to mw,
// as StreamMultiStatus describes, and then each path in deleted is reported
// as 404 Not Found. If truncated is set, the collection itself is reported as
// 507 Insufficient Storage with the DAV:number-of-matches-within-limits
// postcondition (section 3.6).
func ServeSyncCollection(w http.ResponseWriter, r *http.Request, syncToken string, deleted []string, truncated bool, writeUpdated func(mw *MultiStatusWriter) error) error {
	if syncToken == "" {
		return fmt.Errorf("webdav: missing sync-token in sync-collection response")
	}
	return StreamMultiStatus(w, r, func(mw *MultiStatusWriter) error {
		if err := writeUpdated(mw); err != nil {
			return err
		}
		for _, p := range deleted {
			resp := Response{
				Hrefs:  []Href{{Path: p}},
				Status: &Status{Code: http.StatusNotFound},
			}
			if err := mw.WriteResponse(&resp); err != nil {
				return err
			}
		}
		if truncated {
			resp := Response{
				Hrefs:  []Href{{Path: r.URL.Path}},
				Status: &Status{Code: http.StatusInsufficientStorage},
				Error: &Error{Raw: []RawXMLValue{
					*NewRawXMLElement(NumberOfMatchesWithinLimitsName, nil, nil),
				}},
			}
			if err := mw.WriteResponse(&resp); err != nil {
				return err
			}
		}
		mw.SyncToken = syncToken
		return nil
	})
}
