package carddav

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/emersion/go-vcard"
)

// getAddressObjectWith runs GetAddressObject against a server answering with
// the given Content-Type and body. An empty contentType sends no Content-Type
// at all.
func getAddressObjectWith(t *testing.T, contentType, body string) (*AddressObject, error) {
	t.Helper()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if contentType == "" {
			// Stop net/http from sniffing one
			w.Header()["Content-Type"] = nil
		} else {
			w.Header().Set("Content-Type", contentType)
		}
		w.Write([]byte(body))
	}))
	defer srv.Close()

	c, err := NewClient(nil, srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	return c.GetAddressObject(context.Background(), "/contacts/alice.vcf")
}

func TestGetAddressObjectContentType(t *testing.T) {
	for _, contentType := range []string{
		"text/vcard",
		"text/vcard; charset=utf-8",
		"Text/VCard",
		// Legacy media type for vCard 2.1 and 3.0
		"text/x-vcard",
		// Out of spec, but sent by some servers such as iCloud
		"text/plain",
		"text/plain; charset=utf-8",
		"",
	} {
		ao, err := getAddressObjectWith(t, contentType, aliceData)
		if err != nil {
			t.Errorf("Content-Type %q: GetAddressObject() = %v", contentType, err)
			continue
		}
		if got, want := ao.Card.PreferredValue(vcard.FieldFormattedName), "Alice Gopher"; got != want {
			t.Errorf("Content-Type %q: got FN %q, want %q", contentType, got, want)
		}
		if got, want := ao.Path, "/contacts/alice.vcf"; got != want {
			t.Errorf("Content-Type %q: got path %q, want %q", contentType, got, want)
		}
	}
}

func TestGetAddressObjectContentTypeMismatch(t *testing.T) {
	for _, contentType := range []string{
		"text/html",
		"text/html; charset=utf-8",
		"application/json",
	} {
		_, err := getAddressObjectWith(t, contentType, aliceData)
		if err == nil {
			t.Errorf("Content-Type %q: GetAddressObject() succeeded, want an error", contentType)
			continue
		}
		if want := `carddav: expected Content-Type "text/vcard"`; !strings.Contains(err.Error(), want) {
			t.Errorf("Content-Type %q: GetAddressObject() = %q, want an error containing %q", contentType, err, want)
		}
	}
}

func TestGetAddressObjectNotVCard(t *testing.T) {
	for _, tc := range []struct {
		contentType, body string
	}{
		{"text/plain", "Service temporarily unavailable\n"},
		{"text/plain", "Error: no such contact\n"},
		{"text/plain", ""},
		{"", ""},
		{"text/vcard", "BEGIN:VCARD\nVERSION:4.0\nFN:Truncated\n"},
	} {
		_, err := getAddressObjectWith(t, tc.contentType, tc.body)
		if err == nil {
			t.Errorf("Content-Type %q, body %q: GetAddressObject() succeeded, want an error", tc.contentType, tc.body)
			continue
		}
		// The error must say what was expected and what the server sent
		for _, want := range []string{"carddav: ", "vCard", `"` + tc.contentType + `"`} {
			if !strings.Contains(err.Error(), want) {
				t.Errorf("Content-Type %q, body %q: GetAddressObject() = %q, want an error containing %q", tc.contentType, tc.body, err, want)
			}
		}
	}
}
