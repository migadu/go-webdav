package caldav

import (
	"context"
	"encoding/xml"
	"net/http"
	"net/http/httptest"
	"sort"
	"strings"
	"testing"

	"github.com/emersion/go-webdav"
	"github.com/emersion/go-webdav/internal"
)

// Tests for the scheduling (RFC 6638) support enabled by InboxBackend.

const (
	schedulingCalendarPath = "/user/calendars/a/"
	schedulingEventHref    = schedulingCalendarPath + "invite1.ics"
	schedulingInboxPath    = "/user/calendars/inbox"
	schedulingInviteHref   = schedulingInboxPath + "/invite1.ics"
)

// newSchedulingTestBackend returns a testBackend with a calendar holding an
// event, and no scheduling Inbox.
func newSchedulingTestBackend() testBackend {
	event := calendarObjectWithInvite(strings.TrimSuffix(schedulingCalendarPath, "/"), "Team Meeting")
	event.ETag = "event"
	return testBackend{
		calendars: []Calendar{{Path: schedulingCalendarPath}},
		objectMap: map[string][]CalendarObject{
			schedulingCalendarPath: {event},
		},
	}
}

// schedulingBackend is a testBackend with a scheduling Inbox holding an
// invitation, unless inbox is nil. It records the calls made to it, so tests
// can check what the server asked of it.
type schedulingBackend struct {
	testBackend
	inbox *Inbox

	inboxCalls int
	created    []string
}

func newSchedulingBackend(inbox *Inbox) *schedulingBackend {
	b := &schedulingBackend{testBackend: newSchedulingTestBackend(), inbox: inbox}
	if inbox != nil {
		invite := calendarObjectWithInvite(strings.TrimSuffix(inbox.Path, "/"), "Team Meeting Invite")
		invite.ETag = "invite"
		b.objectMap[inbox.Path] = []CalendarObject{invite}
	}
	return b
}

func (b *schedulingBackend) GetInbox(ctx context.Context) (*Inbox, error) {
	b.inboxCalls++
	return b.inbox, nil
}

func (b *schedulingBackend) CreateCalendar(ctx context.Context, cal *Calendar) error {
	b.created = append(b.created, cal.Path)
	return nil
}

// schedulingFullBackend is a schedulingBackend which also implements the
// optional interfaces of calendars, and records the paths it's called with.
type schedulingFullBackend struct {
	*schedulingBackend

	queried, deleted, updated, synced []string
}

func newSchedulingFullBackend(inbox *Inbox) *schedulingFullBackend {
	return &schedulingFullBackend{schedulingBackend: newSchedulingBackend(inbox)}
}

func (b *schedulingFullBackend) QueryCalendarObjects(ctx context.Context, path string, query *CalendarQuery) ([]CalendarObject, error) {
	b.queried = append(b.queried, path)
	cos, err := b.ListCalendarObjects(ctx, path, &query.CompRequest)
	if err != nil {
		return nil, err
	}
	return Filter(query, cos)
}

func (b *schedulingFullBackend) GetCalendarObject(ctx context.Context, path string, req *CalendarCompRequest) (*CalendarObject, error) {
	co, err := b.schedulingBackend.GetCalendarObject(ctx, path, req)
	if err != nil {
		return nil, webdav.NewHTTPError(http.StatusNotFound, err)
	}
	return co, nil
}

func (b *schedulingFullBackend) DeleteCalendarObject(ctx context.Context, path string) error {
	b.deleted = append(b.deleted, path)
	return nil
}

func (b *schedulingFullBackend) UpdateCalendar(ctx context.Context, path string, update *CalendarUpdate) error {
	b.updated = append(b.updated, path)
	return nil
}

func (b *schedulingFullBackend) SyncCollection(ctx context.Context, path string, query *SyncQuery) (*SyncResponse, error) {
	b.synced = append(b.synced, path)
	return &SyncResponse{SyncToken: "token"}, nil
}

const propFindPrivileges = `<?xml version="1.0" encoding="UTF-8"?>
<D:propfind xmlns:D="DAV:">
  <D:prop>
    <D:resourcetype/>
    <D:current-user-privilege-set/>
  </D:prop>
</D:propfind>`

// testPrivilegeSet decodes a DAV:current-user-privilege-set, independently
// of the server's own types.
type testPrivilegeSet struct {
	XMLName    xml.Name `xml:"DAV: current-user-privilege-set"`
	Privileges []struct {
		Raw []internal.RawXMLValue `xml:",any"`
	} `xml:"DAV: privilege"`
}

// names returns the sorted names of the privileges in the set, and the
// number of DAV:privilege elements which don't hold exactly one privilege.
func (s *testPrivilegeSet) names() (names []string, malformed int) {
	for _, p := range s.Privileges {
		if len(p.Raw) != 1 {
			malformed++
		}
		for _, raw := range p.Raw {
			if name, ok := raw.XMLName(); ok {
				names = append(names, name.Space+" "+name.Local)
			}
		}
	}
	sort.Strings(names)
	return names, malformed
}

// propFindResponse sends a PROPFIND request for body to h and returns the
// response for href in the multistatus.
func propFindResponse(t *testing.T, h http.Handler, target, depth, body, href string) *internal.Response {
	t.Helper()
	req := httptest.NewRequest("PROPFIND", target, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/xml")
	if depth != "" {
		req.Header.Set("Depth", depth)
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	if w.Code != http.StatusMultiStatus {
		t.Fatalf("PROPFIND %v: got status %v, want 207; body:\n%s", target, w.Code, w.Body.String())
	}
	var ms internal.MultiStatus
	if err := xml.NewDecoder(w.Body).Decode(&ms); err != nil {
		t.Fatalf("PROPFIND %v: failed to decode multistatus: %v", target, err)
	}
	for i := range ms.Responses {
		resp := &ms.Responses[i]
		for _, h := range resp.Hrefs {
			if h.Path == href {
				return resp
			}
		}
	}
	t.Fatalf("PROPFIND %v: no response for %v", target, href)
	return nil
}

func inboxPrivileges(t *testing.T, h http.Handler, inboxPath string) []string {
	t.Helper()
	resp := propFindResponse(t, h, inboxPath, "0", propFindPrivileges, inboxPath)
	var set testPrivilegeSet
	if err := resp.DecodeProp(&set); err != nil {
		t.Fatalf("current-user-privilege-set: %v", err)
	}
	names, malformed := set.names()
	if malformed > 0 {
		t.Errorf("%d DAV:privilege elements don't hold exactly one privilege (RFC 3744 section 5.4): %v", malformed, names)
	}
	return names
}

func containsString(l []string, s string) bool {
	for _, v := range l {
		if v == s {
			return true
		}
	}
	return false
}

// RFC 6638 section 6.1: the scheduling privileges are defined in the CalDAV
// namespace, and schedule-deliver aggregates schedule-deliver-invite,
// schedule-deliver-reply and schedule-query-freebusy (section 6.3), which
// are listed along with it (RFC 3744 section 5.4).
func TestInboxScheduleDeliverPrivilege(t *testing.T) {
	b := newSchedulingBackend(&Inbox{Path: schedulingInboxPath})
	names := inboxPrivileges(t, &Handler{Backend: b}, schedulingInboxPath)

	for _, want := range []string{
		namespace + " schedule-deliver",
		namespace + " schedule-deliver-invite",
		namespace + " schedule-deliver-reply",
		namespace + " schedule-query-freebusy",
	} {
		if !containsString(names, want) {
			t.Errorf("privileges %v don't contain %v", names, want)
		}
	}
	if containsString(names, "DAV: schedule-deliver") {
		t.Errorf("privileges %v contain DAV: schedule-deliver, which doesn't exist", names)
	}
}

// RFC 6638 section 2.2 and RFC 3744: the Inbox belongs to the current user,
// who can read the scheduling messages in it and, unless the Inbox is
// read-only, update and remove them. The Inbox properties can't be modified
// and clients can't add messages, so DAV:write, which aggregates
// write-properties and bind, isn't granted.
func TestInboxOwnerPrivileges(t *testing.T) {
	b := newSchedulingBackend(&Inbox{Path: schedulingInboxPath})
	names := inboxPrivileges(t, &Handler{Backend: b}, schedulingInboxPath)

	want := []string{
		"DAV: read",
		"DAV: unbind",
		"DAV: write-content",
		namespace + " schedule-deliver",
		namespace + " schedule-deliver-invite",
		namespace + " schedule-deliver-reply",
		namespace + " schedule-query-freebusy",
	}
	sort.Strings(want)
	if strings.Join(names, ", ") != strings.Join(want, ", ") {
		t.Errorf("privileges = %v, want %v", names, want)
	}
}

func TestInboxReadOnlyPrivileges(t *testing.T) {
	b := newSchedulingBackend(&Inbox{Path: schedulingInboxPath, ReadOnly: true})
	names := inboxPrivileges(t, &Handler{Backend: b}, schedulingInboxPath)

	want := []string{
		"DAV: read",
		namespace + " schedule-deliver",
		namespace + " schedule-deliver-invite",
		namespace + " schedule-deliver-reply",
		namespace + " schedule-query-freebusy",
	}
	sort.Strings(want)
	if strings.Join(names, ", ") != strings.Join(want, ", ") {
		t.Errorf("privileges = %v, want %v", names, want)
	}
}

// "calendar-auto-schedule" tells clients that the server performs implicit
// scheduling (RFC 6638 sections 2 and 3), so that they don't send scheduling
// messages themselves: it's only advertised for backends which implement
// InboxBackend, and only to users who have an Inbox.
func TestSchedulingCapability(t *testing.T) {
	for _, tc := range []struct {
		name    string
		backend Backend
		want    bool
	}{
		{"no InboxBackend", newSchedulingTestBackend(), false},
		{"InboxBackend", newSchedulingBackend(&Inbox{Path: schedulingInboxPath}), true},
		{"no Inbox for the user", newSchedulingBackend(nil), false},
	} {
		for _, target := range []string{"/", "/user/", "/user/calendars/", schedulingCalendarPath, schedulingEventHref} {
			w := serveCalDAV(tc.backend, http.MethodOptions, target, "", "")
			if w.Code != http.StatusNoContent {
				t.Fatalf("%v: OPTIONS %v: got status %v, want 204; body:\n%s", tc.name, target, w.Code, w.Body.String())
			}
			dav := w.Header().Get("DAV")
			if got := strings.Contains(dav, "calendar-auto-schedule"); got != tc.want {
				t.Errorf("%v: OPTIONS %v: DAV: %v, want calendar-auto-schedule: %v", tc.name, target, dav, tc.want)
			}
		}
	}
}

const propFindPrincipalScheduling = `<?xml version="1.0" encoding="UTF-8"?>
<D:propfind xmlns:D="DAV:" xmlns:C="urn:ietf:params:xml:ns:caldav">
  <D:prop>
    <D:resourcetype/>
    <C:calendar-home-set/>
    <C:schedule-inbox-URL/>
    <C:calendar-user-address-set/>
  </D:prop>
</D:propfind>`

// propStatus returns the status of the property name in resp.
func propStatus(t *testing.T, resp *internal.Response, name xml.Name) int {
	t.Helper()
	for _, propstat := range resp.PropStats {
		if propstat.Prop.Get(name) != nil {
			return propstat.Status.Code
		}
	}
	t.Fatalf("response for %v doesn't mention %v", resp.Hrefs, name)
	return 0
}

// GetInbox returns nil for users who aren't enabled for scheduling: the
// principal then has no Inbox, and the server mustn't fail.
func TestSchedulingDisabledForUser(t *testing.T) {
	h := &Handler{Backend: newSchedulingBackend(nil)}

	resp := propFindResponse(t, h, "/user/", "0", propFindPrincipalScheduling, "/user/")
	if code := propStatus(t, resp, calendarHomeSetName); code != http.StatusOK {
		t.Errorf("calendar-home-set: got status %v, want 200", code)
	}
	for _, name := range []xml.Name{scheduleInboxURLName, calendarUserAddressSetName} {
		if code := propStatus(t, resp, name); code != http.StatusNotFound {
			t.Errorf("%v: got status %v, want 404", name.Local, code)
		}
	}

	w := serveCalDAV(h.Backend, "PROPFIND", "/user/calendars/", "1", propFindPrivileges)
	checkStatusCodes(t, multiStatusCodes(t, w), map[string]int{
		"/user/calendars/":     http.StatusOK,
		schedulingCalendarPath: http.StatusOK,
	})
}

func checkIsInbox(t *testing.T, resp *internal.Response) {
	t.Helper()
	var rt internal.ResourceType
	if err := resp.DecodeProp(&rt); err != nil {
		t.Fatalf("%v: resourcetype: %v", resp.Hrefs, err)
	}
	if !rt.Is(internal.CollectionName) || !rt.Is(scheduleInboxName) {
		t.Errorf("%v: resourcetype %v isn't a schedule-inbox collection", resp.Hrefs, rt.Raw)
	}
}

// The Inbox is matched whether or not its path, or the request's, has a
// trailing slash, and backend paths include the handler prefix, as for
// calendars.
func TestInboxPathMatching(t *testing.T) {
	for _, tc := range []struct {
		prefix, inboxPath, target string
	}{
		{"", "/user/calendars/inbox/", "/user/calendars/inbox/"},
		{"", "/user/calendars/inbox/", "/user/calendars/inbox"},
		{"", "/user/calendars/inbox", "/user/calendars/inbox/"},
		{"/dav", "/dav/user/calendars/inbox/", "/dav/user/calendars/inbox/"},
		{"/dav/", "/dav/user/calendars/inbox", "/dav/user/calendars/inbox"},
	} {
		h := &Handler{Backend: newSchedulingBackend(&Inbox{Path: tc.inboxPath}), Prefix: tc.prefix}
		req := httptest.NewRequest("PROPFIND", tc.target, strings.NewReader(propFindPrivileges))
		req.Header.Set("Content-Type", "application/xml")
		req.Header.Set("Depth", "0")
		w := httptest.NewRecorder()
		h.ServeHTTP(w, req)
		if w.Code != http.StatusMultiStatus {
			t.Errorf("prefix %q, Inbox %v: PROPFIND %v: got status %v, want 207; body:\n%s", tc.prefix, tc.inboxPath, tc.target, w.Code, w.Body.String())
			continue
		}
		resp := propFindResponse(t, h, tc.target, "0", propFindPrivileges, tc.inboxPath)
		checkIsInbox(t, resp)
	}
}

// The members of the Inbox are calendar objects, wherever the Inbox is.
func TestInboxMembers(t *testing.T) {
	const inboxPath = "/user/inbox/"
	const inviteHref = inboxPath + "invite1.ics"
	h := &Handler{Backend: newSchedulingBackend(&Inbox{Path: inboxPath})}

	resp := propFindResponse(t, h, inviteHref, "0", propFindETag, inviteHref)
	if code := propStatus(t, resp, internal.GetETagName); code != http.StatusOK {
		t.Errorf("PROPFIND %v: getetag: got status %v, want 200", inviteHref, code)
	}

	w := serveCalDAV(h.Backend, http.MethodOptions, inviteHref, "", "")
	if allow := w.Header().Get("Allow"); !strings.Contains(allow, http.MethodPut) || !strings.Contains(allow, http.MethodDelete) {
		t.Errorf("OPTIONS %v: Allow: %v, want PUT and DELETE", inviteHref, allow)
	}
}

// Paths deeper than calendar objects aren't the Inbox.
func TestDeepPathIsNotInbox(t *testing.T) {
	h := &Handler{Backend: newSchedulingBackend(&Inbox{Path: schedulingInboxPath})}
	w := serveCalDAV(h.Backend, "PROPFIND", "/user/calendars/a/b/c", "0", propFindPrivileges)
	if strings.Contains(w.Body.String(), "schedule-inbox") {
		t.Errorf("PROPFIND /user/calendars/a/b/c returned the Inbox:\n%s", w.Body.String())
	}
}

// GetInbox is called once per request at most, and not for the root.
func TestGetInboxCalls(t *testing.T) {
	for _, tc := range []struct {
		method, target, depth, body string
		want                        int
	}{
		{"PROPFIND", "/", "0", propFindPrivileges, 0},
		{http.MethodOptions, "/user/", "", "", 1},
		{"PROPFIND", "/user/", "1", propFindPrincipalScheduling, 1},
		{"PROPFIND", "/user/calendars/", "1", propFindPrivileges, 1},
		{"PROPFIND", schedulingInboxPath, "1", propFindPrivileges, 1},
		{"PROPFIND", schedulingCalendarPath, "1", propFindPrivileges, 1},
		{http.MethodGet, schedulingEventHref, "", "", 0},
	} {
		b := newSchedulingBackend(&Inbox{Path: schedulingInboxPath})
		w := serveCalDAV(b, tc.method, tc.target, tc.depth, tc.body)
		if w.Code >= 300 {
			t.Errorf("%v %v: got status %v; body:\n%s", tc.method, tc.target, w.Code, w.Body.String())
		}
		if b.inboxCalls != tc.want {
			t.Errorf("%v %v: GetInbox called %d times, want %d", tc.method, tc.target, b.inboxCalls, tc.want)
		}
	}
}

// The Inbox is listed in the calendar home set when it's a member of it, as
// RFC 4918 section 9.1 restricts Depth: 1 to the internal members of a
// collection.
func TestInboxInHomeSet(t *testing.T) {
	b := newSchedulingBackend(&Inbox{Path: "/user/calendars/inbox/"})
	w := serveCalDAV(b, "PROPFIND", "/user/calendars/", "1", propFindPrivileges)
	checkStatusCodes(t, multiStatusCodes(t, w), map[string]int{
		"/user/calendars/":       http.StatusOK,
		schedulingCalendarPath:   http.StatusOK,
		"/user/calendars/inbox/": http.StatusOK,
	})

	w = serveCalDAV(b, "PROPFIND", "/user/calendars/", "infinity", propFindPrivileges)
	checkStatusCodes(t, multiStatusCodes(t, w), map[string]int{
		"/user/calendars/":                  http.StatusOK,
		schedulingCalendarPath:              http.StatusOK,
		schedulingEventHref:                 http.StatusOK,
		"/user/calendars/inbox/":            http.StatusOK,
		"/user/calendars/inbox/invite1.ics": http.StatusOK,
	})

	b = newSchedulingBackend(&Inbox{Path: "/user/inbox/"})
	w = serveCalDAV(b, "PROPFIND", "/user/calendars/", "1", propFindPrivileges)
	checkStatusCodes(t, multiStatusCodes(t, w), map[string]int{
		"/user/calendars/":     http.StatusOK,
		schedulingCalendarPath: http.StatusOK,
	})
}

// RFC 6638 section 2: clients can't create the Inbox.
func TestMkcalendarAtInbox(t *testing.T) {
	for _, method := range []string{"MKCALENDAR", "MKCOL"} {
		b := newSchedulingBackend(&Inbox{Path: "/user/calendars/inbox/"})
		w := serveCalDAV(b, method, "/user/calendars/inbox", "", "")
		if w.Code != http.StatusForbidden {
			t.Errorf("%v at the Inbox: got status %v, want 403", method, w.Code)
		}
		if len(b.created) > 0 {
			t.Errorf("%v at the Inbox created %v", method, b.created)
		}
	}
}

const (
	inboxMemberPath = "/user/calendars/inbox/"
	inboxMemberHref = inboxMemberPath + "invite1.ics"
)

const propFindAllPropInclude = `<?xml version="1.0" encoding="UTF-8"?>
<D:propfind xmlns:D="DAV:" xmlns:C="urn:ietf:params:xml:ns:caldav">
  <D:allprop/>
  <D:include>
    <C:schedule-inbox-URL/>
  </D:include>
</D:propfind>`

// RFC 6638 sections 2.2.1 and 2.4.1: the scheduling properties aren't
// returned by allprop, nor is DAV:current-user-privilege-set (RFC 3744
// section 5.4), unless the request includes them.
func TestSchedulingAllProp(t *testing.T) {
	h := &Handler{Backend: newSchedulingBackend(&Inbox{
		Path:           inboxMemberPath,
		UserAddressSet: []string{"mailto:user@example.com"},
	})}
	notAllProp := []string{
		"schedule-inbox-URL",
		"calendar-user-address-set",
		"current-user-privilege-set",
		"supported-report-set",
	}

	for _, target := range []string{"/user/", inboxMemberPath} {
		for _, body := range []string{"", propFindAllProp} {
			w := serveCalDAV(h.Backend, "PROPFIND", target, "0", body)
			if w.Code != http.StatusMultiStatus {
				t.Fatalf("PROPFIND %v: got status %v, want 207", target, w.Code)
			}
			resp := w.Body.String()
			if !strings.Contains(resp, "<resourcetype") {
				t.Errorf("allprop PROPFIND %v: no resourcetype:\n%s", target, resp)
			}
			for _, name := range notAllProp {
				if strings.Contains(resp, "<"+name) {
					t.Errorf("allprop PROPFIND %v returned %v:\n%s", target, name, resp)
				}
			}
		}
	}

	resp := propFindResponse(t, h, "/user/", "0", propFindAllPropInclude, "/user/")
	var inboxURL scheduleInboxURL
	if err := resp.DecodeProp(&inboxURL); err != nil {
		t.Errorf("allprop PROPFIND with include: schedule-inbox-URL: %v", err)
	} else if inboxURL.Href.Path != inboxMemberPath {
		t.Errorf("schedule-inbox-URL = %v, want %v", inboxURL.Href.Path, inboxMemberPath)
	}
}

// The properties of the Inbox can't be modified, and it isn't a calendar:
// CalendarUpdater isn't called for it.
func TestInboxPropPatch(t *testing.T) {
	b := newSchedulingFullBackend(&Inbox{Path: inboxMemberPath})
	w := serveCalDAV(b, "PROPPATCH", inboxMemberPath, "", propPatchCalendarRequest)
	if w.Code != http.StatusMultiStatus {
		t.Fatalf("PROPPATCH the Inbox: got status %v, want 207; body:\n%s", w.Code, w.Body.String())
	}
	var ms internal.MultiStatus
	if err := xml.NewDecoder(w.Body).Decode(&ms); err != nil {
		t.Fatalf("failed to decode multistatus: %v", err)
	}
	if len(ms.Responses) != 1 {
		t.Fatalf("got %d responses, want 1", len(ms.Responses))
	}
	for _, propstat := range ms.Responses[0].PropStats {
		if propstat.Status.Code != http.StatusForbidden {
			t.Errorf("PROPPATCH the Inbox: got property status %v, want 403", propstat.Status.Code)
		}
	}
	if len(b.updated) > 0 {
		t.Errorf("PROPPATCH the Inbox called UpdateCalendar for %v", b.updated)
	}
}

// RFC 6638 section 2.3 extends calendar-query and calendar-multiget to the
// Inbox; the Inbox doesn't support sync-collection, which SyncBackend
// implements for calendars.
func TestInboxSupportedReportSet(t *testing.T) {
	h := &Handler{Backend: newSchedulingFullBackend(&Inbox{Path: inboxMemberPath})}
	resp := propFindResponse(t, h, inboxMemberPath, "0", propFindSyncProps, inboxMemberPath)
	var reports testSupportedReportSet
	if err := resp.DecodeProp(&reports); err != nil {
		t.Fatalf("supported-report-set: %v", err)
	}
	want := []string{namespace + " calendar-multiget", namespace + " calendar-query"}
	if got := reports.names(); strings.Join(got, ", ") != strings.Join(want, ", ") {
		t.Errorf("supported-report-set = %v, want %v", got, want)
	}
}

func TestInboxSyncCollection(t *testing.T) {
	b := newSchedulingFullBackend(&Inbox{Path: inboxMemberPath})
	res := serveTestRequest(t, &Handler{Backend: b}, "REPORT", inboxMemberPath, syncCollectionBody("", "1", 0))
	decodeTestError(t, res, http.StatusForbidden, "supported-report")
	if len(b.synced) > 0 {
		t.Errorf("sync-collection on the Inbox called SyncCollection for %v", b.synced)
	}
}

const calendarQueryInbox = `<?xml version="1.0" encoding="UTF-8"?>
<C:calendar-query xmlns:D="DAV:" xmlns:C="urn:ietf:params:xml:ns:caldav">
  <D:prop>
    <D:getetag/>
    <C:calendar-data/>
  </D:prop>
  <C:filter>
    <C:comp-filter name="VCALENDAR">
      <C:comp-filter name="VEVENT"/>
    </C:comp-filter>
  </C:filter>
</C:calendar-query>`

const calendarMultigetInbox = `<?xml version="1.0" encoding="UTF-8"?>
<C:calendar-multiget xmlns:D="DAV:" xmlns:C="urn:ietf:params:xml:ns:caldav">
  <D:prop>
    <D:getetag/>
    <C:calendar-data/>
  </D:prop>
  <D:href>` + inboxMemberHref + `</D:href>
  <D:href>` + inboxMemberPath + `missing.ics</D:href>
</C:calendar-multiget>`

func TestInboxReports(t *testing.T) {
	b := newSchedulingFullBackend(&Inbox{Path: inboxMemberPath})

	w := serveCalDAV(b, "REPORT", inboxMemberPath, "1", calendarQueryInbox)
	checkStatusCodes(t, multiStatusCodes(t, w), map[string]int{inboxMemberHref: http.StatusOK})
	if strings.Join(b.queried, ", ") != inboxMemberPath {
		t.Errorf("calendar-query on the Inbox queried %v, want %v", b.queried, inboxMemberPath)
	}

	w = serveCalDAV(b, "REPORT", inboxMemberPath, "1", calendarMultigetInbox)
	body := w.Body.String()
	checkStatusCodes(t, multiStatusCodes(t, w), map[string]int{
		inboxMemberHref:                 http.StatusOK,
		inboxMemberPath + "missing.ics": http.StatusNotFound,
	})
	if !strings.Contains(body, "SUMMARY:Team Meeting Invite") {
		t.Errorf("calendar-multiget on the Inbox didn't return the invitation:\n%s", body)
	}
}

// RFC 6638 section 2: clients can't delete the Inbox, only the scheduling
// messages in it.
func TestInboxDelete(t *testing.T) {
	b := newSchedulingFullBackend(&Inbox{Path: inboxMemberPath})
	for _, target := range []string{inboxMemberPath, "/user/calendars/inbox"} {
		w := serveCalDAV(b, http.MethodDelete, target, "", "")
		if w.Code != http.StatusForbidden {
			t.Errorf("DELETE %v: got status %v, want 403", target, w.Code)
		}
	}
	if len(b.deleted) > 0 {
		t.Errorf("DELETE the Inbox called DeleteCalendarObject for %v", b.deleted)
	}

	w := serveCalDAV(b, http.MethodDelete, inboxMemberHref, "", "")
	if w.Code != http.StatusNoContent {
		t.Errorf("DELETE %v: got status %v, want 204", inboxMemberHref, w.Code)
	}
	if strings.Join(b.deleted, ", ") != inboxMemberHref {
		t.Errorf("DELETE %v deleted %v", inboxMemberHref, b.deleted)
	}
}
