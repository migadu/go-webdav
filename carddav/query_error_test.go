package carddav

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// An is-not-defined element with siblings breaks the grammar of RFC 6352
// sections 10.5.1 and 10.5.2. Unlike CalDAV, CardDAV defines no valid-filter
// precondition for addressbook-query (section 8.6): CARDDAV:supported-filter
// is about properties and parameters the server can't query, so the request
// is a plain 400 Bad Request, without a DAV:error element.
func TestAddressBookQueryInvalidFilterReturns400(t *testing.T) {
	for _, tc := range []struct {
		name   string
		filter string
	}{
		{
			name:   "prop-filter is-not-defined with text-match",
			filter: `<C:prop-filter name="FN"><C:is-not-defined/><C:text-match>Alice</C:text-match></C:prop-filter>`,
		},
		{
			name:   "prop-filter is-not-defined with param-filter",
			filter: `<C:prop-filter name="TEL"><C:is-not-defined/><C:param-filter name="TYPE"/></C:prop-filter>`,
		},
		{
			name:   "param-filter is-not-defined with text-match",
			filter: `<C:prop-filter name="TEL"><C:param-filter name="TYPE"><C:is-not-defined/><C:text-match>work</C:text-match></C:param-filter></C:prop-filter>`,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			body := `<?xml version="1.0" encoding="UTF-8"?>
<C:addressbook-query xmlns:D="DAV:" xmlns:C="urn:ietf:params:xml:ns:carddav">
  <D:prop><D:getetag/></D:prop>
  <C:filter>` + tc.filter + `</C:filter>
</C:addressbook-query>`
			req := httptest.NewRequest("REPORT", "/user/contacts/default/", strings.NewReader(body))
			req.Header.Set("Content-Type", "application/xml")
			req.Header.Set("Depth", "1")
			w := serveNoCall(t, req)
			if w.Code != http.StatusBadRequest {
				t.Errorf("status = %d, want 400 Bad Request", w.Code)
			}
			if ct := w.Header().Get("Content-Type"); strings.Contains(ct, "xml") {
				t.Errorf("Content-Type = %q, want no DAV:error; body:\n%s", ct, w.Body.String())
			}
		})
	}
}
