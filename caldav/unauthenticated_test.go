package caldav

import (
	"encoding/xml"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/emersion/go-webdav/internal"
)

const propFindUnauthenticated = `<?xml version="1.0" encoding="utf-8"?>
<d:propfind xmlns:d="DAV:">
  <d:prop>
    <d:current-user-principal/>
    <d:owner/>
    <d:principal-collection-set/>
  </d:prop>
</d:propfind>`

type unauthenticatedCurrentUserPrincipal struct {
	XMLName         xml.Name  `xml:"DAV: current-user-principal"`
	Href            *string   `xml:"DAV: href"`
	Unauthenticated *struct{} `xml:"DAV: unauthenticated"`
}

// RFC 5397 section 3: when the backend has no current user, such as for a
// public calendar, DAV:current-user-principal is DAV:unauthenticated, not
// an empty href. DAV:owner and DAV:principal-collection-set are empty
// rather than holding an empty href (RFC 3744 sections 5.1 and 5.8).
func TestPropFindUnauthenticated(t *testing.T) {
	const (
		homeSet = "/public/calendars/"
		calPath = "/public/calendars/a/"
	)
	obj := newSyncTestObject("event", "Event", "etag-event")
	obj.Path = calPath + "event.ics"
	h := &Handler{Backend: aclPrincipalBackend{
		testBackend: testBackend{
			calendars: []Calendar{{Path: calPath}},
			objectMap: map[string][]CalendarObject{calPath: {obj}},
		},
		homeSet: homeSet,
	}}

	for _, tc := range []struct {
		name, path, depth string
		hrefs             []string
	}{
		{"root", "/", "0", []string{"/"}},
		{"home set", homeSet, "0", []string{homeSet}},
		{"calendar and object", calPath, "1", []string{calPath, obj.Path}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest("PROPFIND", tc.path, strings.NewReader(propFindUnauthenticated))
			req.Header.Set("Content-Type", "application/xml")
			req.Header.Set("Depth", tc.depth)
			w := httptest.NewRecorder()
			h.ServeHTTP(w, req)
			if w.Code != http.StatusMultiStatus {
				t.Fatalf("status = %v, want 207; body:\n%s", w.Code, w.Body)
			}
			body := w.Body.String()
			if strings.Contains(body, "<href></href>") {
				t.Errorf("response has an empty href:\n%s", body)
			}

			var ms internal.MultiStatus
			if err := xml.Unmarshal(w.Body.Bytes(), &ms); err != nil {
				t.Fatalf("failed to decode multistatus: %v\n%s", err, body)
			}
			if len(ms.Responses) != len(tc.hrefs) {
				t.Fatalf("got %d responses, want %d:\n%s", len(ms.Responses), len(tc.hrefs), body)
			}
			for i := range ms.Responses {
				resp := &ms.Responses[i]
				p := resp.Hrefs[0].Path
				if p != tc.hrefs[i] {
					t.Errorf("response %d is for %v, want %v", i, p, tc.hrefs[i])
				}
				var cup unauthenticatedCurrentUserPrincipal
				if err := resp.DecodeProp(&cup); err != nil {
					t.Errorf("%v: current-user-principal: %v", p, err)
				} else if cup.Unauthenticated == nil || cup.Href != nil {
					t.Errorf("%v: current-user-principal isn't DAV:unauthenticated:\n%s", p, body)
				}
			}
		})
	}
}
