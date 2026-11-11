package caldav

import (
	"context"
	"encoding/xml"
	"net/http"
	"net/http/httptest"
	"sort"
	"strings"
	"testing"

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
}

func newSchedulingBackend(inbox *Inbox) *schedulingBackend {
	b := &schedulingBackend{testBackend: newSchedulingTestBackend(), inbox: inbox}
	if inbox != nil {
		invite := calendarObjectWithInvite(strings.TrimSuffix(inbox.Path, "/"), "Team Meeting Invite")
		b.objectMap[inbox.Path] = []CalendarObject{invite}
	}
	return b
}

func (b *schedulingBackend) GetInbox(ctx context.Context) (*Inbox, error) {
	b.inboxCalls++
	return b.inbox, nil
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
