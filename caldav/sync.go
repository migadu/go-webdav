package caldav

import (
	"context"
	"encoding/xml"
	"net/http"
	"strings"

	"github.com/emersion/go-webdav/internal"
)

// SyncBackend is an optional interface a Backend can implement to support
// collection synchronization (RFC 6578): it lets clients fetch only the
// calendar objects that changed since their last sync, instead of listing
// whole calendars. Calendars of such backends should carry a SyncToken.
type SyncBackend interface {
	// SyncCollection returns the changes to the members of the calendar at
	// path since the state identified by query.SyncToken, along with a new
	// sync token identifying the calendar's state after those changes.
	//
	// query.SyncToken is empty for an initial sync, in which case every
	// member is returned in Updated. Otherwise Updated lists the members
	// created or modified since that state and Deleted the paths of the
	// members removed since. If the token is invalid, unknown or expired,
	// return NewInvalidSyncTokenError so the client restarts with an
	// initial sync; if there is no calendar at path, return a 404 error.
	//
	// query.CompRequest is the calendar-data requested by the client, or
	// the zero value if calendar-data was not requested, in which case
	// the objects' Data may be left nil.
	//
	// If query.Limit is positive, at most that many members should be
	// returned in total; if more changes remain, return a token identifying
	// the partial state and set SyncResponse.Truncated. Backends that
	// can't do this may ignore Limit and return all changes.
	//
	// Calendars have no sub-collections, so the DAV:sync-level values "1"
	// and "infinite" are handled alike.
	SyncCollection(ctx context.Context, path string, query *SyncQuery) (*SyncResponse, error)
}

// handleSyncCollection serves a sync-collection REPORT (RFC 6578) on a
// calendar. The Depth header is ignored: the report always applies to the
// calendar's members.
func (h *Handler) handleSyncCollection(r *http.Request, w http.ResponseWriter, sc *internal.SyncCollectionQuery) error {
	b := backend{
		Backend: h.Backend,
		Prefix:  strings.TrimSuffix(h.Prefix, "/"),
	}
	syncBackend, ok := h.Backend.(SyncBackend)
	if !ok || b.resourceTypeAtPath(r.Context(), r.URL.Path) != resourceTypeCalendar {
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
	var calendarData calendarDataReq
	if err := sc.Prop.Decode(&calendarData); err == nil {
		compReq, err := decodeCalendarDataReq(&calendarData)
		if err != nil {
			return err
		}
		query.CompRequest = *compReq
	} else if !internal.IsNotFound(err) {
		return &internal.HTTPError{Code: http.StatusBadRequest, Err: err}
	}

	res, err := syncBackend.SyncCollection(r.Context(), r.URL.Path, &query)
	if err != nil {
		return err
	}

	propfind := internal.PropFind{Prop: sc.Prop}
	return internal.ServeSyncCollection(w, r, res.SyncToken, res.Deleted, res.Truncated, func(mw *internal.MultiStatusWriter) error {
		for i := range res.Updated {
			resp, err := b.propFindCalendarObject(r.Context(), &propfind, true, &res.Updated[i])
			if err != nil {
				return err
			}
			if err := mw.WriteResponse(resp); err != nil {
				return err
			}
		}
		return nil
	})
}

// supportedReportSet returns the DAV:supported-report-set of calendars.
func (b *backend) supportedReportSet() *internal.SupportedReportSet {
	reports := []xml.Name{calendarQueryName, calendarMultigetName}
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
