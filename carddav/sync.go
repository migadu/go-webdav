package carddav

import (
	"context"
	"encoding/xml"
	"net/http"
	"strings"

	"github.com/emersion/go-webdav/internal"
)

// SyncBackend is an optional interface a Backend can implement to support
// collection synchronization (RFC 6578): it lets clients fetch only the
// address objects that changed since their last sync, instead of listing
// whole address books. Address books of such backends should carry a
// SyncToken.
type SyncBackend interface {
	// SyncCollection returns the changes to the members of the address book
	// at path since the state identified by query.SyncToken, along with a
	// new sync token identifying the address book's state after those
	// changes.
	//
	// query.SyncToken is empty for an initial sync, in which case every
	// member is returned in Updated. Otherwise Updated lists the members
	// created or modified since that state and Deleted the paths of the
	// members removed since. If the token is invalid, unknown or expired,
	// return NewInvalidSyncTokenError so the client restarts with an
	// initial sync; if there is no address book at path, return a 404
	// error.
	//
	// query.DataRequest is the address-data requested by the client (with
	// AllProp set if it asked for whole vCards), or the zero value if
	// address-data was not requested, in which case the objects' Card may
	// be left nil.
	//
	// If query.Limit is positive, at most that many members should be
	// returned in total; if more changes remain, return a token identifying
	// the partial state and set SyncResponse.Truncated. Backends that
	// can't do this may ignore Limit and return all changes.
	//
	// Address books have no sub-collections, so the DAV:sync-level values
	// "1" and "infinite" are handled alike.
	SyncCollection(ctx context.Context, path string, query *SyncQuery) (*SyncResponse, error)
}

// handleSyncCollection serves a sync-collection REPORT (RFC 6578) on an
// address book. The Depth header is ignored: the report always applies to the
// address book's members.
func (h *Handler) handleSyncCollection(r *http.Request, w http.ResponseWriter, sc *internal.SyncCollectionQuery) error {
	b := backend{
		Backend: h.Backend,
		Prefix:  strings.TrimSuffix(h.Prefix, "/"),
	}
	syncBackend, ok := h.Backend.(SyncBackend)
	if !ok || b.resourceTypeAtPath(r.URL.Path) != resourceTypeAddressBook {
		return internal.NewConditionError(http.StatusForbidden, internal.SupportedReportName)
	}

	syncToken, err := internal.CheckSyncCollection(sc)
	if err != nil {
		return err
	}
	query := SyncQuery{SyncToken: syncToken}
	if sc.Limit != nil {
		query.Limit = int(sc.Limit.NResults)
	}
	dataReq, err := decodeAddressDataProp(sc.Prop)
	if err != nil {
		return err
	}
	query.DataRequest = *dataReq

	res, err := syncBackend.SyncCollection(r.Context(), r.URL.Path, &query)
	if err != nil {
		return err
	}

	propfind := internal.PropFind{Prop: sc.Prop}
	resps := make([]internal.Response, 0, len(res.Updated))
	for i := range res.Updated {
		resp, err := b.propFindAddressObject(r.Context(), &propfind, true, &res.Updated[i])
		if err != nil {
			return err
		}
		resps = append(resps, *resp)
	}

	ms, err := internal.NewSyncCollectionMultiStatus(r.URL.Path, res.SyncToken, resps, res.Deleted, res.Truncated)
	if err != nil {
		return err
	}
	return internal.ServeMultiStatus(w, ms)
}

// supportedReportSet returns the DAV:supported-report-set of address books.
func (b *backend) supportedReportSet() *internal.SupportedReportSet {
	reports := []xml.Name{addressBookQueryName, addressBookMultigetName}
	if _, ok := b.Backend.(SyncBackend); ok {
		reports = append(reports, internal.SyncCollectionName)
	}
	return internal.NewSupportedReportSet(reports...)
}

// NewInvalidSyncTokenError returns an error reporting that the sync token of
// a sync-collection REPORT is invalid, unknown or has expired. It is served as
// 403 Forbidden with the DAV:valid-sync-token precondition (RFC 6578 section
// 3.2), which tells clients to restart with an initial sync.
func NewInvalidSyncTokenError() error {
	return internal.NewConditionError(http.StatusForbidden, internal.ValidSyncTokenName)
}
