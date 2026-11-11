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
	schedulingInboxPath  = "/user/calendars/inbox"
	schedulingInviteHref = schedulingInboxPath + "/invite1.ics"
)

// schedulingBackend is a testBackend with a scheduling Inbox. It records the
// calls made to it, so tests can check what the server asked of it.
type schedulingBackend struct {
	testBackend
	inbox *Inbox

	inboxCalls int
}

func newSchedulingBackend(inbox *Inbox) *schedulingBackend {
	invite := calendarObjectWithInvite(inbox.Path, "Team Meeting Invite")
	return &schedulingBackend{
		testBackend: testBackend{
			calendars: []Calendar{{Path: "/user/calendars/a/"}},
			objectMap: map[string][]CalendarObject{
				inbox.Path: {invite},
			},
		},
		inbox: inbox,
	}
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
