package caldav

import (
	"encoding/xml"
	"net/http"
	"strings"
	"testing"

	"github.com/emersion/go-webdav/internal"
)

// Tests for the access control properties of RFC 3744 section 5.

const propFindACLProps = `<?xml version="1.0" encoding="UTF-8"?>
<D:propfind xmlns:D="DAV:">
  <D:prop>
    <D:owner/>
    <D:supported-privilege-set/>
    <D:principal-collection-set/>
  </D:prop>
</D:propfind>`

// The privileges defined on the resources: DAV:all aggregates all of them,
// DAV:write aggregates the four privileges RFC 3744 section 3.12 requires it
// to.
const davPrivilegeTree = `DAV: all (abstract)
  DAV: read
    DAV: read-current-user-privilege-set (abstract)
  DAV: write
    DAV: write-properties
    DAV: write-content
    DAV: bind
    DAV: unbind
`

// The scheduling Inbox also defines the CalDAV scheduling privileges which
// apply to it (RFC 6638 sections 6.1 and 6.3).
const inboxPrivilegeTree = davPrivilegeTree + `  urn:ietf:params:xml:ns:caldav schedule-deliver
    urn:ietf:params:xml:ns:caldav schedule-deliver-invite
    urn:ietf:params:xml:ns:caldav schedule-deliver-reply
    urn:ietf:params:xml:ns:caldav schedule-query-freebusy
`

// testSupportedPrivilege decodes a DAV:supported-privilege, independently of
// the server's own types.
type testSupportedPrivilege struct {
	Privilege struct {
		Raw []internal.RawXMLValue `xml:",any"`
	} `xml:"DAV: privilege"`
	Abstract    *struct{} `xml:"DAV: abstract"`
	Description *struct {
		Lang string `xml:"http://www.w3.org/XML/1998/namespace lang,attr"`
		Text string `xml:",chardata"`
	} `xml:"DAV: description"`
	Aggregated []testSupportedPrivilege `xml:"DAV: supported-privilege"`
}

type testSupportedPrivilegeSet struct {
	XMLName    xml.Name                 `xml:"DAV: supported-privilege-set"`
	Privileges []testSupportedPrivilege `xml:"DAV: supported-privilege"`
}

// tree renders the privileges of the set, one per line, indented below the
// privilege which aggregates them. It reports the privileges which don't have
// the single privilege and the description in a human language that RFC 3744
// section 5.3 requires.
func (s *testSupportedPrivilegeSet) tree(t *testing.T) string {
	t.Helper()
	var sb strings.Builder
	var walk func(sps []testSupportedPrivilege, indent string)
	walk = func(sps []testSupportedPrivilege, indent string) {
		for _, sp := range sps {
			var names []string
			for _, raw := range sp.Privilege.Raw {
				if name, ok := raw.XMLName(); ok {
					names = append(names, name.Space+" "+name.Local)
				}
			}
			if len(names) != 1 {
				t.Errorf("supported-privilege has privileges %v, want one", names)
			}
			if sp.Description == nil || strings.TrimSpace(sp.Description.Text) == "" || sp.Description.Lang == "" {
				t.Errorf("supported-privilege %v has description %+v, want a text with xml:lang", names, sp.Description)
			}
			sb.WriteString(indent + strings.Join(names, ", "))
			if sp.Abstract != nil {
				sb.WriteString(" (abstract)")
			}
			sb.WriteString("\n")
			walk(sp.Aggregated, indent+"  ")
		}
	}
	walk(s.Privileges, "")
	return sb.String()
}

// nonAbstract returns the privileges of the set which aren't abstract.
func (s *testSupportedPrivilegeSet) nonAbstract() map[string]bool {
	m := make(map[string]bool)
	var walk func(sps []testSupportedPrivilege)
	walk = func(sps []testSupportedPrivilege) {
		for _, sp := range sps {
			for _, raw := range sp.Privilege.Raw {
				if name, ok := raw.XMLName(); ok && sp.Abstract == nil {
					m[name.Space+" "+name.Local] = true
				}
			}
			walk(sp.Aggregated)
		}
	}
	walk(s.Privileges)
	return m
}

// findResponse decodes the multistatus body and returns the response for
// href.
func findResponse(t *testing.T, body []byte, href string) *internal.Response {
	t.Helper()
	var ms internal.MultiStatus
	if err := xml.Unmarshal(body, &ms); err != nil {
		t.Fatalf("failed to decode multistatus: %v", err)
	}
	for i := range ms.Responses {
		for _, h := range ms.Responses[i].Hrefs {
			if h.Path == href {
				return &ms.Responses[i]
			}
		}
	}
	t.Fatalf("no response for %v in:\n%s", href, body)
	return nil
}

func TestPropFindACLProps(t *testing.T) {
	inbox := &Inbox{Path: schedulingInboxPath}
	for _, tc := range []struct {
		name, path, tree string
		backend          Backend
	}{
		{"principal", "/user/", davPrivilegeTree, newSchedulingTestBackend()},
		{"home set", "/user/calendars/", davPrivilegeTree, newSchedulingTestBackend()},
		{"calendar", schedulingCalendarPath, davPrivilegeTree, newSchedulingTestBackend()},
		{"object", schedulingEventHref, davPrivilegeTree, newSchedulingTestBackend()},
		{"inbox", schedulingInboxPath, inboxPrivilegeTree, newSchedulingBackend(inbox)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			w := serveCalDAV(tc.backend, "PROPFIND", tc.path, "0", propFindACLProps)
			if w.Code != http.StatusMultiStatus {
				t.Fatalf("PROPFIND returned %v:\n%s", w.Code, w.Body.String())
			}
			body := w.Body.String()

			// RFC 3744 section 5.1: the current user owns the resources
			// the server serves for them
			if want := `<owner xmlns="DAV:"><href>/user/</href></owner>`; !strings.Contains(body, want) {
				t.Errorf("want %s in:\n%s", want, body)
			}
			// RFC 3744 section 5.8: the collection which holds the
			// principal
			if want := `<principal-collection-set xmlns="DAV:"><href>/</href></principal-collection-set>`; !strings.Contains(body, want) {
				t.Errorf("want %s in:\n%s", want, body)
			}

			resp := findResponse(t, w.Body.Bytes(), tc.path)
			var set testSupportedPrivilegeSet
			if err := resp.DecodeProp(&set); err != nil {
				t.Fatalf("supported-privilege-set: %v", err)
			}
			if got := set.tree(t); got != tc.tree {
				t.Errorf("supported-privilege-set =\n%s\nwant:\n%s", got, tc.tree)
			}
		})
	}
}

// RFC 3744 section 5.4: each privilege of DAV:current-user-privilege-set is a
// non-abstract privilege of DAV:supported-privilege-set.
func TestCurrentUserPrivilegesSupported(t *testing.T) {
	for _, readOnly := range []bool{false, true} {
		b := newSchedulingBackend(&Inbox{Path: schedulingInboxPath, ReadOnly: readOnly})
		b.calendars[0].ReadOnly = readOnly
		objs := b.objectMap[schedulingCalendarPath]
		objs[0].ReadOnly = readOnly

		for _, p := range []string{schedulingCalendarPath, schedulingEventHref, schedulingInboxPath} {
			body := strings.Replace(propFindPrivileges, "<D:resourcetype/>", "<D:supported-privilege-set/>", 1)
			w := serveCalDAV(b, "PROPFIND", p, "0", body)
			if w.Code != http.StatusMultiStatus {
				t.Fatalf("PROPFIND %v returned %v:\n%s", p, w.Code, w.Body.String())
			}
			resp := findResponse(t, w.Body.Bytes(), p)

			var set testSupportedPrivilegeSet
			if err := resp.DecodeProp(&set); err != nil {
				t.Fatalf("PROPFIND %v: supported-privilege-set: %v", p, err)
			}
			supported := set.nonAbstract()

			var current testPrivilegeSet
			if err := resp.DecodeProp(&current); err != nil {
				t.Fatalf("PROPFIND %v: current-user-privilege-set: %v", p, err)
			}
			for _, priv := range current.Privileges {
				for _, raw := range priv.Raw {
					name, _ := raw.XMLName()
					if !supported[name.Space+" "+name.Local] {
						t.Errorf("readOnly=%v: PROPFIND %v: current user privilege %v isn't a non-abstract supported privilege", readOnly, p, name)
					}
				}
			}
		}
	}
}

// RFC 3744 section 5: an allprop PROPFIND request doesn't return the access
// control properties.
func TestAllPropNoACLProps(t *testing.T) {
	b := newSchedulingBackend(&Inbox{Path: schedulingInboxPath})
	for _, p := range []string{"/user/", "/user/calendars/", schedulingCalendarPath, schedulingEventHref, schedulingInboxPath} {
		w := serveCalDAV(b, "PROPFIND", p, "0", propFindAllProp)
		if w.Code != http.StatusMultiStatus {
			t.Fatalf("PROPFIND %v returned %v:\n%s", p, w.Code, w.Body.String())
		}
		for _, name := range []string{"owner", "supported-privilege-set", "principal-collection-set"} {
			if strings.Contains(w.Body.String(), "<"+name+" ") {
				t.Errorf("allprop PROPFIND %v returned DAV:%v:\n%s", p, name, w.Body.String())
			}
		}
	}
}
