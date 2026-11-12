package internal

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/http/httptrace"
	"reflect"
	"strings"
	"testing"
)

// The responses to an extended MKCOL or a MKCALENDAR request, of RFC 4791
// section 5.3.1 and RFC 5689 section 3, and those servers send instead.
func TestClientDoCreateCollection(t *testing.T) {
	for _, tc := range []struct {
		name        string
		status      int
		contentType string
		body        string
		// wantCode is the code of the HTTPError errors.As finds, or 0 for
		// no error
		wantCode int
		// wantErr lists strings the error contains, in order
		wantErr []string
	}{
		{name: "201", status: http.StatusCreated},
		{
			// RFC 5689 section 3.4
			name:        "201 with a mkcol-response",
			status:      http.StatusCreated,
			contentType: "application/xml; charset=utf-8",
			body: `<?xml version="1.0" encoding="utf-8" ?>
<D:mkcol-response xmlns:D="DAV:">
  <D:propstat>
    <D:prop><D:resourcetype/><D:displayname/></D:prop>
    <D:status>HTTP/1.1 200 OK</D:status>
  </D:propstat>
</D:mkcol-response>`,
		},
		{
			// As SabreDAV reports a property it doesn't store
			name:        "207 with a refused property",
			status:      http.StatusMultiStatus,
			contentType: "application/xml; charset=utf-8",
			body: `<?xml version="1.0" encoding="utf-8"?>
<d:multistatus xmlns:d="DAV:" xmlns:x="http://example.com/ns/">
  <d:response>
    <d:href>/calendars/user/work/</d:href>
    <d:propstat>
      <d:prop><d:displayname/></d:prop>
      <d:status>HTTP/1.1 424 Failed Dependency</d:status>
    </d:propstat>
    <d:propstat>
      <d:prop><x:color/></d:prop>
      <d:status>HTTP/1.1 403 Forbidden</d:status>
    </d:propstat>
  </d:response>
</d:multistatus>`,
			wantCode: http.StatusForbidden,
			wantErr:  []string{"http://example.com/ns/ color", "403 Forbidden", "DAV: displayname", "424 Failed Dependency"},
		},
		{
			name:        "207 with a failed response",
			status:      http.StatusMultiStatus,
			contentType: "application/xml",
			body: `<d:multistatus xmlns:d="DAV:">
  <d:response><d:href>/calendars/user/work/</d:href><d:status>HTTP/1.1 507 Insufficient Storage</d:status></d:response>
</d:multistatus>`,
			wantCode: http.StatusInsufficientStorage,
			wantErr:  []string{"/calendars/user/work/", "507"},
		},
		{
			// The status of a propstat element applies to its properties:
			// without any, it's still the status of the request
			name:        "207 with a failed propstat without properties",
			status:      http.StatusMultiStatus,
			contentType: "application/xml",
			body: `<d:multistatus xmlns:d="DAV:">
  <d:response><d:href>/calendars/user/work/</d:href>
    <d:propstat><d:prop/><d:status>HTTP/1.1 507 Insufficient Storage</d:status></d:propstat>
  </d:response>
</d:multistatus>`,
			wantCode: http.StatusInsufficientStorage,
			wantErr:  []string{"507 Insufficient Storage"},
		},
		{
			name:        "207 without failures",
			status:      http.StatusMultiStatus,
			contentType: "application/xml",
			body: `<d:multistatus xmlns:d="DAV:">
  <d:response><d:href>/calendars/user/work/</d:href>
    <d:propstat><d:prop><d:displayname/></d:prop><d:status>HTTP/1.1 200 OK</d:status></d:propstat>
  </d:response>
</d:multistatus>`,
		},
		{
			// RFC 5689 section 3.5
			name:        "403 with a mkcol-response",
			status:      http.StatusForbidden,
			contentType: "application/xml; charset=utf-8",
			body: `<?xml version="1.0" encoding="utf-8" ?>
<D:mkcol-response xmlns:D="DAV:" xmlns:C="urn:ietf:params:xml:ns:caldav">
  <D:propstat>
    <D:prop><D:resourcetype/></D:prop>
    <D:status>HTTP/1.1 409 Conflict</D:status>
    <D:responsedescription>
      <D:error><D:valid-resourcetype /></D:error>
      Resource type is not supported by this server
    </D:responsedescription>
  </D:propstat>
  <D:propstat>
    <D:prop><D:displayname/></D:prop>
    <D:status>HTTP/1.1 424 Failed Dependency</D:status>
  </D:propstat>
</D:mkcol-response>`,
			wantCode: http.StatusForbidden,
			wantErr:  []string{"403 Forbidden", "DAV: resourcetype", "409 Conflict", "DAV: displayname", "424 Failed Dependency"},
		},
		{
			// The example of RFC 5689 section 3.5: the description of a
			// propstat element is part of the error, as for a response
			// element
			name:        "403 with the mkcol-response of RFC 5689",
			status:      http.StatusForbidden,
			contentType: "application/xml; charset=utf-8",
			body: `<?xml version="1.0" encoding="utf-8" ?>
<D:mkcol-response xmlns:D="DAV:">
  <D:propstat>
    <D:prop>
      <D:resourcetype/>
    </D:prop>
    <D:status>HTTP/1.1 403 Forbidden</D:status>
    <D:error><D:valid-resourcetype /></D:error>
    <D:responsedescription>Resource type is not
    supported by this server</D:responsedescription>
  </D:propstat>
  <D:propstat>
    <D:prop>
      <D:displayname/>
    </D:prop>
    <D:status>HTTP/1.1 424 Failed Dependency</D:status>
  </D:propstat>
</D:mkcol-response>`,
			wantCode: http.StatusForbidden,
			wantErr:  []string{"DAV: resourcetype", "403 Forbidden", "Resource type is not", "valid-resourcetype", "DAV: displayname", "424 Failed Dependency"},
		},
		{
			name:        "403 with a mkcol-response without properties",
			status:      http.StatusForbidden,
			contentType: "application/xml",
			body: `<D:mkcol-response xmlns:D="DAV:">
  <D:propstat>
    <D:prop/>
    <D:status>HTTP/1.1 403 Forbidden</D:status>
    <D:responsedescription>Quota exceeded</D:responsedescription>
  </D:propstat>
</D:mkcol-response>`,
			wantCode: http.StatusForbidden,
			wantErr:  []string{"403 Forbidden", "Quota exceeded"},
		},
		{
			// RFC 4791 section 5.3.1.1
			name:        "403 with a precondition",
			status:      http.StatusForbidden,
			contentType: "application/xml",
			body: `<?xml version="1.0" encoding="utf-8" ?>
<D:error xmlns:D="DAV:" xmlns:C="urn:ietf:params:xml:ns:caldav"><C:calendar-collection-location-ok/></D:error>`,
			wantCode: http.StatusForbidden,
			wantErr:  []string{"403 Forbidden", "calendar-collection-location-ok"},
		},
		{
			name:        "405 with text",
			status:      http.StatusMethodNotAllowed,
			contentType: "text/plain",
			body:        "The resource you tried to create already exists",
			wantCode:    http.StatusMethodNotAllowed,
			wantErr:     []string{"405 Method Not Allowed", "already exists"},
		},
		{name: "409 without a body", status: http.StatusConflict, wantCode: http.StatusConflict},
		{
			name:        "malformed XML",
			status:      http.StatusForbidden,
			contentType: "application/xml",
			body:        `<D:mkcol-response xmlns:D="DAV:"><D:propstat>`,
			wantCode:    http.StatusForbidden,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var method string
			ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				method = r.Method
				if tc.contentType != "" {
					w.Header().Set("Content-Type", tc.contentType)
				}
				w.WriteHeader(tc.status)
				io.WriteString(w, tc.body)
			}))
			defer ts.Close()
			c, err := NewClient(nil, ts.URL)
			if err != nil {
				t.Fatal(err)
			}

			req, err := c.NewRequest("MKCALENDAR", "/calendars/user/work/", nil)
			if err != nil {
				t.Fatal(err)
			}
			err = c.DoCreateCollection(req.WithContext(context.Background()))
			if method != "MKCALENDAR" {
				t.Errorf("method = %q, want MKCALENDAR", method)
			}

			if tc.wantCode == 0 {
				if err != nil {
					t.Errorf("DoCreateCollection() = %v, want nil", err)
				}
				return
			}
			var httpErr *HTTPError
			if !errors.As(err, &httpErr) || httpErr.Code != tc.wantCode {
				t.Fatalf("DoCreateCollection() = %v, want an HTTPError %v", err, tc.wantCode)
			}
			msg, i := err.Error(), 0
			for _, s := range tc.wantErr {
				j := strings.Index(msg[i:], s)
				if j < 0 {
					t.Fatalf("DoCreateCollection() = %q, want %q after offset %v", msg, s, i)
				}
				i += j + len(s)
			}
		})
	}
}

// The body of a 201 Created response, such as the DAV:mkcol-response of RFC
// 5689 section 3.4, is read before it's closed, so that the connection can be
// reused.
func TestClientDoCreateCollectionReusesConnection(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/xml; charset=utf-8")
		w.WriteHeader(http.StatusCreated)
		io.WriteString(w, `<?xml version="1.0" encoding="utf-8" ?>
<D:mkcol-response xmlns:D="DAV:">
  <D:propstat>
    <D:prop><D:resourcetype/><D:displayname/></D:prop>
    <D:status>HTTP/1.1 200 OK</D:status>
  </D:propstat>
</D:mkcol-response>`)
	}))
	defer ts.Close()
	c, err := NewClient(ts.Client(), ts.URL)
	if err != nil {
		t.Fatal(err)
	}

	var reused []bool
	ctx := httptrace.WithClientTrace(context.Background(), &httptrace.ClientTrace{
		GotConn: func(info httptrace.GotConnInfo) {
			reused = append(reused, info.Reused)
		},
	})
	for i := 0; i < 2; i++ {
		req, err := c.NewRequest("MKCOL", "/calendars/user/work/", nil)
		if err != nil {
			t.Fatal(err)
		}
		if err := c.DoCreateCollection(req.WithContext(ctx)); err != nil {
			t.Fatalf("DoCreateCollection() = %v", err)
		}
	}
	if !reflect.DeepEqual(reused, []bool{false, true}) {
		t.Errorf("connections reused: %v, want [false true]", reused)
	}
}
