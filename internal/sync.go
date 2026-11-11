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
