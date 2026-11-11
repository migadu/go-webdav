package carddav

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestGetAddressObjectETag(t *testing.T) {
	for _, tc := range []struct {
		header, want string
	}{
		{`"abc"`, "abc"},
		{`"a\b"`, `a\b`},
		// Out of spec, but sent by some servers
		{`abc`, "abc"},
	} {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "text/vcard")
			w.Header().Set("ETag", tc.header)
			w.Write([]byte(aliceData))
		}))

		c, err := NewClient(nil, srv.URL)
		if err != nil {
			t.Fatal(err)
		}
		ao, err := c.GetAddressObject(context.Background(), "/contacts/alice.vcf")
		srv.Close()
		if err != nil {
			t.Errorf("ETag: %s: GetAddressObject() = %v", tc.header, err)
			continue
		}
		if ao.ETag != tc.want {
			t.Errorf("ETag: %s: got %q, want %q", tc.header, ao.ETag, tc.want)
		}
	}
}
