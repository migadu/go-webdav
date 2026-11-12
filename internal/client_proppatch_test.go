package internal

import (
	"context"
	"encoding/xml"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// The properties a PROPPATCH request failed for are reported in the propstat
// elements of a 207 Multi-Status response (RFC 4918 section 9.2.1). The status
// of a propstat element which names none still fails the request, and its
// description is part of the error.
func TestClientPropPatchErrors(t *testing.T) {
	for _, tc := range []struct {
		name     string
		propstat string
		wantErr  []string
	}{
		{
			name:     "propstat without properties",
			propstat: `<d:propstat><d:prop/><d:status>HTTP/1.1 403 Forbidden</d:status></d:propstat>`,
			wantErr:  []string{"403 Forbidden"},
		},
		{
			name: "propstat with a description",
			propstat: `<d:propstat>
  <d:prop><d:displayname/></d:prop>
  <d:status>HTTP/1.1 409 Conflict</d:status>
  <d:responsedescription>Name already used</d:responsedescription>
</d:propstat>`,
			wantErr: []string{"DAV: displayname", "409 Conflict", "Name already used"},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/xml; charset=utf-8")
				w.WriteHeader(http.StatusMultiStatus)
				io.WriteString(w, `<d:multistatus xmlns:d="DAV:"><d:response><d:href>/calendars/user/work/</d:href>`+
					tc.propstat+`</d:response></d:multistatus>`)
			}))
			defer ts.Close()
			c, err := NewClient(nil, ts.URL)
			if err != nil {
				t.Fatal(err)
			}

			update, err := NewPropertyUpdate([]interface{}{&DisplayName{Name: "Work"}}, []xml.Name{{Space: "http://example.com/ns/", Local: "color"}})
			if err != nil {
				t.Fatal(err)
			}
			err = c.PropPatch(context.Background(), "/calendars/user/work/", update)
			var httpErr *HTTPError
			if !errors.As(err, &httpErr) {
				t.Fatalf("PropPatch() = %v, want an HTTPError", err)
			}
			msg, i := err.Error(), 0
			for _, s := range tc.wantErr {
				j := strings.Index(msg[i:], s)
				if j < 0 {
					t.Fatalf("PropPatch() = %q, want %q after offset %v", msg, s, i)
				}
				i += j + len(s)
			}
		})
	}
}
