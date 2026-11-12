package caldav

import (
	"bytes"
	"context"
	"encoding/xml"
	"errors"
	"fmt"
	"mime"
	"net/http"
	"path"
	"strconv"
	"strings"
	"time"

	"github.com/emersion/go-ical"
	"github.com/emersion/go-webdav"
	"github.com/emersion/go-webdav/internal"
	"github.com/emersion/go-webdav/internal/push"
)

// TODO if nothing more Caldav-specific needs to be added this should be merged with carddav.PutAddressObjectOptions
type PutCalendarObjectOptions struct {
	// IfNoneMatch indicates that the client does not want to overwrite
	// an existing resource. Check it with MatchETagWeak.
	IfNoneMatch webdav.ConditionalMatch
	// IfMatch provides the ETag of the resource that the client intends
	// to overwrite, can be "". Check it with MatchETag.
	IfMatch webdav.ConditionalMatch
}

// Backend is a CalDAV server backend.
//
// ListCalendars, ListCalendarObjects and QueryCalendarObjects can report
// resources that couldn't be loaded along with the others, and
// DeleteCalendarObject can report the members of a calendar it couldn't
// delete, by returning an error made only of errors created by
// webdav.NewHrefError (a single one, or several combined with errors.Join).
// The server then replies with a 207 Multi-Status response listing each
// failed resource with its own status, instead of failing the whole request.
// Any other error fails the whole request.
//
// The responses to PROPFIND and REPORT requests are streamed: each resource
// is written to the client as soon as it's built. An error failing the whole
// request once some resources have been written, such as ListCalendarObjects
// failing for a calendar of a Depth: infinity PROPFIND after the previous
// calendars were listed, can't change the status of the response anymore:
// the response is aborted instead, so that the client sees it's incomplete.
type Backend interface {
	CalendarHomeSetPath(ctx context.Context) (string, error)

	CreateCalendar(ctx context.Context, calendar *Calendar) error
	ListCalendars(ctx context.Context) ([]Calendar, error)
	GetCalendar(ctx context.Context, path string) (*Calendar, error)

	GetCalendarObject(ctx context.Context, path string, req *CalendarCompRequest) (*CalendarObject, error)
	ListCalendarObjects(ctx context.Context, path string, req *CalendarCompRequest) ([]CalendarObject, error)
	QueryCalendarObjects(ctx context.Context, path string, query *CalendarQuery) ([]CalendarObject, error)
	PutCalendarObject(ctx context.Context, path string, calendar *ical.Calendar, opts *PutCalendarObjectOptions) (*CalendarObject, error)
	// DeleteCalendarObject deletes the resource at path, a calendar object
	// or a calendar. Members of a calendar that couldn't be deleted can be
	// reported with webdav.NewHrefError; as per RFC 4918 section 9.6.1, the
	// members that were deleted shouldn't be reported.
	DeleteCalendarObject(ctx context.Context, path string) error

	webdav.UserPrincipalBackend
}

// CalendarUpdater is an optional interface a Backend can implement to let
// clients modify the properties of calendars with PROPPATCH.
type CalendarUpdater interface {
	// UpdateCalendar applies all of update to the calendar at path, or none
	// of it.
	UpdateCalendar(ctx context.Context, path string, update *CalendarUpdate) error
}

// CopyCalendarObjectOptions holds the options of a COPY request for a
// calendar object.
type CopyCalendarObjectOptions struct {
	// NoOverwrite indicates that the client doesn't want a calendar object
	// already at the destination to be replaced ("Overwrite: F"): the copy
	// must then fail with 412 Precondition Failed.
	NoOverwrite bool
}

// MoveCalendarObjectOptions holds the options of a MOVE request for a
// calendar object.
type MoveCalendarObjectOptions struct {
	// NoOverwrite indicates that the client doesn't want a calendar object
	// already at the destination to be replaced ("Overwrite: F"): the move
	// must then fail with 412 Precondition Failed.
	NoOverwrite bool
}

// CalendarObjectCopyMover is an optional interface a Backend can implement to
// let clients copy and move calendar objects with COPY and MOVE, for instance
// to move an event from a calendar to another. Calendars cannot be copied or
// moved.
//
// The Handler calls these methods only for src and dst paths of calendar
// objects, that is, of resources in a calendar, and dst is never src.
// created is true if there was no calendar object at dst, false if one was
// replaced. If there is no calendar object at src, they fail with 404 Not
// Found, and if there is no calendar to hold dst, with 409 Conflict (RFC 4918
// sections 9.8.5 and 9.9.4).
//
// The preconditions of RFC 4791 section 5.3.2.1 apply to the destination, and
// can be reported with NewPreconditionError: for instance no-uid-conflict, if
// the calendar of dst already holds another calendar object with the same
// UID, or if dst is a calendar object with another UID, and
// supported-calendar-component, if the calendar of dst doesn't support the
// component of the calendar object.
type CalendarObjectCopyMover interface {
	// CopyCalendarObject copies the calendar object at src to dst.
	CopyCalendarObject(ctx context.Context, src, dst string, opts *CopyCalendarObjectOptions) (created bool, err error)
	// MoveCalendarObject moves the calendar object at src to dst.
	MoveCalendarObject(ctx context.Context, src, dst string, opts *MoveCalendarObjectOptions) (created bool, err error)
}

// InboxBackend is an optional interface a Backend can implement to support
// scheduling (RFC 6638): delivering scheduling messages, such as invitations,
// to the user's clients, and letting them reply.
//
// Implementing InboxBackend asserts that the backend performs implicit
// scheduling. The server then advertises the "calendar-auto-schedule"
// capability, which tells clients that the server sends and processes
// scheduling messages as calendar objects are stored and removed (RFC 6638
// section 3), so that they mustn't send them themselves, e.g. by email. The
// server can't do that on its own: when PutCalendarObject and
// DeleteCalendarObject store or remove a scheduling object resource, a
// calendar object whose ORGANIZER, or one of whose ATTENDEEs, is one of the
// user's Inbox.UserAddressSet (section 3.1), the backend must:
//
//   - for an event the user organizes, send a REQUEST to the attendees when
//     it's created or modified, and a CANCEL to the attendees it's removed
//     for, or to all of them when it's deleted (section 3.2.1);
//   - for an event the user attends, send a REPLY to the organizer when the
//     user's PARTSTAT changes, and a REPLY declining it when it's deleted
//     (section 3.2.2);
//
// unless the SCHEDULE-AGENT parameter is CLIENT or NONE (section 7.1). The
// messages can be sent to users of other servers as iMIP email (RFC 6047).
// The backend must also process the scheduling messages the user receives
// (section 4): add or update the invitations in the user's calendars, apply
// the replies of attendees to the events the user organizes, and store a copy
// of the messages in the Inbox, where clients find them.
//
// For users GetInbox returns an Inbox for, the server serves the
// CALDAV:schedule-inbox-URL and CALDAV:calendar-user-address-set properties
// of the principal, and the Inbox collection at Inbox.Path. The scheduling
// messages in the Inbox are iTIP messages (RFC 5546), whose METHOD is set:
// the server lists them with ListCalendarObjects, queries them with
// QueryCalendarObjects (RFC 6638 section 2.3) and fetches them with
// GetCalendarObject, or GetCalendarObjects for CalendarObjectsBackend. To
// reply to a message, clients store the event with the user's PARTSTAT
// changed with PutCalendarObject, and then remove the message from the Inbox
// with DeleteCalendarObject.
//
// The rest of RFC 6638 isn't supported: there is no scheduling Outbox, so no
// CALDAV:schedule-outbox-URL and no free-busy requests by POST (sections 2.1
// and 5); no Schedule-Tag or If-Schedule-Tag-Match headers (sections 3.2.10,
// 8.2 and 8.3); no CALDAV:schedule-default-calendar-URL or
// CALDAV:schedule-calendar-transp properties (section 9); and the scheduling
// preconditions of section 3.2.4 aren't checked. The Schedule-Reply request
// header (section 8.1), with which clients ask not to send a REPLY when an
// event is deleted, isn't passed to DeleteCalendarObject: backends can read
// it in an HTTP middleware.
type InboxBackend interface {
	// GetInbox returns the scheduling Inbox of the current user. It returns
	// nil if the user isn't enabled for scheduling: calendar-auto-schedule
	// isn't advertised to such users.
	GetInbox(ctx context.Context) (*Inbox, error)
}

// Handler handles CalDAV HTTP requests. It can be used to create a CalDAV
// server.
//
// The principal, the calendar home set, the calendars, the scheduling Inbox
// and the calendar objects report the access control properties of RFC 3744
// section 5 clients ask for: the current user principal is their DAV:owner,
// the collection holding it their DAV:principal-collection-set, and
// DAV:supported-privilege-set describes the privileges reported in
// DAV:current-user-privilege-set, which the ReadOnly fields control. DAV:acl
// isn't reported, and can't be modified with the ACL method.
type Handler struct {
	Backend Backend
	Prefix  string
}

func (h *Handler) newBackend() backend {
	p := h.Prefix
	if p != "" {
		p = path.Clean(p)
		if !strings.HasPrefix(p, "/") {
			p = "/" + p
		}
		p = strings.TrimSuffix(p, "/")
	}
	return backend{
		Backend: h.Backend,
		Prefix:  p,
	}
}

// ServeHTTP implements http.Handler.
func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if h.Backend == nil {
		http.Error(w, "caldav: no backend available", http.StatusInternalServerError)
		return
	}

	b := h.newBackend()
	if internal.SamePath(r.URL.Path, "/.well-known/caldav") {
		principalPath, err := h.Backend.CurrentUserPrincipal(r.Context())
		if err != nil {
			// An HTTPError keeps its status: the server may require
			// authentication here (RFC 6764 section 5)
			internal.ServeError(w, fmt.Errorf("caldav: failed to determine current user principal: %w", err))
			return
		}
		if principalPath == "" {
			// Without a current user, redirect to the context path of the
			// service, the root of the handler (RFC 6764 section 5), rather
			// than resolve an empty path to "/.well-known/"
			principalPath = b.Prefix + "/"
		}

		// The location depends on the user, so caches must check back
		// before reusing the redirect (RFC 6764 section 5)
		w.Header().Set("Cache-Control", "no-cache")
		http.Redirect(w, r, principalPath, http.StatusPermanentRedirect)
		return
	}

	if !b.inPrefix(r.URL.Path) {
		if r.Method == http.MethodDelete {
			if pb, ok := h.Backend.(PushBackend); ok {
				if handled, err := h.servePush(w, r, pb); handled {
					if err != nil {
						internal.ServeError(w, err)
					}
					return
				}
			}
		}
		internal.ServeError(w, internal.HTTPErrorf(http.StatusNotFound, "caldav: resource outside handler prefix"))
		return
	}

	r, err := internal.DecodeRequestBody(w, r)
	if err != nil {
		internal.ServeError(w, err)
		return
	}

	switch r.Method {
	case "REPORT":
		err = h.handleReport(w, r)
	case "MKCALENDAR":
		err = h.handleMkcalendar(w, r)
	default:
		if pb, ok := h.Backend.(PushBackend); ok {
			var handled bool
			if handled, err = h.servePush(w, r, pb); handled {
				break
			}
		}
		hh := internal.Handler{Backend: &b}
		hh.ServeHTTP(w, r)
	}

	if err != nil {
		internal.ServeError(w, err)
	}
}

// handleMkcalendar handles MKCALENDAR requests, see RFC 4791 section 5.3.1.
func (h *Handler) handleMkcalendar(w http.ResponseWriter, r *http.Request) error {
	b := h.newBackend()
	if b.resourceTypeAtPath(r.Context(), r.URL.Path) != resourceTypeCalendar {
		return newPreconditionError(http.StatusForbidden, PreconditionCalendarCollectionLocationOk)
	}

	cal := Calendar{Path: r.URL.Path}
	if !internal.IsRequestBodyEmpty(r) {
		var m mkcalendarReq
		if err := internal.DecodeXMLRequest(r, &m); err != nil {
			return err
		}
		if err := setCalendarProps(&cal, &m.Set.Prop); err != nil {
			return err
		}
	}

	if err := h.Backend.CreateCalendar(r.Context(), &cal); err != nil {
		return err
	}
	w.WriteHeader(http.StatusCreated)
	return nil
}

func (h *Handler) handleReport(w http.ResponseWriter, r *http.Request) error {
	var report reportReq
	if err := internal.DecodeXMLRequest(r, &report); err != nil {
		return err
	}

	if report.Query != nil {
		return h.handleQuery(r, w, report.Query)
	} else if report.Multiget != nil {
		return h.handleMultiget(r, w, report.Multiget)
	} else if report.SyncCollection != nil {
		return h.handleSyncCollection(r, w, report.SyncCollection)
	}
	return internal.HTTPErrorf(http.StatusBadRequest, "caldav: expected calendar-query, calendar-multiget or sync-collection element in REPORT request")
}

func decodeTextMatch(el *textMatch) (*TextMatch, error) {
	if !internal.IsSupportedCollation(el.Collation) {
		return nil, newPreconditionError(http.StatusForbidden, PreconditionSupportedCollation)
	}
	collation := el.Collation
	// "default" names the default collation (RFC 4791 section 7.5), which
	// backends get as an empty Collation
	if collation == internal.CollationDefault {
		collation = ""
	}
	return &TextMatch{
		Text:            el.Text,
		Collation:       collation,
		NegateCondition: bool(el.NegateCondition),
	}, nil
}

// newSupportedCollationSet returns the CALDAV:supported-collation-set of the
// resources which support calendar-query, the REPORT which does text
// matching (RFC 4791 section 7.5.1).
func newSupportedCollationSet() *supportedCollationSet {
	return &supportedCollationSet{Collations: internal.SupportedCollations()}
}

func decodeParamFilter(el *paramFilter) (*ParamFilter, error) {
	pf := &ParamFilter{Name: el.Name}
	if el.IsNotDefined != nil {
		// RFC 4791 section 9.7.3: (is-not-defined | text-match?)
		if el.TextMatch != nil {
			return nil, newPreconditionError(http.StatusForbidden, PreconditionValidFilter)
		}
		pf.IsNotDefined = true
	}
	if el.TextMatch != nil {
		tm, err := decodeTextMatch(el.TextMatch)
		if err != nil {
			return nil, err
		}
		pf.TextMatch = tm
	}
	return pf, nil
}

func decodePropFilter(el *propFilter) (*PropFilter, error) {
	pf := &PropFilter{Name: el.Name}
	if el.IsNotDefined != nil {
		// RFC 4791 section 9.7.2: (is-not-defined | ((time-range |
		// text-match)?, param-filter*))
		if el.TextMatch != nil || el.TimeRange != nil || len(el.ParamFilter) > 0 {
			return nil, newPreconditionError(http.StatusForbidden, PreconditionValidFilter)
		}
		pf.IsNotDefined = true
	}
	if el.TextMatch != nil {
		tm, err := decodeTextMatch(el.TextMatch)
		if err != nil {
			return nil, err
		}
		pf.TextMatch = tm
	}
	if el.TimeRange != nil {
		pf.Start, pf.End = el.TimeRange.bounds()
	}
	for _, paramEl := range el.ParamFilter {
		paramFi, err := decodeParamFilter(&paramEl)
		if err != nil {
			return nil, err
		}
		pf.ParamFilter = append(pf.ParamFilter, *paramFi)
	}
	return pf, nil
}

func decodeCompFilter(el *compFilter) (*CompFilter, error) {
	cf := &CompFilter{Name: el.Name}
	if el.IsNotDefined != nil {
		// RFC 4791 section 9.7.1: (is-not-defined | (time-range?,
		// prop-filter*, comp-filter*))
		if el.TimeRange != nil || len(el.PropFilters) > 0 || len(el.CompFilters) > 0 {
			return nil, newPreconditionError(http.StatusForbidden, PreconditionValidFilter)
		}
		cf.IsNotDefined = true
	}
	if el.TimeRange != nil {
		cf.Start, cf.End = el.TimeRange.bounds()
	}
	for _, pfEl := range el.PropFilters {
		pf, err := decodePropFilter(&pfEl)
		if err != nil {
			return nil, err
		}
		cf.Props = append(cf.Props, *pf)
	}
	for _, childEl := range el.CompFilters {
		child, err := decodeCompFilter(&childEl)
		if err != nil {
			return nil, err
		}
		cf.Comps = append(cf.Comps, *child)
	}
	return cf, nil
}

func decodeComp(comp *comp) (*CalendarCompRequest, error) {
	if comp == nil {
		return nil, internal.HTTPErrorf(http.StatusBadRequest, "caldav: unexpected empty calendar-data in request")
	}
	if comp.Allprop != nil && len(comp.Prop) > 0 {
		return nil, internal.HTTPErrorf(http.StatusBadRequest, "caldav: only one of allprop or prop can be specified in calendar-data")
	}
	if comp.Allcomp != nil && len(comp.Comp) > 0 {
		return nil, internal.HTTPErrorf(http.StatusBadRequest, "caldav: only one of allcomp or comp can be specified in calendar-data")
	}

	req := &CalendarCompRequest{
		Name:     comp.Name,
		AllProps: comp.Allprop != nil,
		AllComps: comp.Allcomp != nil,
	}
	for _, p := range comp.Prop {
		req.Props = append(req.Props, p.Name)
	}
	for _, c := range comp.Comp {
		comp, err := decodeComp(&c)
		if err != nil {
			return nil, err
		}
		req.Comps = append(req.Comps, *comp)
	}
	return req, nil
}

// decodeRequiredTimeRange returns the start and end of the time range of the
// element called name. RFC 4791 sections 9.6.5 to 9.6.7 require both, the end
// after the start.
func decodeRequiredTimeRange(name string, tr *requiredTimeRange) (start, end time.Time, err error) {
	if err := tr.check(name); err != nil {
		return time.Time{}, time.Time{}, &internal.HTTPError{Code: http.StatusBadRequest, Err: err}
	}
	return time.Time(*tr.Start), time.Time(*tr.End), nil
}

func decodeCalendarDataReq(calendarData *calendarDataReq) (*CalendarCompRequest, error) {
	// RFC 4791 section 9.6: (comp?, (expand | limit-recurrence-set)?,
	// limit-freebusy-set?)
	for _, el := range []struct {
		name string
		n    int
	}{
		{"expand", len(calendarData.Expand)},
		{"limit-recurrence-set", len(calendarData.LimitRecurrenceSet)},
		{"limit-freebusy-set", len(calendarData.LimitFreeBusySet)},
	} {
		if el.n > 1 {
			return nil, internal.HTTPErrorf(http.StatusBadRequest, "caldav: calendar-data can't hold more than one %s", el.name)
		}
	}
	if len(calendarData.Expand) > 0 && len(calendarData.LimitRecurrenceSet) > 0 {
		return nil, internal.HTTPErrorf(http.StatusBadRequest, "caldav: only one of expand or limit-recurrence-set can be specified in calendar-data")
	}

	var req *CalendarCompRequest
	if calendarData.Comp == nil {
		req = &CalendarCompRequest{
			AllProps: true,
			AllComps: true,
		}
	} else {
		var err error
		if req, err = decodeComp(calendarData.Comp); err != nil {
			return nil, err
		}
	}

	if len(calendarData.Expand) > 0 {
		start, end, err := decodeRequiredTimeRange("expand", &calendarData.Expand[0].requiredTimeRange)
		if err != nil {
			return nil, err
		}
		req.Expand = &CalendarExpandRequest{Start: start, End: end}
	}
	if len(calendarData.LimitRecurrenceSet) > 0 {
		start, end, err := decodeRequiredTimeRange("limit-recurrence-set", &calendarData.LimitRecurrenceSet[0].requiredTimeRange)
		if err != nil {
			return nil, err
		}
		req.LimitRecurrenceSet = &CalendarLimitRequest{Start: start, End: end}
	}
	if len(calendarData.LimitFreeBusySet) > 0 {
		start, end, err := decodeRequiredTimeRange("limit-freebusy-set", &calendarData.LimitFreeBusySet[0].requiredTimeRange)
		if err != nil {
			return nil, err
		}
		req.LimitFreeBusySet = &CalendarLimitRequest{Start: start, End: end}
	}

	return req, nil
}

// decodeCalendarDataProp decodes the CALDAV:calendar-data element of the
// DAV:prop element of a REPORT request.
func decodeCalendarDataProp(prop *internal.Prop) (*CalendarCompRequest, error) {
	var calendarData calendarDataReq
	if err := prop.Decode(&calendarData); internal.IsNotFound(err) {
		return &CalendarCompRequest{}, nil
	} else if err != nil {
		return nil, &internal.HTTPError{Code: http.StatusBadRequest, Err: err}
	}
	return decodeCalendarDataReq(&calendarData)
}

func (h *Handler) handleQuery(r *http.Request, w http.ResponseWriter, query *calendarQuery) error {
	propfind := internal.PropFind{
		Prop:     query.Prop,
		AllProp:  query.AllProp,
		PropName: query.PropName,
	}

	var q CalendarQuery
	if query.Prop != nil {
		req, err := decodeCalendarDataProp(query.Prop)
		if err != nil {
			return err
		}
		q.CompRequest = *req
	}
	cf, err := decodeCompFilter(&query.Filter.CompFilter)
	if err != nil {
		return err
	}
	q.CompFilter = *cf

	cos, err := h.Backend.QueryCalendarObjects(r.Context(), r.URL.Path, &q)
	errResps, err := internal.HrefErrorResponses(err)
	if err != nil {
		return err
	}

	b := h.newBackend()
	return internal.StreamMultiStatus(w, r, func(mw *internal.MultiStatusWriter) error {
		for i := range cos {
			resp, err := b.propFindCalendarObject(r.Context(), &propfind, true, &cos[i])
			if err != nil {
				return err
			}
			if err := mw.WriteResponse(resp); err != nil {
				return err
			}
		}
		return mw.WriteResponses(errResps)
	})
}

// CalendarObjectsBackend is an optional interface a Backend can implement to
// fetch the calendar objects of a calendar-multiget REPORT in a single call,
// rather than calling GetCalendarObject for each of them, in which case the
// response for each object is sent before the next one is fetched.
type CalendarObjectsBackend interface {
	// GetCalendarObjects returns the calendar objects found at paths, in any order.
	// The Path of each must be the path it was requested by: paths without
	// an object are reported as 404 Not Found, or with the per-resource error
	// reported via webdav.NewHrefError. Any other error fails the whole request.
	GetCalendarObjects(ctx context.Context, paths []string, req *CalendarCompRequest) ([]CalendarObject, error)
}

// handleMultiget serves a calendar-multiget REPORT. Without
// CalendarObjectsBackend, the response for each calendar object is written
// as soon as the object is fetched, before the next one is.
func (h *Handler) handleMultiget(r *http.Request, w http.ResponseWriter, multiget *calendarMultiget) error {
	ctx := r.Context()
	propfind := internal.PropFind{
		Prop:     multiget.Prop,
		AllProp:  multiget.AllProp,
		PropName: multiget.PropName,
	}

	var dataReq CalendarCompRequest
	if multiget.Prop != nil {
		decoded, err := decodeCalendarDataProp(multiget.Prop)
		if err != nil {
			return err
		}
		dataReq = *decoded
	}

	b := h.newBackend()

	var objects map[string]*CalendarObject
	var objErrResps map[string]internal.Response
	if mb, ok := h.Backend.(CalendarObjectsBackend); ok {
		paths := make([]string, 0, len(multiget.Hrefs))
		for _, href := range multiget.Hrefs {
			if b.inPrefix(href.Path) {
				paths = append(paths, href.Path)
			}
		}
		objects = make(map[string]*CalendarObject, len(paths))
		if len(paths) > 0 {
			l, err := mb.GetCalendarObjects(ctx, paths, &dataReq)
			errResps, err := internal.HrefErrorResponses(err)
			if err != nil {
				return err
			}
			for i := range l {
				objects[l[i].Path] = &l[i]
			}
			objErrResps = make(map[string]internal.Response, len(errResps))
			for _, resp := range errResps {
				if len(resp.Hrefs) > 0 {
					objErrResps[resp.Hrefs[0].Path] = resp
				}
			}
		}
	}

	return internal.StreamMultiStatus(w, r, func(mw *internal.MultiStatusWriter) error {
		for _, href := range multiget.Hrefs {
			if !b.inPrefix(href.Path) {
				resp := internal.NewErrorResponse(href.Path, internal.HTTPErrorf(http.StatusNotFound, "caldav: calendar object not found"))
				if err := mw.WriteResponse(resp); err != nil {
					return err
				}
				continue
			}

			var co *CalendarObject
			var err error
			if objects != nil {
				if errResp, ok := objErrResps[href.Path]; ok {
					if err := mw.WriteResponse(&errResp); err != nil {
						return err
					}
					continue
				}
				if co = objects[href.Path]; co == nil {
					err = internal.HTTPErrorf(http.StatusNotFound, "caldav: calendar object not found")
				}
			} else {
				co, err = h.Backend.GetCalendarObject(ctx, href.Path, &dataReq)
			}

			var resp *internal.Response
			if err != nil {
				resp = internal.NewErrorResponse(href.Path, err)
			} else if resp, err = b.propFindCalendarObject(ctx, &propfind, true, co); err != nil {
				return err
			}
			if err := mw.WriteResponse(resp); err != nil {
				return err
			}
		}
		return nil
	})
}

// backend serves a single request.
type backend struct {
	Backend Backend
	Prefix  string

	// The scheduling Inbox of the current user, see scheduleInbox
	inbox       *Inbox
	inboxErr    error
	inboxLoaded bool
}

type resourceType int

const (
	resourceTypeRoot resourceType = iota
	resourceTypeUserPrincipal
	resourceTypeCalendarHomeSet
	resourceTypeCalendar
	resourceTypeCalendarObject
)

// resourceTypeScheduleInbox is the type of the scheduling Inbox, which is
// found by its path rather than by its depth.
const resourceTypeScheduleInbox resourceType = -1

const resourceTypeInvalid resourceType = -2

func (b *backend) resourceTypeAtPath(ctx context.Context, reqPath string) resourceType {
	reqPath = path.Clean(reqPath)
	if !b.inPrefix(reqPath) {
		return resourceTypeInvalid
	}
	p := strings.TrimPrefix(reqPath, b.Prefix)
	if !strings.HasPrefix(p, "/") {
		p = "/" + p
	}
	if p == "/" {
		return resourceTypeRoot
	}
	// Like the other backend paths, the Inbox path includes the prefix. An
	// error is reported by the requests for the Inbox properties.
	if inbox, err := b.scheduleInbox(ctx); err == nil && inbox != nil {
		switch inboxPath := path.Clean(inbox.Path); {
		case reqPath == inboxPath:
			return resourceTypeScheduleInbox
		case path.Dir(reqPath) == inboxPath:
			return resourceTypeCalendarObject
		}
	}
	return resourceType(len(strings.Split(p, "/")) - 1)
}

// inScheduleInbox reports whether p is the path of a scheduling message, a
// member of the scheduling Inbox.
func (b *backend) inScheduleInbox(ctx context.Context, p string) bool {
	inbox, err := b.scheduleInbox(ctx)
	return err == nil && inbox != nil && path.Dir(path.Clean(p)) == path.Clean(inbox.Path)
}

// scheduleInbox returns the scheduling Inbox of the current user, or nil if
// the backend doesn't implement InboxBackend or the user has no Inbox. It
// calls GetInbox once at most: the result is kept for the rest of the request.
func (b *backend) scheduleInbox(ctx context.Context) (*Inbox, error) {
	inboxBackend, ok := b.Backend.(InboxBackend)
	if !ok {
		return nil, nil
	}
	if !b.inboxLoaded {
		b.inbox, b.inboxErr = inboxBackend.GetInbox(ctx)
		b.inboxLoaded = true
	}
	return b.inbox, b.inboxErr
}

func (b *backend) Options(r *http.Request) (caps []string, allow []string, err error) {
	resType := b.resourceTypeAtPath(r.Context(), r.URL.Path)
	if resType == resourceTypeInvalid {
		return nil, nil, internal.HTTPErrorf(http.StatusNotFound, "caldav: resource outside handler prefix")
	}

	caps = []string{"calendar-access"}

	// The capability asserts that the backend performs implicit scheduling,
	// see InboxBackend and RFC 6638 section 2
	if inbox, err := b.scheduleInbox(r.Context()); err != nil {
		return nil, nil, err
	} else if inbox != nil {
		caps = append(caps, "calendar-auto-schedule")
	}

	if resType != resourceTypeCalendarObject {
		allow = []string{http.MethodOptions, "PROPFIND", "REPORT", "DELETE", "MKCOL", "MKCALENDAR"}
		if _, ok := b.Backend.(CalendarUpdater); ok {
			allow = append(allow, "PROPPATCH")
		}
		// Advertise WebDAV-Push for the collections supporting it
		// (draft-bitfire-webdav-push section 2). With push information
		// that can't be served as the draft requires, they don't: the
		// push properties and registrations get the error.
		if info, err := b.pushInfo(r.Context(), r.URL.Path); err != nil && !errors.Is(err, push.ErrInvalidInfo) {
			return nil, nil, err
		} else if info != nil {
			caps = append(caps, string(webdav.CapabilityPush))
			allow = append(allow, http.MethodPost)
		}
		return caps, allow, nil
	}

	var dataReq CalendarCompRequest
	_, err = b.Backend.GetCalendarObject(r.Context(), r.URL.Path, &dataReq)
	if internal.IsNotFound(err) {
		return caps, []string{http.MethodOptions, http.MethodPut}, nil
	} else if err != nil {
		return nil, nil, err
	}

	allow = []string{
		http.MethodOptions,
		http.MethodHead,
		http.MethodGet,
		http.MethodPut,
		http.MethodDelete,
		"PROPFIND",
	}
	if _, ok := b.Backend.(CalendarObjectCopyMover); ok {
		allow = append(allow, "COPY", "MOVE")
	}
	return caps, allow, nil
}

func (b *backend) HeadGet(w http.ResponseWriter, r *http.Request) error {
	var dataReq CalendarCompRequest
	if r.Method != http.MethodHead {
		dataReq.AllProps = true
	}
	co, err := b.Backend.GetCalendarObject(r.Context(), r.URL.Path, &dataReq)
	if err != nil {
		return err
	}

	w.Header().Set("Content-Type", ical.MIMEType)
	if co.ContentLength > 0 {
		w.Header().Set("Content-Length", strconv.FormatInt(co.ContentLength, 10))
	}
	if co.ETag != "" {
		w.Header().Set("ETag", internal.ETag(co.ETag).String())
	}
	if !co.ModTime.IsZero() {
		w.Header().Set("Last-Modified", co.ModTime.UTC().Format(http.TimeFormat))
	}

	if r.Method != http.MethodHead {
		return ical.NewEncoder(w).Encode(co.Data)
	}
	return nil
}

// PropFind writes the responses to a PROPFIND request to mw. The members of a
// collection are listed before the response for the collection is written:
// for a Depth: 0 or 1 request, any error listing resources is served as the
// status of the request (see internal.StreamMultiStatus).
func (b *backend) PropFind(r *http.Request, propfind *internal.PropFind, depth internal.Depth, mw *internal.MultiStatusWriter) error {
	ctx := r.Context()
	resType := b.resourceTypeAtPath(ctx, r.URL.Path)

	var dataReq CalendarCompRequest

	switch resType {
	case resourceTypeInvalid:
		return internal.HTTPErrorf(http.StatusNotFound, "caldav: resource outside handler prefix")
	case resourceTypeRoot:
		resp, err := b.propFindRoot(ctx, r.URL.Path, propfind)
		if err != nil {
			return err
		}
		return mw.WriteResponse(resp)
	case resourceTypeUserPrincipal:
		principalPath, err := b.Backend.CurrentUserPrincipal(ctx)
		if err != nil {
			return err
		}
		if principalPath == "" || !internal.SamePath(r.URL.Path, principalPath) {
			return internal.HTTPErrorf(http.StatusNotFound, "caldav: principal not found")
		}
		resp, err := b.propFindUserPrincipal(ctx, propfind)
		if err != nil {
			return err
		}
		if depth == internal.DepthZero {
			return mw.WriteResponse(resp)
		}
		homeSetResp, err := b.propFindHomeSet(ctx, propfind)
		if err != nil {
			return err
		}
		if depth != internal.DepthInfinity {
			if err := mw.WriteResponse(resp); err != nil {
				return err
			}
			return mw.WriteResponse(homeSetResp)
		}
		homeSetPath, err := b.Backend.CalendarHomeSetPath(ctx)
		if err != nil {
			return err
		}
		return b.propFindAllCalendars(ctx, mw, propfind, homeSetPath, true, resp, homeSetResp)
	case resourceTypeCalendarHomeSet:
		homeSetPath, err := b.Backend.CalendarHomeSetPath(ctx)
		if err != nil {
			return err
		}
		if homeSetPath == "" || !internal.SamePath(r.URL.Path, homeSetPath) {
			return internal.HTTPErrorf(http.StatusNotFound, "caldav: calendar home set not found")
		}
		resp, err := b.propFindHomeSet(ctx, propfind)
		if err != nil {
			return err
		}
		if depth == internal.DepthZero {
			return mw.WriteResponse(resp)
		}
		recurse := depth == internal.DepthInfinity
		return b.propFindAllCalendars(ctx, mw, propfind, homeSetPath, recurse, resp)
	case resourceTypeCalendar:
		cal, err := b.Backend.GetCalendar(ctx, r.URL.Path)
		if err != nil {
			return err
		}
		resp, err := b.propFindCalendar(ctx, propfind, cal)
		if err != nil {
			return err
		}
		if depth == internal.DepthZero {
			return mw.WriteResponse(resp)
		}
		return b.propFindAllCalendarObjects(ctx, mw, propfind, cal.Path, resp)
	case resourceTypeCalendarObject:
		co, err := b.Backend.GetCalendarObject(ctx, r.URL.Path, &dataReq)
		if err != nil {
			return err
		}
		resp, err := b.propFindCalendarObject(ctx, propfind, false, co)
		if err != nil {
			return err
		}
		return mw.WriteResponse(resp)
	case resourceTypeScheduleInbox:
		inbox, err := b.scheduleInbox(ctx)
		if err != nil {
			return err
		}
		if inbox == nil || inbox.Path == "" || !internal.SamePath(r.URL.Path, inbox.Path) {
			return internal.HTTPErrorf(http.StatusNotFound, "caldav: scheduling Inbox not found")
		}
		resp, err := b.propFindInbox(ctx, propfind, inbox)
		if err != nil {
			return err
		}
		if depth == internal.DepthZero {
			return mw.WriteResponse(resp)
		}
		return b.propFindAllCalendarObjects(ctx, mw, propfind, inbox.Path, resp)
	}
	return internal.HTTPErrorf(http.StatusNotFound, "caldav: resource not found")
}

func (b *backend) propFindRoot(ctx context.Context, rootPath string, propfind *internal.PropFind) (*internal.Response, error) {
	principalPath, err := b.Backend.CurrentUserPrincipal(ctx)
	if err != nil {
		return nil, err
	}

	props := map[xml.Name]internal.PropFindFunc{
		internal.CurrentUserPrincipalName: internal.PropFindValue(internal.NewCurrentUserPrincipal(principalPath)),
		internal.ResourceTypeName:         internal.PropFindValue(internal.NewResourceType(internal.CollectionName)),
	}
	return internal.NewPropFindResponse(rootPath, propfind, props)
}

func (b *backend) propFindUserPrincipal(ctx context.Context, propfind *internal.PropFind) (*internal.Response, error) {
	principalPath, err := b.Backend.CurrentUserPrincipal(ctx)
	if err != nil {
		return nil, err
	}
	homeSetPath, err := b.Backend.CalendarHomeSetPath(ctx)
	if err != nil {
		return nil, err
	}

	props := map[xml.Name]internal.PropFindFunc{
		internal.CurrentUserPrincipalName: internal.PropFindValue(internal.NewCurrentUserPrincipal(principalPath)),
		calendarHomeSetName: internal.PropFindValue(&calendarHomeSet{
			Href: &internal.Href{Path: homeSetPath},
		}),
		internal.ResourceTypeName: internal.PropFindValue(internal.NewResourceType(internal.CollectionName, internal.PrincipalName)),
	}
	inbox, err := b.scheduleInbox(ctx)
	if err != nil {
		return nil, err
	}
	if inbox != nil {
		props[scheduleInboxURLName] = internal.PropFindValue(&scheduleInboxURL{
			Href: internal.Href{Path: inbox.Path},
		})
		props[calendarUserAddressSetName] = internal.PropFindValue(&calendarUserAddressSet{
			Addresses: inbox.UserAddressSet,
		})
	}
	internal.AddPrincipalProps(props, principalPath, "")
	// The principal owns itself
	internal.AddACLProps(props, func() (string, error) { return principalPath, nil })
	b.addPushProps(ctx, propfind, principalPath, props)
	return internal.NewPropFindResponse(principalPath, propfind, props)
}

func (b *backend) propFindHomeSet(ctx context.Context, propfind *internal.PropFind) (*internal.Response, error) {
	principalPath, err := b.Backend.CurrentUserPrincipal(ctx)
	if err != nil {
		return nil, err
	}
	homeSetPath, err := b.Backend.CalendarHomeSetPath(ctx)
	if err != nil {
		return nil, err
	}

	// TODO anything else to return here?
	props := map[xml.Name]internal.PropFindFunc{
		internal.CurrentUserPrincipalName: internal.PropFindValue(internal.NewCurrentUserPrincipal(principalPath)),
		internal.ResourceTypeName:         internal.PropFindValue(internal.NewResourceType(internal.CollectionName)),
	}
	internal.AddACLProps(props, func() (string, error) { return principalPath, nil })
	b.addPushProps(ctx, propfind, homeSetPath, props)
	return internal.NewPropFindResponse(homeSetPath, propfind, props)
}

func (b *backend) propFindCalendar(ctx context.Context, propfind *internal.PropFind, cal *Calendar) (*internal.Response, error) {
	props := map[xml.Name]internal.PropFindFunc{
		internal.CurrentUserPrincipalName: func(*internal.RawXMLValue) (interface{}, error) {
			path, err := b.Backend.CurrentUserPrincipal(ctx)
			if err != nil {
				return nil, err
			}
			return internal.NewCurrentUserPrincipal(path), nil
		},
		internal.ResourceTypeName: internal.PropFindValue(internal.NewResourceType(internal.CollectionName, calendarName)),
		supportedCalendarDataName: internal.PropFindValue(&supportedCalendarData{
			Types: []calendarDataType{
				{ContentType: ical.MIMEType, Version: "2.0"},
			},
		}),
		supportedCalendarComponentSetName: func(*internal.RawXMLValue) (interface{}, error) {
			components := []comp{}
			if cal.SupportedComponentSet != nil {
				for _, name := range cal.SupportedComponentSet {
					components = append(components, comp{Name: name})
				}
			} else {
				components = append(components, comp{Name: ical.CompEvent})
			}
			return &supportedCalendarComponentSet{
				Comp: components,
			}, nil
		},
		supportedCollationSetName:            internal.PropFindValue(newSupportedCollationSet()),
		internal.CurrentUserPrivilegeSetName: internal.PropFindValue(internal.NewCurrentUserPrivilegeSet(cal.ReadOnly)),
		internal.SupportedReportSetName:      internal.PropFindValue(b.supportedReportSet()),
	}

	if cal.SyncToken != "" {
		props[internal.SyncTokenName] = internal.PropFindValue(&internal.SyncToken{
			Token: cal.SyncToken,
		})
	}
	if cal.Name != "" {
		props[internal.DisplayNameName] = internal.PropFindValue(&internal.DisplayName{
			Name: cal.Name,
		})
	}
	if cal.Description != "" {
		props[calendarDescriptionName] = internal.PropFindValue(&calendarDescription{
			Description: cal.Description,
		})
	}
	if cal.Color != "" {
		props[calendarColorName] = internal.PropFindValue(&calendarColor{
			Color: cal.Color,
		})
	}
	if cal.Timezone != nil {
		props[calendarTimezoneName] = func(*internal.RawXMLValue) (interface{}, error) {
			var buf bytes.Buffer
			if err := ical.NewEncoder(&buf).Encode(cal.Timezone); err != nil {
				return nil, err
			}
			return &calendarTimezone{
				Timezone: buf.String(),
			}, nil
		}
	}
	if cal.MaxResourceSize > 0 {
		props[maxResourceSizeName] = internal.PropFindValue(&maxResourceSize{
			Size: cal.MaxResourceSize,
		})
	}
	if cal.CTag != "" {
		props[internal.GetCTagName] = internal.PropFindValue(&internal.GetCTag{CTag: cal.CTag})
	}

	// TODO: CALDAV:min-date-time, CALDAV:max-date-time, CALDAV:max-instances, CALDAV:max-attendees-per-instance

	b.addACLProps(ctx, props)
	b.addPushProps(ctx, propfind, cal.Path, props)
	return internal.NewPropFindResponse(cal.Path, propfind, props)
}

func (b *backend) propFindInbox(ctx context.Context, propfind *internal.PropFind, inbox *Inbox) (*internal.Response, error) {
	props := map[xml.Name]internal.PropFindFunc{
		internal.CurrentUserPrincipalName: func(*internal.RawXMLValue) (interface{}, error) {
			path, err := b.Backend.CurrentUserPrincipal(ctx)
			if err != nil {
				return nil, err
			}
			return internal.NewCurrentUserPrincipal(path), nil
		},
		internal.ResourceTypeName:            internal.PropFindValue(internal.NewResourceType(internal.CollectionName, scheduleInboxName)),
		internal.CurrentUserPrivilegeSetName: internal.PropFindValue(newInboxPrivilegeSet(inbox.ReadOnly)),
		// RFC 6638 section 2.3 extends these reports to the Inbox, while
		// sync-collection REPORTs are only served for calendars
		internal.SupportedReportSetName: internal.PropFindValue(internal.NewSupportedReportSet(calendarQueryName, calendarMultigetName)),
		supportedCollationSetName:       internal.PropFindValue(newSupportedCollationSet()),
	}
	b.addACLProps(ctx, props, newScheduleDeliverSupportedPrivilege())
	b.addPushProps(ctx, propfind, inbox.Path, props)
	return internal.NewPropFindResponse(inbox.Path, propfind, props)
}

// newInboxPrivilegeSet returns the DAV:current-user-privilege-set of the
// scheduling Inbox of the current user, who owns it: DAV:read, plus
// DAV:write-content and DAV:unbind unless readOnly is set, so that the
// scheduling messages can be updated and removed once handled. DAV:write
// isn't granted: it aggregates DAV:write-properties and DAV:bind, but the
// Inbox properties can't be modified and only the server delivers messages.
//
// CALDAV:schedule-deliver is an aggregate privilege (RFC 6638 section 6.3),
// and RFC 3744 section 5.4 asks for the privileges an aggregate contains to
// be listed along with it. Each DAV:privilege element holds a single
// privilege.
func newInboxPrivilegeSet(readOnly bool) *internal.CurrentUserPrivilegeSet {
	granted := &struct{}{}
	privs := []internal.Privilege{{Read: granted}}
	if !readOnly {
		privs = append(privs,
			internal.Privilege{WriteContent: granted},
			internal.Privilege{Unbind: granted},
		)
	}
	privs = append(privs,
		internal.Privilege{ScheduleDeliver: granted},
		internal.Privilege{ScheduleDeliverInvite: granted},
		internal.Privilege{ScheduleDeliverReply: granted},
		internal.Privilege{ScheduleQueryFreeBusy: granted},
	)
	return &internal.CurrentUserPrivilegeSet{Privilege: privs}
}

// newScheduleDeliverSupportedPrivilege returns the CALDAV:schedule-deliver
// privilege defined on the scheduling Inbox, which aggregates the other
// scheduling privileges of the Inbox (RFC 6638 sections 6.1 and 6.3). The
// scheduling privileges must be non-abstract and appear in the
// DAV:supported-privilege-set of the Inbox (RFC 6638 section 6).
func newScheduleDeliverSupportedPrivilege() internal.SupportedPrivilege {
	granted := &struct{}{}
	return internal.NewSupportedPrivilege(internal.Privilege{ScheduleDeliver: granted}, "Deliver scheduling messages",
		internal.NewSupportedPrivilege(internal.Privilege{ScheduleDeliverInvite: granted}, "Deliver scheduling messages from organizers"),
		internal.NewSupportedPrivilege(internal.Privilege{ScheduleDeliverReply: granted}, "Deliver scheduling messages from attendees"),
		internal.NewSupportedPrivilege(internal.Privilege{ScheduleQueryFreeBusy: granted}, "Query free-busy information"),
	)
}

// addACLProps adds to props the access control properties of a resource of
// the current user (see internal.AddACLProps), with the privileges others
// defined on it in addition.
func (b *backend) addACLProps(ctx context.Context, props map[xml.Name]internal.PropFindFunc, others ...internal.SupportedPrivilege) {
	internal.AddACLProps(props, func() (string, error) {
		return b.Backend.CurrentUserPrincipal(ctx)
	}, others...)
}

// propFindAllCalendars writes resps, which end with the response for the
// calendar home set at homeSetPath, followed by the responses for the members
// of the home set: the calendars, and the scheduling Inbox if it's one of
// them, along with their own members if recurse is set. The members are
// listed before resps are written.
func (b *backend) propFindAllCalendars(ctx context.Context, mw *internal.MultiStatusWriter, propfind *internal.PropFind, homeSetPath string, recurse bool, resps ...*internal.Response) error {
	cals, err := b.Backend.ListCalendars(ctx)
	errResps, err := internal.HrefErrorResponses(err)
	if err != nil {
		return err
	}
	inbox, err := b.scheduleInbox(ctx)
	if err != nil {
		return err
	}

	for _, resp := range resps {
		if err := mw.WriteResponse(resp); err != nil {
			return err
		}
	}
	writeCollection := func(resp *internal.Response, collPath string) error {
		if recurse {
			return b.propFindAllCalendarObjects(ctx, mw, propfind, collPath, resp)
		}
		return mw.WriteResponse(resp)
	}
	for i := range cals {
		resp, err := b.propFindCalendar(ctx, propfind, &cals[i])
		if err != nil {
			return err
		}
		if err := writeCollection(resp, cals[i].Path); err != nil {
			return err
		}
	}
	// A Depth: 1 PROPFIND only returns the internal members of a collection
	// (RFC 4918 section 9.1)
	if inbox != nil && path.Dir(path.Clean(inbox.Path)) == path.Clean(homeSetPath) {
		resp, err := b.propFindInbox(ctx, propfind, inbox)
		if err != nil {
			return err
		}
		if err := writeCollection(resp, inbox.Path); err != nil {
			return err
		}
	}
	return mw.WriteResponses(errResps)
}

func (b *backend) propFindCalendarObject(ctx context.Context, propfind *internal.PropFind, report bool, co *CalendarObject) (*internal.Response, error) {
	// Unlike a PROPFIND request, a calendar-query or calendar-multiget
	// REPORT doesn't have to ask for properties (RFC 4791 sections 9.5 and
	// 9.10). Each object found still gets a response, with only a status
	// (RFC 4918 section 14.24).
	if report && !propfind.AsksForProps() {
		return internal.NewOKResponse(co.Path), nil
	}

	props := map[xml.Name]internal.PropFindFunc{
		internal.CurrentUserPrincipalName: func(*internal.RawXMLValue) (interface{}, error) {
			path, err := b.Backend.CurrentUserPrincipal(ctx)
			if err != nil {
				return nil, err
			}
			return internal.NewCurrentUserPrincipal(path), nil
		},
		internal.CurrentUserPrivilegeSetName: internal.PropFindValue(internal.NewCurrentUserPrivilegeSet(co.ReadOnly)),
		internal.GetContentTypeName: internal.PropFindValue(&internal.GetContentType{
			Type: ical.MIMEType,
		}),
	}

	// calendar-data is not a property: it is only returned by REPORT
	// requests, not by PROPFIND (RFC 4791 section 9.6)
	if report {
		props[calendarDataName] = func(*internal.RawXMLValue) (interface{}, error) {
			var buf bytes.Buffer
			if err := ical.NewEncoder(&buf).Encode(co.Data); err != nil {
				return nil, err
			}

			return &calendarDataResp{Data: buf.Bytes()}, nil
		}
	}

	if co.ContentLength > 0 {
		props[internal.GetContentLengthName] = internal.PropFindValue(&internal.GetContentLength{
			Length: co.ContentLength,
		})
	}
	if !co.ModTime.IsZero() {
		props[internal.GetLastModifiedName] = internal.PropFindValue(&internal.GetLastModified{
			LastModified: internal.Time(co.ModTime),
		})
	}

	if co.ETag != "" {
		props[internal.GetETagName] = internal.PropFindValue(&internal.GetETag{
			ETag: internal.ETag(co.ETag),
		})
	}

	b.addACLProps(ctx, props)
	return internal.NewPropFindResponse(co.Path, propfind, props)
}

// propFindAllCalendarObjects writes resp, the response for the collection at
// path, followed by the responses for its members, the calendar objects. The
// members are listed before resp is written.
func (b *backend) propFindAllCalendarObjects(ctx context.Context, mw *internal.MultiStatusWriter, propfind *internal.PropFind, path string, resp *internal.Response) error {
	var dataReq CalendarCompRequest
	cos, err := b.Backend.ListCalendarObjects(ctx, path, &dataReq)
	errResps, err := internal.HrefErrorResponses(err)
	if err != nil {
		return err
	}

	if err := mw.WriteResponse(resp); err != nil {
		return err
	}
	for i := range cos {
		resp, err := b.propFindCalendarObject(ctx, propfind, false, &cos[i])
		if err != nil {
			return err
		}
		if err := mw.WriteResponse(resp); err != nil {
			return err
		}
	}
	return mw.WriteResponses(errResps)
}

func (b *backend) PropPatch(r *http.Request, update *internal.PropertyUpdate) (*internal.Response, error) {
	// Make sure the resource exists before reporting on its properties
	switch b.resourceTypeAtPath(r.Context(), r.URL.Path) {
	case resourceTypeCalendar:
		if _, err := b.Backend.GetCalendar(r.Context(), r.URL.Path); err != nil {
			return nil, err
		}
		if updater, ok := b.Backend.(CalendarUpdater); ok {
			return b.propPatchCalendar(r, updater, update)
		}
	case resourceTypeCalendarObject:
		var dataReq CalendarCompRequest
		if _, err := b.Backend.GetCalendarObject(r.Context(), r.URL.Path, &dataReq); err != nil {
			return nil, err
		}
	}

	// Properties cannot be written yet, and those of the scheduling Inbox
	// won't be. Refusing them one by one in a multistatus, rather than
	// failing the whole request, keeps clients which PROPPATCH display
	// metadata while adopting a calendar (macOS Calendar sets displayname,
	// calendar-color and calendar-order) from dropping it.
	return internal.NewPropPatchResponse(r.URL.Path, update, http.StatusForbidden)
}

func (b *backend) propPatchCalendar(r *http.Request, updater CalendarUpdater, update *internal.PropertyUpdate) (*internal.Response, error) {
	var calUpdate CalendarUpdate
	patch := func(raw *internal.RawXMLValue, remove bool) error {
		name, _ := raw.XMLName()
		switch name {
		case internal.DisplayNameName:
			var v internal.DisplayName
			if !remove {
				if err := raw.Decode(&v); err != nil {
					return &internal.HTTPError{Code: http.StatusConflict, Err: err}
				}
			}
			calUpdate.Name = &v.Name
		case calendarDescriptionName:
			var v calendarDescription
			if !remove {
				if err := raw.Decode(&v); err != nil {
					return &internal.HTTPError{Code: http.StatusConflict, Err: err}
				}
			}
			calUpdate.Description = &v.Description
		case calendarColorName:
			var color string
			if !remove {
				var v calendarColor
				if err := raw.Decode(&v); err != nil {
					return &internal.HTTPError{Code: http.StatusConflict, Err: err}
				}
				color = strings.TrimSpace(v.Color)
			}
			calUpdate.Color = &color
		case calendarTimezoneName:
			if remove {
				calUpdate.Timezone = nil
				calUpdate.RemoveTimezone = true
				break
			}
			var v calendarTimezone
			if err := raw.Decode(&v); err != nil {
				return &internal.HTTPError{Code: http.StatusConflict, Err: err}
			}
			tz, err := decodeCalendarTimezone(strings.TrimSpace(v.Timezone))
			if err != nil {
				return err
			}
			calUpdate.Timezone = tz
			calUpdate.RemoveTimezone = false
		default:
			return internal.HTTPErrorf(http.StatusForbidden, "caldav: property cannot be modified")
		}
		return nil
	}
	commit := func() error {
		return updater.UpdateCalendar(r.Context(), r.URL.Path, &calUpdate)
	}
	return internal.PatchProps(r.URL.Path, update, patch, commit)
}

func (b *backend) Put(w http.ResponseWriter, r *http.Request) error {
	ifNoneMatch := webdav.ConditionalMatch(r.Header.Get("If-None-Match"))
	ifMatch := webdav.ConditionalMatch(r.Header.Get("If-Match"))

	opts := PutCalendarObjectOptions{
		IfNoneMatch: ifNoneMatch,
		IfMatch:     ifMatch,
	}

	t, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil {
		return internal.HTTPErrorf(http.StatusBadRequest, "caldav: malformed Content-Type: %v", err)
	}
	if t != ical.MIMEType {
		// TODO: send CALDAV:supported-calendar-data error
		return internal.HTTPErrorf(http.StatusBadRequest, "caldav: unsupported Content-Type %q", t)
	}

	// TODO: check CALDAV:max-resource-size precondition
	cal, err := ical.NewDecoder(r.Body).Decode()
	if err != nil {
		// TODO: send CALDAV:valid-calendar-data error
		return internal.HTTPErrorf(http.StatusBadRequest, "caldav: failed to parse iCalendar: %v", err)
	}

	if isTimezoneOnly(cal) {
		if err := b.checkTimezoneOnly(r.Context(), r.URL.Path); err != nil {
			return err
		}
	}

	co, err := b.Backend.PutCalendarObject(r.Context(), r.URL.Path, cal, &opts)
	if err != nil {
		return err
	}

	if co.ETag != "" {
		w.Header().Set("ETag", internal.ETag(co.ETag).String())
	}
	if !co.ModTime.IsZero() {
		w.Header().Set("Last-Modified", co.ModTime.UTC().Format(http.TimeFormat))
	}
	if co.Path != "" {
		w.Header().Set("Location", co.Path)
	}

	// TODO: http.StatusNoContent if the resource already existed
	w.WriteHeader(http.StatusCreated)

	return nil
}

// isTimezoneOnly reports whether cal holds VTIMEZONE components only.
func isTimezoneOnly(cal *ical.Calendar) bool {
	for _, comp := range cal.Children {
		if comp.Name != ical.CompTimezone {
			return false
		}
	}
	return len(cal.Children) > 0
}

// checkTimezoneOnly checks that the calendar of the calendar object at p can
// hold a calendar object with only VTIMEZONE components. RFC 4791 section
// 5.2.3 only lets calendars listing VTIMEZONE in their
// supported-calendar-component-set hold them. The server always reports this
// property, with VEVENT only if Calendar.SupportedComponentSet is nil.
//
// The other component types are left to the backend. These objects aren't:
// ValidateCalendarObject used to refuse them, so backends could rely on it not
// to store them.
func (b *backend) checkTimezoneOnly(ctx context.Context, p string) error {
	// Leave the members of the scheduling Inbox, which are scheduling
	// messages, and the paths which aren't calendar objects to the backend
	if b.resourceTypeAtPath(ctx, p) != resourceTypeCalendarObject || b.inScheduleInbox(ctx, p) {
		return nil
	}
	// With a trailing slash, the form RFC 4918 section 5.2 asks clients to
	// use for collections
	cal, err := b.Backend.GetCalendar(ctx, path.Dir(path.Clean(p))+"/")
	if err != nil {
		return err
	}
	for _, name := range cal.SupportedComponentSet {
		if strings.EqualFold(name, ical.CompTimezone) {
			return nil
		}
	}
	return newPreconditionError(http.StatusForbidden, PreconditionSupportedCalendarComponent)
}

func (b *backend) Delete(r *http.Request) error {
	// Only calendars and calendar objects (including scheduling messages in the
	// Inbox, RFC 6638 section 2; clients can't delete the Inbox itself) can be
	// deleted.
	switch b.resourceTypeAtPath(r.Context(), r.URL.Path) {
	case resourceTypeCalendar, resourceTypeCalendarObject:
		return b.Backend.DeleteCalendarObject(r.Context(), r.URL.Path)
	}
	return internal.HTTPErrorf(http.StatusForbidden, "caldav: cannot delete resource at given location")
}

func (b *backend) Mkcol(r *http.Request) error {
	if b.resourceTypeAtPath(r.Context(), r.URL.Path) != resourceTypeCalendar {
		return internal.HTTPErrorf(http.StatusForbidden, "caldav: calendar creation not allowed at given location")
	}

	cal := Calendar{
		Path: r.URL.Path,
	}

	if !internal.IsRequestBodyEmpty(r) {
		var m mkcolReq
		if err := internal.DecodeXMLRequest(r, &m); err != nil {
			return internal.HTTPErrorf(http.StatusBadRequest, "caldav: error parsing mkcol request: %s", err.Error())
		}

		prop := m.Set.Prop
		if !prop.ResourceType.Is(internal.CollectionName) || !prop.ResourceType.Is(calendarName) {
			return internal.HTTPErrorf(http.StatusBadRequest, "caldav: unexpected resource type")
		}
		if err := setCalendarProps(&cal, &prop); err != nil {
			return err
		}
	}

	return b.Backend.CreateCalendar(r.Context(), &cal)
}

// setCalendarProps sets the fields of cal from the properties of an extended
// MKCOL or a MKCALENDAR request. The properties it doesn't support are
// ignored: clients set many which are only meaningful to them, such as
// calendar-order, and refusing the request would leave them unable to create
// calendars.
func setCalendarProps(cal *Calendar, prop *mkcolProp) error {
	cal.Name = prop.DisplayName
	cal.Description = prop.CalendarDescription
	cal.Color = strings.TrimSpace(prop.CalendarColor)

	if s := strings.TrimSpace(prop.CalendarTimezone); s != "" {
		tz, err := decodeCalendarTimezone(s)
		if err != nil {
			return err
		}
		cal.Timezone = tz
	}

	// Leave the set nil when the request doesn't set it, so the default
	// applies
	for _, v := range prop.SupportedCalendarComponentSet.Comp {
		cal.SupportedComponentSet = append(cal.SupportedComponentSet, v.Name)
	}
	return nil
}

func decodeCalendarTimezone(s string) (*ical.Calendar, error) {
	cal, err := ical.NewDecoder(strings.NewReader(s)).Decode()
	if err != nil {
		return nil, NewPreconditionError(PreconditionValidCalendarData)
	}
	if len(cal.Children) != 1 || cal.Children[0].Name != ical.CompTimezone {
		return nil, NewPreconditionError(PreconditionValidCalendarData)
	}
	// The decoder doesn't validate, but the encoder does: reject anything we
	// wouldn't be able to return in PROPFIND responses
	if err := ical.NewEncoder(new(bytes.Buffer)).Encode(cal); err != nil {
		return nil, NewPreconditionError(PreconditionValidCalendarData)
	}
	return cal, nil
}

// inPrefix reports whether p is under the prefix of the Handler.
func (b *backend) inPrefix(p string) bool {
	p = path.Clean(p)
	return b.Prefix == "" || p == b.Prefix || strings.HasPrefix(p, b.Prefix+"/")
}

// checkCopyMove checks that a COPY or MOVE request is for a calendar object,
// to be copied or moved into a calendar. The scheduling messages of the Inbox
// aren't: the server delivers them, and they are iTIP messages, which
// calendars can't hold (RFC 4791 section 4.1).
func (b *backend) checkCopyMove(ctx context.Context, src, dst string) error {
	if b.resourceTypeAtPath(ctx, src) != resourceTypeCalendarObject || b.inScheduleInbox(ctx, src) {
		return internal.HTTPErrorf(http.StatusForbidden, "caldav: only calendar objects can be copied or moved")
	}
	if !b.inPrefix(dst) {
		// RFC 4918 sections 9.8.5 and 9.9.4
		return internal.HTTPErrorf(http.StatusBadGateway, "caldav: destination is outside of the CalDAV namespace")
	}
	if b.resourceTypeAtPath(ctx, dst) != resourceTypeCalendarObject || b.inScheduleInbox(ctx, dst) {
		// Calendar objects are stored in calendars (RFC 4791 section 4.2).
		// Like MKCALENDAR, report a wrong location with
		// calendar-collection-location-ok, the precondition RFC 4791
		// section 5.3.2.1 defines for COPY and MOVE of calendars.
		return newPreconditionError(http.StatusForbidden, PreconditionCalendarCollectionLocationOk)
	}
	return nil
}

func (b *backend) Copy(r *http.Request, dest *internal.Href, recursive, overwrite bool) (created bool, err error) {
	cm, ok := b.Backend.(CalendarObjectCopyMover)
	if !ok {
		return false, internal.HTTPErrorf(http.StatusNotImplemented, "caldav: Copy not implemented")
	}
	if err := b.checkCopyMove(r.Context(), r.URL.Path, dest.Path); err != nil {
		return false, err
	}
	// recursive doesn't matter: calendar objects have no members
	opts := CopyCalendarObjectOptions{NoOverwrite: !overwrite}
	return cm.CopyCalendarObject(r.Context(), r.URL.Path, dest.Path, &opts)
}

func (b *backend) Move(r *http.Request, dest *internal.Href, overwrite bool) (created bool, err error) {
	cm, ok := b.Backend.(CalendarObjectCopyMover)
	if !ok {
		return false, internal.HTTPErrorf(http.StatusNotImplemented, "caldav: Move not implemented")
	}
	if err := b.checkCopyMove(r.Context(), r.URL.Path, dest.Path); err != nil {
		return false, err
	}
	opts := MoveCalendarObjectOptions{NoOverwrite: !overwrite}
	return cm.MoveCalendarObject(r.Context(), r.URL.Path, dest.Path, &opts)
}

// https://datatracker.ietf.org/doc/html/rfc4791#section-5.3.2.1
type PreconditionType string

const (
	PreconditionNoUIDConflict                PreconditionType = "no-uid-conflict"
	PreconditionSupportedCalendarData        PreconditionType = "supported-calendar-data"
	PreconditionSupportedCalendarComponent   PreconditionType = "supported-calendar-component"
	PreconditionValidCalendarData            PreconditionType = "valid-calendar-data"
	PreconditionValidCalendarObjectResource  PreconditionType = "valid-calendar-object-resource"
	PreconditionCalendarCollectionLocationOk PreconditionType = "calendar-collection-location-ok"
	PreconditionMaxResourceSize              PreconditionType = "max-resource-size"
	PreconditionMinDateTime                  PreconditionType = "min-date-time"
	PreconditionMaxDateTime                  PreconditionType = "max-date-time"
	PreconditionMaxInstances                 PreconditionType = "max-instances"
	PreconditionMaxAttendeesPerInstance      PreconditionType = "max-attendees-per-instance"
	PreconditionSupportedCollation           PreconditionType = "supported-collation"

	// PreconditionValidFilter is the precondition of the calendar-query
	// REPORT that its filter is valid (RFC 4791 section 7.8). The server
	// reports a malformed filter with 403 Forbidden: repeating the request
	// can't help (section 1.3).
	PreconditionValidFilter PreconditionType = "valid-filter"
)

func NewPreconditionError(err PreconditionType) error {
	return newPreconditionError(http.StatusConflict, err)
}

func newPreconditionError(code int, err PreconditionType) error {
	name := xml.Name{Space: "urn:ietf:params:xml:ns:caldav", Local: string(err)}
	elem := internal.NewRawXMLElement(name, nil, nil)
	return &internal.HTTPError{
		Code: code,
		Err: &internal.Error{
			Raw: []internal.RawXMLValue{*elem},
		},
	}
}
