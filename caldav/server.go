package caldav

import (
	"bytes"
	"context"
	"encoding/xml"
	"fmt"
	"mime"
	"net/http"
	"path"
	"strconv"
	"strings"

	"github.com/emersion/go-ical"
	"github.com/emersion/go-webdav"
	"github.com/emersion/go-webdav/internal"
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
type Handler struct {
	Backend Backend
	Prefix  string
}

// ServeHTTP implements http.Handler.
func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if h.Backend == nil {
		http.Error(w, "caldav: no backend available", http.StatusInternalServerError)
		return
	}

	if r.URL.Path == "/.well-known/caldav" {
		principalPath, err := h.Backend.CurrentUserPrincipal(r.Context())
		if err != nil {
			http.Error(w, "caldav: failed to determine current user principal", http.StatusInternalServerError)
			return
		}

		http.Redirect(w, r, principalPath, http.StatusPermanentRedirect)
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
		b := backend{
			Backend: h.Backend,
			Prefix:  strings.TrimSuffix(h.Prefix, "/"),
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
	b := backend{
		Backend: h.Backend,
		Prefix:  strings.TrimSuffix(h.Prefix, "/"),
	}
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
		return h.handleMultiget(r.Context(), w, report.Multiget)
	} else if report.SyncCollection != nil {
		return h.handleSyncCollection(r, w, report.SyncCollection)
	}
	return internal.HTTPErrorf(http.StatusBadRequest, "caldav: expected calendar-query, calendar-multiget or sync-collection element in REPORT request")
}

func decodeTextMatch(el *textMatch) *TextMatch {
	// TODO: handle the collation attribute
	return &TextMatch{
		Text:            el.Text,
		NegateCondition: bool(el.NegateCondition),
	}
}

func decodeParamFilter(el *paramFilter) (*ParamFilter, error) {
	pf := &ParamFilter{Name: el.Name}
	if el.IsNotDefined != nil {
		if el.TextMatch != nil {
			return nil, fmt.Errorf("caldav: failed to parse param-filter: if is-not-defined is provided, text-match can't be provided")
		}
		pf.IsNotDefined = true
	}
	if el.TextMatch != nil {
		pf.TextMatch = decodeTextMatch(el.TextMatch)
	}
	return pf, nil
}

func decodePropFilter(el *propFilter) (*PropFilter, error) {
	pf := &PropFilter{Name: el.Name}
	if el.IsNotDefined != nil {
		if el.TextMatch != nil || el.TimeRange != nil || len(el.ParamFilter) > 0 {
			return nil, fmt.Errorf("caldav: failed to parse prop-filter: if is-not-defined is provided, text-match, time-range, or param-filter can't be provided")
		}
		pf.IsNotDefined = true
	}
	if el.TextMatch != nil {
		pf.TextMatch = decodeTextMatch(el.TextMatch)
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
		if el.TimeRange != nil || len(el.PropFilters) > 0 || len(el.CompFilters) > 0 {
			return nil, fmt.Errorf("caldav: failed to parse comp-filter: if is-not-defined is provided, time-range, prop-filter, or comp-filter can't be provided")
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

func decodeCalendarDataReq(calendarData *calendarDataReq) (*CalendarCompRequest, error) {
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

	if calendarData.Expand != nil {
		req.Expand = calendarData.Expand.request()
	}
	// TODO: limit-recurrence-set, limit-freebusy-set

	return req, nil
}

// decodeCalendarDataProp decodes the CALDAV:calendar-data element of the
// DAV:prop element of a REPORT request.
func decodeCalendarDataProp(prop *internal.Prop) (*CalendarCompRequest, error) {
	var calendarData calendarDataReq
	if err := prop.Decode(&calendarData); internal.IsNotFound(err) {
		return &CalendarCompRequest{}, nil
	} else if err != nil {
		return nil, err
	}
	return decodeCalendarDataReq(&calendarData)
}

func (h *Handler) handleQuery(r *http.Request, w http.ResponseWriter, query *calendarQuery) error {
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

	resps := make([]internal.Response, 0, len(cos)+len(errResps))
	for _, co := range cos {
		b := backend{
			Backend: h.Backend,
			Prefix:  strings.TrimSuffix(h.Prefix, "/"),
		}
		propfind := internal.PropFind{
			Prop:     query.Prop,
			AllProp:  query.AllProp,
			PropName: query.PropName,
		}
		resp, err := b.propFindCalendarObject(r.Context(), &propfind, true, &co)
		if err != nil {
			return err
		}
		resps = append(resps, *resp)
	}
	resps = append(resps, errResps...)

	ms := internal.NewMultiStatus(resps...)

	return internal.ServeMultiStatus(w, ms)
}

// CalendarObjectsBackend is an optional interface a Backend can implement to
// fetch the calendar objects of a calendar-multiget REPORT in a single call,
// rather than calling GetCalendarObject for each of them.
type CalendarObjectsBackend interface {
	// GetCalendarObjects returns the calendar objects found at paths, in any order.
	// The Path of each must be the path it was requested by: paths without
	// an object are reported as 404 Not Found. An error fails the whole
	// request.
	GetCalendarObjects(ctx context.Context, paths []string, req *CalendarCompRequest) ([]CalendarObject, error)
}

func (h *Handler) handleMultiget(ctx context.Context, w http.ResponseWriter, multiget *calendarMultiget) error {
	var dataReq CalendarCompRequest
	if multiget.Prop != nil {
		decoded, err := decodeCalendarDataProp(multiget.Prop)
		if err != nil {
			return err
		}
		dataReq = *decoded
	}

	var objects map[string]*CalendarObject
	if mb, ok := h.Backend.(CalendarObjectsBackend); ok {
		paths := make([]string, len(multiget.Hrefs))
		for i, href := range multiget.Hrefs {
			paths[i] = href.Path
		}
		l, err := mb.GetCalendarObjects(ctx, paths, &dataReq)
		if err != nil {
			return err
		}
		objects = make(map[string]*CalendarObject, len(l))
		for i := range l {
			objects[l[i].Path] = &l[i]
		}
	}

	var resps []internal.Response
	for _, href := range multiget.Hrefs {
		var co *CalendarObject
		var err error
		if objects != nil {
			if co = objects[href.Path]; co == nil {
				err = internal.HTTPErrorf(http.StatusNotFound, "caldav: calendar object not found")
			}
		} else {
			co, err = h.Backend.GetCalendarObject(ctx, href.Path, &dataReq)
		}
		if err != nil {
			resp := internal.NewErrorResponse(href.Path, err)
			resps = append(resps, *resp)
			continue
		}

		b := backend{
			Backend: h.Backend,
			Prefix:  strings.TrimSuffix(h.Prefix, "/"),
		}
		propfind := internal.PropFind{
			Prop:     multiget.Prop,
			AllProp:  multiget.AllProp,
			PropName: multiget.PropName,
		}
		resp, err := b.propFindCalendarObject(ctx, &propfind, true, co)
		if err != nil {
			return err
		}
		resps = append(resps, *resp)
	}

	ms := internal.NewMultiStatus(resps...)
	return internal.ServeMultiStatus(w, ms)
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

func (b *backend) resourceTypeAtPath(ctx context.Context, reqPath string) resourceType {
	reqPath = path.Clean(reqPath)
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
	caps = []string{"calendar-access"}

	// The capability asserts that the backend performs implicit scheduling,
	// see InboxBackend and RFC 6638 section 2
	if inbox, err := b.scheduleInbox(r.Context()); err != nil {
		return nil, nil, err
	} else if inbox != nil {
		caps = append(caps, "calendar-auto-schedule")
	}

	if b.resourceTypeAtPath(r.Context(), r.URL.Path) != resourceTypeCalendarObject {
		return caps, []string{http.MethodOptions, "PROPFIND", "REPORT", "DELETE", "MKCOL", "MKCALENDAR"}, nil
	}

	var dataReq CalendarCompRequest
	_, err = b.Backend.GetCalendarObject(r.Context(), r.URL.Path, &dataReq)
	if httpErr, ok := err.(*internal.HTTPError); ok && httpErr.Code == http.StatusNotFound {
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

func (b *backend) PropFind(r *http.Request, propfind *internal.PropFind, depth internal.Depth) (*internal.MultiStatus, error) {
	resType := b.resourceTypeAtPath(r.Context(), r.URL.Path)

	var dataReq CalendarCompRequest
	var resps []internal.Response

	switch resType {
	case resourceTypeRoot:
		resp, err := b.propFindRoot(r.Context(), propfind)
		if err != nil {
			return nil, err
		}
		resps = append(resps, *resp)
	case resourceTypeUserPrincipal:
		principalPath, err := b.Backend.CurrentUserPrincipal(r.Context())
		if err != nil {
			return nil, err
		}
		if r.URL.Path == principalPath {
			resp, err := b.propFindUserPrincipal(r.Context(), propfind)
			if err != nil {
				return nil, err
			}
			resps = append(resps, *resp)
			if depth != internal.DepthZero {
				resp, err := b.propFindHomeSet(r.Context(), propfind)
				if err != nil {
					return nil, err
				}
				resps = append(resps, *resp)
				if depth == internal.DepthInfinity {
					homeSetPath, err := b.Backend.CalendarHomeSetPath(r.Context())
					if err != nil {
						return nil, err
					}
					resps_, err := b.propFindAllCalendars(r.Context(), propfind, homeSetPath, true)
					if err != nil {
						return nil, err
					}
					resps = append(resps, resps_...)
				}
			}
		}
	case resourceTypeCalendarHomeSet:
		homeSetPath, err := b.Backend.CalendarHomeSetPath(r.Context())
		if err != nil {
			return nil, err
		}
		if r.URL.Path == homeSetPath {
			resp, err := b.propFindHomeSet(r.Context(), propfind)
			if err != nil {
				return nil, err
			}
			resps = append(resps, *resp)
			if depth != internal.DepthZero {
				recurse := depth == internal.DepthInfinity
				resps_, err := b.propFindAllCalendars(r.Context(), propfind, homeSetPath, recurse)
				if err != nil {
					return nil, err
				}
				resps = append(resps, resps_...)
			}
		}
	case resourceTypeCalendar:
		ab, err := b.Backend.GetCalendar(r.Context(), r.URL.Path)
		if err != nil {
			return nil, err
		}
		resp, err := b.propFindCalendar(r.Context(), propfind, ab)
		if err != nil {
			return nil, err
		}
		resps = append(resps, *resp)
		if depth != internal.DepthZero {
			resps_, err := b.propFindAllCalendarObjects(r.Context(), propfind, ab.Path)
			if err != nil {
				return nil, err
			}
			resps = append(resps, resps_...)
		}
	case resourceTypeCalendarObject:
		ao, err := b.Backend.GetCalendarObject(r.Context(), r.URL.Path, &dataReq)
		if err != nil {
			return nil, err
		}

		resp, err := b.propFindCalendarObject(r.Context(), propfind, false, ao)
		if err != nil {
			return nil, err
		}
		resps = append(resps, *resp)
	case resourceTypeScheduleInbox:
		inbox, err := b.scheduleInbox(r.Context())
		if err != nil {
			return nil, err
		}
		if inbox != nil {
			resp, err := b.propFindInbox(propfind, inbox)
			if err != nil {
				return nil, err
			}
			resps = append(resps, *resp)
			if depth != internal.DepthZero {
				resps_, err := b.propFindAllCalendarObjects(r.Context(), propfind, inbox.Path)
				if err != nil {
					return nil, err
				}
				resps = append(resps, resps_...)
			}
		}
	}

	return internal.NewMultiStatus(resps...), nil
}

func (b *backend) propFindRoot(ctx context.Context, propfind *internal.PropFind) (*internal.Response, error) {
	principalPath, err := b.Backend.CurrentUserPrincipal(ctx)
	if err != nil {
		return nil, err
	}

	props := map[xml.Name]internal.PropFindFunc{
		internal.CurrentUserPrincipalName: internal.PropFindValue(&internal.CurrentUserPrincipal{
			Href: internal.Href{Path: principalPath},
		}),
		internal.ResourceTypeName: internal.PropFindValue(internal.NewResourceType(internal.CollectionName)),
	}
	return internal.NewPropFindResponse(principalPath, propfind, props)
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
		internal.CurrentUserPrincipalName: internal.PropFindValue(&internal.CurrentUserPrincipal{
			Href: internal.Href{Path: principalPath},
		}),
		calendarHomeSetName: internal.PropFindValue(&calendarHomeSet{
			Href: internal.Href{Path: homeSetPath},
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
		internal.CurrentUserPrincipalName: internal.PropFindValue(&internal.CurrentUserPrincipal{
			Href: internal.Href{Path: principalPath},
		}),
		internal.ResourceTypeName: internal.PropFindValue(internal.NewResourceType(internal.CollectionName)),
	}
	return internal.NewPropFindResponse(homeSetPath, propfind, props)
}

func (b *backend) propFindCalendar(ctx context.Context, propfind *internal.PropFind, cal *Calendar) (*internal.Response, error) {
	props := map[xml.Name]internal.PropFindFunc{
		internal.CurrentUserPrincipalName: func(*internal.RawXMLValue) (interface{}, error) {
			path, err := b.Backend.CurrentUserPrincipal(ctx)
			if err != nil {
				return nil, err
			}
			return &internal.CurrentUserPrincipal{Href: internal.Href{Path: path}}, nil
		},
		internal.ResourceTypeName: internal.PropFindValue(internal.NewResourceType(internal.CollectionName, calendarName)),
		calendarDescriptionName: internal.PropFindValue(&calendarDescription{
			Description: cal.Description,
		}),
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

	return internal.NewPropFindResponse(cal.Path, propfind, props)
}

func (b *backend) propFindInbox(propfind *internal.PropFind, inbox *Inbox) (*internal.Response, error) {
	props := map[xml.Name]internal.PropFindFunc{
		internal.ResourceTypeName:            internal.PropFindValue(internal.NewResourceType(internal.CollectionName, scheduleInboxName)),
		internal.CurrentUserPrivilegeSetName: internal.PropFindValue(newInboxPrivilegeSet(inbox.ReadOnly)),
		// RFC 6638 section 2.3 extends these reports to the Inbox, while
		// sync-collection REPORTs are only served for calendars
		internal.SupportedReportSetName: internal.PropFindValue(internal.NewSupportedReportSet(calendarQueryName, calendarMultigetName)),
	}
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

// propFindAllCalendars returns the responses for the members of the calendar
// home set: the calendars, and the scheduling Inbox if it's one of them.
func (b *backend) propFindAllCalendars(ctx context.Context, propfind *internal.PropFind, homeSetPath string, recurse bool) ([]internal.Response, error) {
	abs, err := b.Backend.ListCalendars(ctx)
	errResps, err := internal.HrefErrorResponses(err)
	if err != nil {
		return nil, err
	}

	var resps []internal.Response
	for _, ab := range abs {
		resp, err := b.propFindCalendar(ctx, propfind, &ab)
		if err != nil {
			return nil, err
		}
		resps = append(resps, *resp)
		if recurse {
			resps_, err := b.propFindAllCalendarObjects(ctx, propfind, ab.Path)
			if err != nil {
				return nil, err
			}
			resps = append(resps, resps_...)
		}
	}
	inbox, err := b.scheduleInbox(ctx)
	if err != nil {
		return nil, err
	}
	// A Depth: 1 PROPFIND only returns the internal members of a collection
	// (RFC 4918 section 9.1)
	if inbox != nil && path.Dir(path.Clean(inbox.Path)) == path.Clean(homeSetPath) {
		resp, err := b.propFindInbox(propfind, inbox)
		if err != nil {
			return nil, err
		}
		resps = append(resps, *resp)
		if recurse {
			resps_, err := b.propFindAllCalendarObjects(ctx, propfind, inbox.Path)
			if err != nil {
				return nil, err
			}
			resps = append(resps, resps_...)
		}
	}
	return append(resps, errResps...), nil
}

func (b *backend) propFindCalendarObject(ctx context.Context, propfind *internal.PropFind, report bool, co *CalendarObject) (*internal.Response, error) {
	props := map[xml.Name]internal.PropFindFunc{
		internal.CurrentUserPrincipalName: func(*internal.RawXMLValue) (interface{}, error) {
			path, err := b.Backend.CurrentUserPrincipal(ctx)
			if err != nil {
				return nil, err
			}
			return &internal.CurrentUserPrincipal{Href: internal.Href{Path: path}}, nil
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

	return internal.NewPropFindResponse(co.Path, propfind, props)
}

func (b *backend) propFindAllCalendarObjects(ctx context.Context, propfind *internal.PropFind, path string) ([]internal.Response, error) {
	var dataReq CalendarCompRequest
	aos, err := b.Backend.ListCalendarObjects(ctx, path, &dataReq)
	errResps, err := internal.HrefErrorResponses(err)
	if err != nil {
		return nil, err
	}

	resps := make([]internal.Response, 0, len(aos)+len(errResps))
	for _, ao := range aos {
		resp, err := b.propFindCalendarObject(ctx, propfind, false, &ao)
		if err != nil {
			return nil, err
		}
		resps = append(resps, *resp)
	}
	return append(resps, errResps...), nil
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
		default:
			// TODO: calendar-color and calendar-timezone
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

func (b *backend) Delete(r *http.Request) error {
	// Clients can't delete the Inbox, only the scheduling messages in it (RFC
	// 6638 section 2)
	if b.resourceTypeAtPath(r.Context(), r.URL.Path) == resourceTypeScheduleInbox {
		return internal.HTTPErrorf(http.StatusForbidden, "caldav: the scheduling Inbox can't be deleted")
	}
	return b.Backend.DeleteCalendarObject(r.Context(), r.URL.Path)
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
