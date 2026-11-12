package caldav

import (
	"context"
	"encoding/xml"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/emersion/go-webdav/internal"
)

// createBackend records the calendars it is asked to create.
type createBackend struct {
	testBackend
	created []Calendar
}

func (b *createBackend) CreateCalendar(ctx context.Context, cal *Calendar) error {
	b.created = append(b.created, *cal)
	return nil
}

// A MKCOL which doesn't set supported-calendar-component-set leaves it to the
// server, which defaults to VEVENT.
func TestMkcolWithoutComponentSet(t *testing.T) {
	backend := &createBackend{}
	handler := Handler{Backend: backend}

	req := httptest.NewRequest("MKCOL", "/user/calendars/work/", strings.NewReader(`<?xml version="1.0" encoding="utf-8"?>
<D:mkcol xmlns:D="DAV:" xmlns:C="urn:ietf:params:xml:ns:caldav">
  <D:set><D:prop>
    <D:resourcetype><D:collection/><C:calendar/></D:resourcetype>
    <D:displayname>Work</D:displayname>
  </D:prop></D:set>
</D:mkcol>`))
	req.Header.Set("Content-Type", "application/xml")
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	if w.Code != http.StatusCreated {
		t.Fatalf("MKCOL returned %v: %s", w.Code, w.Body.String())
	}
	if len(backend.created) != 1 {
		t.Fatalf("CreateCalendar called %d times, want 1", len(backend.created))
	}
	if cal := backend.created[0]; cal.Name != "Work" || cal.SupportedComponentSet != nil {
		t.Errorf("created calendar %+v, want name Work and no component set", cal)
	}
}

// mkcolBody returns the body of an extended MKCOL request (RFC 5689 section
// 3) setting props.
func mkcolBody(props string) string {
	return `<?xml version="1.0" encoding="utf-8"?>
<D:mkcol xmlns:D="DAV:" xmlns:C="urn:ietf:params:xml:ns:caldav" xmlns:CR="urn:ietf:params:xml:ns:carddav">
  <D:set><D:prop>` + props + `</D:prop></D:set>
</D:mkcol>`
}

// mkcolErrorBody summarizes the XML body of an error response to a MKCOL or
// a MKCALENDAR request: the conditions of a DAV:error element, or the
// propstat elements of a DAV:mkcol-response element (RFC 5689 section 3.5)
// or of the responses of a DAV:multistatus element (RFC 4791 section
// 5.3.1.1), each with its status, properties and conditions. Another body is
// summarized as "".
func mkcolErrorBody(t *testing.T, w *httptest.ResponseRecorder) string {
	t.Helper()
	if !strings.HasPrefix(w.Header().Get("Content-Type"), "application/xml") {
		return ""
	}
	var body struct {
		XMLName   xml.Name
		Raw       []internal.RawXMLValue `xml:",any"`
		PropStats []internal.PropStat    `xml:"DAV: propstat"`
		Responses []internal.Response    `xml:"DAV: response"`
	}
	if err := xml.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatalf("malformed XML body %q: %v", w.Body.String(), err)
	}
	names := func(raws []internal.RawXMLValue) string {
		var l []string
		for _, raw := range raws {
			name, _ := raw.XMLName()
			l = append(l, "{"+name.Space+"}"+name.Local)
		}
		return strings.Join(l, ",")
	}
	propStats := func(propStats []internal.PropStat) string {
		var l []string
		for _, ps := range propStats {
			s := fmt.Sprintf("%v %v", ps.Status.Code, names(ps.Prop.Raw))
			if ps.Error != nil {
				s += " error " + names(ps.Error.Raw)
			}
			l = append(l, s)
		}
		return strings.Join(l, "; ")
	}
	switch body.XMLName {
	case xml.Name{Space: "DAV:", Local: "error"}:
		return "error " + names(body.Raw)
	case xml.Name{Space: "DAV:", Local: "mkcol-response"}:
		return "mkcol-response " + propStats(body.PropStats)
	case xml.Name{Space: "DAV:", Local: "multistatus"}:
		var l []string
		for _, resp := range body.Responses {
			var hrefs []string
			for _, href := range resp.Hrefs {
				hrefs = append(hrefs, href.Path)
			}
			l = append(l, strings.Join(hrefs, ",")+" "+propStats(resp.PropStats))
		}
		return "multistatus " + strings.Join(l, " | ")
	}
	return "<" + body.XMLName.Space + " " + body.XMLName.Local + ">"
}

const (
	mkcolCalendarType    = `<D:resourcetype><D:collection/><C:calendar/></D:resourcetype>`
	mkcolAddressBookType = `<D:resourcetype><D:collection/><CR:addressbook/></D:resourcetype>`
)

// A MKCOL request creates a calendar only if it's an extended one asking for
// it (RFC 5689 section 4.1). The errors are reported with the preconditions
// of RFC 5689 section 3.3 and RFC 4791 section 5.3.1, those of properties in
// a DAV:mkcol-response element (RFC 5689 section 3).
func TestMkcolErrors(t *testing.T) {
	for _, tc := range []struct {
		name        string
		path        string
		contentType string
		body        string
		// maxBytes, if set, limits the size of the request body
		maxBytes int64
		wantCode int
		wantBody string
	}{
		{
			// RFC 4918 section 9.3: an ordinary collection
			name:     "without a body",
			wantCode: http.StatusForbidden,
			wantBody: "error {DAV:}valid-resourcetype",
		},
		{
			// RFC 4918 section 9.3
			name:        "body which isn't XML",
			contentType: "text/plain",
			body:        "work",
			wantCode:    http.StatusUnsupportedMediaType,
		},
		{
			name:     "body larger than allowed",
			body:     mkcolBody(mkcolCalendarType + `<D:displayname>Work</D:displayname>`),
			maxBytes: 64,
			wantCode: http.StatusRequestEntityTooLarge,
		},
		{
			name:     "malformed body",
			body:     `<D:mkcol xmlns:D="DAV:"><D:set>`,
			wantCode: http.StatusBadRequest,
		},
		{
			// An ordinary collection, with a name
			name:     "without a resource type",
			body:     mkcolBody(`<D:displayname>Work</D:displayname>`),
			wantCode: http.StatusForbidden,
			wantBody: "error {DAV:}valid-resourcetype",
		},
		{
			// RFC 5689 section 3.5
			name:     "address book resource type",
			body:     mkcolBody(mkcolAddressBookType + `<D:displayname>Work</D:displayname>`),
			wantCode: http.StatusForbidden,
			wantBody: "mkcol-response 403 {DAV:}resourcetype error {DAV:}valid-resourcetype; 424 {DAV:}displayname",
		},
		{
			name:     "resource type without collection",
			body:     mkcolBody(`<D:resourcetype><C:calendar/></D:resourcetype>`),
			wantCode: http.StatusForbidden,
			wantBody: "mkcol-response 403 {DAV:}resourcetype error {DAV:}valid-resourcetype",
		},
		{
			// RFC 4791 section 5.3.1
			name:     "calendar home set",
			path:     "/user/calendars/",
			body:     mkcolBody(mkcolCalendarType),
			wantCode: http.StatusForbidden,
			wantBody: "error {urn:ietf:params:xml:ns:caldav}calendar-collection-location-ok",
		},
		{
			name:     "calendar object",
			path:     "/user/calendars/work/event.ics",
			body:     mkcolBody(mkcolCalendarType),
			wantCode: http.StatusForbidden,
			wantBody: "error {urn:ietf:params:xml:ns:caldav}calendar-collection-location-ok",
		},
		{
			// RFC 4791 section 5.3.1
			name: "invalid timezone",
			body: mkcolBody(mkcolCalendarType + `<D:displayname>Work</D:displayname>
<C:calendar-timezone>Europe/Berlin</C:calendar-timezone>`),
			wantCode: http.StatusConflict,
			wantBody: "mkcol-response 409 {urn:ietf:params:xml:ns:caldav}calendar-timezone error {urn:ietf:params:xml:ns:caldav}valid-calendar-data; 424 {DAV:}resourcetype,{DAV:}displayname",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			backend := &createBackend{}
			handler := Handler{Backend: backend}

			p := tc.path
			if p == "" {
				p = "/user/calendars/work/"
			}
			req := httptest.NewRequest("MKCOL", p, strings.NewReader(tc.body))
			if tc.contentType != "" {
				req.Header.Set("Content-Type", tc.contentType)
			} else if tc.body != "" {
				req.Header.Set("Content-Type", "application/xml")
			}
			w := httptest.NewRecorder()
			if tc.maxBytes > 0 {
				req.Body = http.MaxBytesReader(w, req.Body, tc.maxBytes)
			}
			handler.ServeHTTP(w, req)

			if w.Code != tc.wantCode {
				t.Errorf("MKCOL returned %v, want %v: %s", w.Code, tc.wantCode, w.Body.String())
			}
			if tc.wantBody != "" {
				if body := mkcolErrorBody(t, w); body != tc.wantBody {
					t.Errorf("MKCOL body is %q, want %q\n%s", body, tc.wantBody, w.Body.String())
				}
			}
			if len(backend.created) > 0 {
				t.Errorf("MKCOL created %+v", backend.created)
			}
		})
	}
}
