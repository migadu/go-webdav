package carddav

import (
	"bytes"
	"compress/gzip"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

var reportAddressData = `<?xml version="1.0" encoding="utf-8" ?>
<C:addressbook-multiget xmlns:D="DAV:" xmlns:C="urn:ietf:params:xml:ns:carddav">
  <D:prop>
    <D:getetag/>
    <C:address-data/>
  </D:prop>
  <D:href>/test/contacts/private/bob.vcf</D:href>
</C:addressbook-multiget>`

func TestReportCompressedBody(t *testing.T) {
	h := Handler{Backend: &testBackend{}}

	var buf bytes.Buffer
	zw := gzip.NewWriter(&buf)
	if _, err := io.WriteString(zw, reportAddressData); err != nil {
		t.Fatal(err)
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}

	req := httptest.NewRequest("REPORT", "/test/contacts/private/", &buf)
	req.Header.Set("Content-Type", "application/xml")
	req.Header.Set("Content-Encoding", "gzip")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)

	if w.Code != http.StatusMultiStatus {
		t.Fatalf("REPORT status = %d, want %d; body:\n%s", w.Code, http.StatusMultiStatus, w.Body)
	}
	if !strings.Contains(w.Body.String(), "<href>/test/contacts/private/bob.vcf</href>") {
		t.Errorf("REPORT response lacks requested href:\n%s", w.Body)
	}

	req = httptest.NewRequest("REPORT", "/test/contacts/private/", strings.NewReader(reportAddressData))
	req.Header.Set("Content-Type", "application/xml")
	req.Header.Set("Content-Encoding", "compress")
	w = httptest.NewRecorder()
	h.ServeHTTP(w, req)

	if w.Code != http.StatusUnsupportedMediaType {
		t.Fatalf("REPORT status = %d, want %d; body:\n%s", w.Code, http.StatusUnsupportedMediaType, w.Body)
	}

	// A highly compressible body decoding to 16 MiB must be rejected
	buf.Reset()
	zw = gzip.NewWriter(&buf)
	bomb := strings.Replace(reportAddressData, "<D:prop>", strings.Repeat(" ", 16<<20)+"<D:prop>", 1)
	if _, err := io.WriteString(zw, bomb); err != nil {
		t.Fatal(err)
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}

	req = httptest.NewRequest("REPORT", "/test/contacts/private/", &buf)
	req.Header.Set("Content-Type", "application/xml")
	req.Header.Set("Content-Encoding", "gzip")
	w = httptest.NewRecorder()
	h.ServeHTTP(w, req)

	if w.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("REPORT status = %d, want %d; body:\n%s", w.Code, http.StatusRequestEntityTooLarge, w.Body)
	}
}
