package caldav

import (
	"encoding/xml"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"

	"github.com/emersion/go-webdav/internal"
)

// propPatchErrors returns the status of each property of the response to a
// PROPPATCH request, followed by the conditions of its DAV:error element if
// any.
func propPatchErrors(t *testing.T, h http.Handler, path, body string) map[string]string {
	t.Helper()

	req := httptest.NewRequest("PROPPATCH", path, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/xml")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)

	if w.Code != http.StatusMultiStatus {
		t.Fatalf("PROPPATCH returned %v, want 207: %s", w.Code, w.Body.String())
	}
	var ms internal.MultiStatus
	if err := xml.NewDecoder(w.Body).Decode(&ms); err != nil {
		t.Fatal(err)
	}
	errs := make(map[string]string)
	for _, propstat := range ms.Responses[0].PropStats {
		s := fmt.Sprint(propstat.Status.Code)
		if propstat.Error != nil {
			for _, raw := range propstat.Error.Raw {
				name, _ := raw.XMLName()
				s += " {" + name.Space + "}" + name.Local
			}
		}
		for _, raw := range propstat.Prop.Raw {
			name, _ := raw.XMLName()
			errs["{"+name.Space+"}"+name.Local] = s
		}
	}
	return errs
}

// RFC 4918 section 9.2.1: "403 (Forbidden): The client has attempted to set
// a protected property, such as DAV:getetag. If returning this error, the
// server SHOULD use the precondition code 'cannot-modify-protected-property'
// inside the response body." The other properties get 424 Failed
// Dependency, and nothing is changed (section 9.2).
func TestPropPatchCalendarProtectedProps(t *testing.T) {
	const calPath = "/user/calendars/work/"
	const protected = "403 {DAV:}cannot-modify-protected-property"
	for _, tc := range []struct {
		name, prop string
		remove     bool
		want       string
	}{
		{name: "getetag", prop: `<D:getetag>"1"</D:getetag>`, want: protected},
		{name: "sync-token", prop: `<D:sync-token>http://example.com/ns/sync/1</D:sync-token>`, want: protected},
		{name: "getctag", prop: `<CS:getctag>1</CS:getctag>`, want: protected},
		// RFC 4918 section 15.9: "Resource type is generally decided
		// through the operation creating the resource (MKCOL vs PUT),
		// not by PROPPATCH."
		{name: "resourcetype", prop: `<D:resourcetype><D:collection/></D:resourcetype>`, want: protected},
		// RFC 4791 section 5.2.3: "Since this property is protected, it
		// cannot be changed by clients using a PROPPATCH request."
		{name: "supported-calendar-component-set", prop: `<C:supported-calendar-component-set><C:comp name="VTODO"/></C:supported-calendar-component-set>`, want: protected},
		{name: "supported-calendar-data", prop: `<C:supported-calendar-data><C:calendar-data content-type="text/calendar" version="2.0"/></C:supported-calendar-data>`, want: protected},
		{name: "removed max-resource-size", prop: `<C:max-resource-size/>`, remove: true, want: protected},
		// The server doesn't store dead properties: refused, for reasons
		// it doesn't specify
		{name: "dead property", prop: `<A:calendar-order>2</A:calendar-order>`, want: "403"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			backend := &updateBackend{testBackend: testBackend{calendars: []Calendar{{Path: calPath}}}}
			h := &Handler{Backend: backend}

			instruction := "set"
			if tc.remove {
				instruction = "remove"
			}
			errs := propPatchErrors(t, h, calPath, `<?xml version="1.0" encoding="UTF-8"?>
<D:propertyupdate xmlns:D="DAV:" xmlns:C="urn:ietf:params:xml:ns:caldav" xmlns:A="http://apple.com/ns/ical/" xmlns:CS="http://calendarserver.org/ns/">
  <D:set><D:prop><D:displayname>Work</D:displayname></D:prop></D:set>
  <D:`+instruction+`><D:prop>`+tc.prop+`</D:prop></D:`+instruction+`>
</D:propertyupdate>`)

			delete(errs, "{DAV:}displayname")
			if len(errs) != 1 {
				t.Fatalf("propstats = %v, want displayname and one other property", errs)
			}
			for name, got := range errs {
				if got != tc.want {
					t.Errorf("%v: propstat = %q, want %q", name, got, tc.want)
				}
			}
			if len(backend.updates) != 0 {
				t.Errorf("updates = %+v, want none", backend.updates)
			}
		})
	}

	// displayname itself gets 424
	backend := &updateBackend{testBackend: testBackend{calendars: []Calendar{{Path: calPath}}}}
	errs := propPatchErrors(t, &Handler{Backend: backend}, calPath, `<?xml version="1.0" encoding="UTF-8"?>
<D:propertyupdate xmlns:D="DAV:">
  <D:set><D:prop><D:displayname>Work</D:displayname><D:getetag>"1"</D:getetag></D:prop></D:set>
</D:propertyupdate>`)
	want := map[string]string{"{DAV:}displayname": "424", "{DAV:}getetag": protected}
	if !reflect.DeepEqual(errs, want) {
		t.Errorf("propstats = %v, want %v", errs, want)
	}
}
