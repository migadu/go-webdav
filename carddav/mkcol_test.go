package carddav

import (
	"encoding/xml"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/emersion/go-webdav/internal"
)

// mkcolBody returns the body of an extended MKCOL request (RFC 5689 section
// 3) setting props.
func mkcolBody(props string) string {
	return `<?xml version="1.0" encoding="utf-8"?>
<D:mkcol xmlns:D="DAV:" xmlns:C="urn:ietf:params:xml:ns:caldav" xmlns:CR="urn:ietf:params:xml:ns:carddav" xmlns:X="http://example.com/ns/">
  <D:set><D:prop>` + props + `</D:prop></D:set>
</D:mkcol>`
}

// mkcolErrorBody summarizes the XML body of an error response to a MKCOL
// request: the conditions of a DAV:error element, or the propstat elements
// of a DAV:mkcol-response element (RFC 5689 section 3.5), each with its
// status, properties and conditions. Another body is summarized as "".
func mkcolErrorBody(t *testing.T, w *httptest.ResponseRecorder) string {
	t.Helper()
	if !strings.HasPrefix(w.Header().Get("Content-Type"), "application/xml") {
		return ""
	}
	var body struct {
		XMLName   xml.Name
		Raw       []internal.RawXMLValue `xml:",any"`
		PropStats []internal.PropStat    `xml:"DAV: propstat"`
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
	switch body.XMLName {
	case xml.Name{Space: "DAV:", Local: "error"}:
		return "error " + names(body.Raw)
	case xml.Name{Space: "DAV:", Local: "mkcol-response"}:
		var l []string
		for _, ps := range body.PropStats {
			s := fmt.Sprintf("%v %v", ps.Status.Code, names(ps.Prop.Raw))
			if ps.Error != nil {
				s += " error " + names(ps.Error.Raw)
			}
			l = append(l, s)
		}
		return "mkcol-response " + strings.Join(l, "; ")
	}
	return "<" + body.XMLName.Space + " " + body.XMLName.Local + ">"
}

const (
	mkcolAddressBookType = `<D:resourcetype><D:collection/><CR:addressbook/></D:resourcetype>`
	mkcolCalendarType    = `<D:resourcetype><D:collection/><C:calendar/></D:resourcetype>`
)

// A MKCOL request creates an address book only if it's an extended one asking
// for it (RFC 6352 section 6.3.1). The errors are reported with the
// preconditions of RFC 5689 section 3.3 and RFC 6352 section 6.3.2.1, those
// of properties in a DAV:mkcol-response element (RFC 5689 section 3).
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
			body:     mkcolBody(mkcolAddressBookType + `<D:displayname>Work</D:displayname>`),
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
			// The DAV:set and DAV:prop elements are in the DAV: namespace
			// (RFC 5689 section 5.1): these hold no property
			name: "set and prop in another namespace",
			body: `<D:mkcol xmlns:D="DAV:" xmlns:CR="urn:ietf:params:xml:ns:carddav" xmlns:X="http://example.com/ns/">
  <X:set><X:prop>` + mkcolAddressBookType + `</X:prop></X:set>
</D:mkcol>`,
			wantCode: http.StatusForbidden,
			wantBody: "error {DAV:}valid-resourcetype",
		},
		{
			// RFC 5689 section 3.5
			name:     "calendar resource type",
			body:     mkcolBody(mkcolCalendarType + `<D:displayname>Work</D:displayname>`),
			wantCode: http.StatusForbidden,
			wantBody: "mkcol-response 403 {DAV:}resourcetype error {DAV:}valid-resourcetype; 424 {DAV:}displayname",
		},
		{
			name:     "resource type without collection",
			body:     mkcolBody(`<D:resourcetype><CR:addressbook/></D:resourcetype>`),
			wantCode: http.StatusForbidden,
			wantBody: "mkcol-response 403 {DAV:}resourcetype error {DAV:}valid-resourcetype",
		},
		{
			name:     "address book home set",
			path:     "/user/contacts/",
			body:     mkcolBody(mkcolAddressBookType),
			wantCode: http.StatusForbidden,
			wantBody: "error {urn:ietf:params:xml:ns:carddav}addressbook-collection-location-ok",
		},
		{
			name:     "address object",
			path:     "/user/contacts/work/card.vcf",
			body:     mkcolBody(mkcolAddressBookType),
			wantCode: http.StatusForbidden,
			wantBody: "error {urn:ietf:params:xml:ns:carddav}addressbook-collection-location-ok",
		},
		{
			// RFC 4918 section 9.2.1: "403 (Forbidden): The client has
			// attempted to set a protected property, such as
			// DAV:getetag."
			name:     "protected property",
			body:     mkcolBody(mkcolAddressBookType + `<D:displayname>Work</D:displayname><D:getetag>"1"</D:getetag>`),
			wantCode: http.StatusForbidden,
			wantBody: "mkcol-response 403 {DAV:}getetag error {DAV:}cannot-modify-protected-property; 424 {DAV:}resourcetype,{DAV:}displayname",
		},
		{
			// RFC 6352 section 6.2.2: "MUST be protected"
			name: "protected CardDAV property",
			body: mkcolBody(mkcolAddressBookType + `<CR:supported-address-data>
  <CR:address-data-type content-type="text/vcard" version="3.0"/>
</CR:supported-address-data>`),
			wantCode: http.StatusForbidden,
			wantBody: "mkcol-response 403 {urn:ietf:params:xml:ns:carddav}supported-address-data error {DAV:}cannot-modify-protected-property; 424 {DAV:}resourcetype",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			backend := &testBackend{}
			handler := Handler{Backend: backend}

			p := tc.path
			if p == "" {
				p = "/user/contacts/work/"
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
			if len(backend.addressBooks) > 0 {
				t.Errorf("MKCOL created %+v", backend.addressBooks)
			}
		})
	}
}

// The properties of an extended MKCOL request are matched by their namespace
// as well as their name: another property named displayname is ignored, as
// properties the server doesn't support are.
func TestMkcolPropertyNamespaces(t *testing.T) {
	backend := &testBackend{}
	handler := Handler{Backend: backend}

	req := httptest.NewRequest("MKCOL", "/user/contacts/work/", strings.NewReader(mkcolBody(mkcolAddressBookType+`
<D:displayname>Work</D:displayname>
<X:displayname>Other</X:displayname>
<CR:addressbook-description>Colleagues</CR:addressbook-description>
<X:addressbook-description>Other</X:addressbook-description>`)))
	req.Header.Set("Content-Type", "application/xml")
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	if w.Code != http.StatusCreated {
		t.Fatalf("MKCOL returned %v: %s", w.Code, w.Body.String())
	}
	if len(backend.addressBooks) != 1 {
		t.Fatalf("created %+v, want one address book", backend.addressBooks)
	}
	if ab := backend.addressBooks[0]; ab.Name != "Work" || ab.Description != "Colleagues" {
		t.Errorf("created address book %+v, want the name and description in their namespaces", ab)
	}
}
