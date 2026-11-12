package carddav

import (
	"context"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
)

// An is-not-defined element with siblings, a param-filter with more than one
// text-match, or a prop-filter or param-filter without a name breaks the
// grammar of RFC 6352 sections 10.5.1 and 10.5.2. Unlike CalDAV, CardDAV
// defines no valid-filter precondition for addressbook-query (section 8.6):
// CARDDAV:supported-filter is about properties and parameters the server
// can't query, so the request is a plain 400 Bad Request, without a DAV:error
// element.
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
		{
			name:   "param-filter with two text-matches",
			filter: `<C:prop-filter name="TEL"><C:param-filter name="TYPE"><C:text-match>work</C:text-match><C:text-match>home</C:text-match></C:param-filter></C:prop-filter>`,
		},
		{
			name:   "prop-filter without a name",
			filter: `<C:prop-filter><C:text-match>Alice</C:text-match></C:prop-filter>`,
		},
		{
			name:   "param-filter without a name",
			filter: `<C:prop-filter name="TEL"><C:param-filter><C:is-not-defined/></C:param-filter></C:prop-filter>`,
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

// The client refuses, without sending it, a prop-filter or param-filter
// without a name, which RFC 6352 sections 10.5.1 and 10.5.2 require.
func TestQueryAddressBookFilterWithoutName(t *testing.T) {
	var sent bool
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sent = true
		http.Error(w, "unexpected request", http.StatusBadRequest)
	}))
	defer ts.Close()

	c, err := NewClient(nil, ts.URL)
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name string
		pf   PropFilter
	}{
		{"prop-filter", PropFilter{TextMatches: []TextMatch{{Text: "Alice"}}}},
		{"param-filter", PropFilter{Name: "TEL", Params: []ParamFilter{{IsNotDefined: true}}}},
	} {
		sent = false
		_, err := c.QueryAddressBook(context.Background(), "/user/contacts/default/", &AddressBookQuery{
			PropFilters: []PropFilter{tc.pf},
		})
		if err == nil {
			t.Errorf("%v: QueryAddressBook() succeeded", tc.name)
		}
		if sent {
			t.Errorf("%v: QueryAddressBook() sent the request, and got: %v", tc.name, err)
		}
	}
}

// RFC 6352 section 10.3 requires a filter in addressbook-query, but some
// clients leave it out to list every address object. Without a valid-filter
// precondition to report, the server reads a missing filter as an empty one,
// which matches every address object (section 10.5).
func TestAddressBookQueryWithoutFilter(t *testing.T) {
	const abPath = "/user/contacts/default/"
	handler := &Handler{Backend: &filterBackend{objects: []AddressObject{
		*newLargeAddressObject(abPath+"1.vcf", "uid-1"),
		*newLargeAddressObject(abPath+"2.vcf", "uid-2"),
	}}}
	body := `<?xml version="1.0" encoding="UTF-8"?>
<C:addressbook-query xmlns:D="DAV:" xmlns:C="urn:ietf:params:xml:ns:carddav">
  <D:prop><D:getetag/></D:prop>
</C:addressbook-query>`
	req := httptest.NewRequest("REPORT", abPath, strings.NewReader(body))
	req.Header.Set("Depth", "1")
	want := []string{abPath + "1.vcf propstat", abPath + "2.vcf propstat"}
	if got := statusResponses(t, handler, req); !reflect.DeepEqual(got, want) {
		t.Errorf("responses = %q, want %q", got, want)
	}
}
