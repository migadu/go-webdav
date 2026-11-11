package carddav

import (
	"bytes"
	"context"
	"encoding/xml"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/emersion/go-vcard"
	"github.com/emersion/go-webdav"
	"github.com/emersion/go-webdav/internal"
)

// streamTimeout bounds the wait for a response in the streaming tests, so
// that they fail rather than hang if responses aren't streamed.
const streamTimeout = 10 * time.Second

// newLargeAddressObject returns an address object whose address data is
// larger than the buffers of net/http (a few KiB), so that writing its
// response to an http.ResponseWriter sends it to the client.
func newLargeAddressObject(path, uid string) *AddressObject {
	card := make(vcard.Card)
	card.SetValue(vcard.FieldVersion, "4.0")
	card.SetValue(vcard.FieldUID, uid)
	card.SetValue(vcard.FieldFormattedName, "Gopher "+uid)
	card.SetValue(vcard.FieldNote, strings.Repeat("Streaming multistatus. ", 1500))
	return &AddressObject{Path: path, ETag: uid, Card: card}
}

// streamBackend serves large address objects, and blocks GetAddressObject
// for the path block until release is closed.
type streamBackend struct {
	testBackend
	objects map[string]*AddressObject
	block   string
	release <-chan struct{}
}

func (b *streamBackend) GetAddressObject(ctx context.Context, path string, req *AddressDataRequest) (*AddressObject, error) {
	if path == b.block {
		select {
		case <-b.release:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	ao, ok := b.objects[path]
	if !ok {
		return nil, webdav.NewHTTPError(http.StatusNotFound, nil)
	}
	return ao, nil
}

// readUntil reads from r until what it read contains marker.
func readUntil(r io.Reader, marker string) ([]byte, error) {
	var b []byte
	buf := make([]byte, 512)
	for {
		n, err := r.Read(buf)
		b = append(b, buf[:n]...)
		if bytes.Contains(b, []byte(marker)) {
			return b, nil
		} else if err != nil {
			return b, err
		}
	}
}

// The response for each address object of an addressbook-multiget is sent to
// the client as soon as it's built, before the next object is fetched.
func TestMultiGetStreaming(t *testing.T) {
	const abPath = "/user/contacts/default/"
	release := make(chan struct{})
	var releaseOnce sync.Once
	unblock := func() { releaseOnce.Do(func() { close(release) }) }

	backend := &streamBackend{
		objects: map[string]*AddressObject{
			abPath + "1.vcf": newLargeAddressObject(abPath+"1.vcf", "uid-1"),
			abPath + "2.vcf": newLargeAddressObject(abPath+"2.vcf", "uid-2"),
		},
		block:   abPath + "2.vcf",
		release: release,
	}
	srv := httptest.NewServer(&Handler{Backend: backend})
	defer srv.Close()
	defer unblock()

	ctx, cancel := context.WithTimeout(context.Background(), streamTimeout)
	defer cancel()
	body := `<?xml version="1.0" encoding="UTF-8"?>
<C:addressbook-multiget xmlns:D="DAV:" xmlns:C="urn:ietf:params:xml:ns:carddav">
  <D:prop><D:getetag/><C:address-data/></D:prop>
  <D:href>` + abPath + `1.vcf</D:href>
  <D:href>` + abPath + `2.vcf</D:href>
</C:addressbook-multiget>`
	req, err := http.NewRequestWithContext(ctx, "REPORT", srv.URL+abPath, strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/xml")
	req.Header.Set("Depth", "1")
	resp, err := srv.Client().Do(req)
	if err != nil {
		t.Fatalf("no response while the second address object is being fetched: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusMultiStatus {
		t.Fatalf("REPORT returned %v, want 207", resp.Status)
	}

	first, err := readUntil(resp.Body, abPath+"1.vcf</href>")
	if err != nil {
		t.Fatalf("response for the first address object not received while the second one is being fetched: %v", err)
	}
	unblock()
	rest, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("failed to read the rest of the response: %v", err)
	}

	var ms internal.MultiStatus
	if err := xml.Unmarshal(append(first, rest...), &ms); err != nil {
		t.Fatal(err)
	}
	if len(ms.Responses) != 2 {
		t.Fatalf("got %d responses, want 2", len(ms.Responses))
	}
	for i, uid := range []string{"uid-1", "uid-2"} {
		resp := &ms.Responses[i]
		var data addressDataResp
		if err := resp.DecodeProp(&data); err != nil {
			t.Errorf("response %d: %v", i, err)
		} else if !bytes.Contains(data.Data, []byte("UID:"+uid)) {
			t.Errorf("response %d doesn't hold the address data of %s", i, uid)
		}
	}
}

// listErrorBackend fails to list the address objects.
type listErrorBackend struct {
	testBackend
}

func (b *listErrorBackend) ListAddressObjects(ctx context.Context, path string, req *AddressDataRequest) ([]AddressObject, error) {
	return nil, webdav.NewHTTPError(http.StatusForbidden, errors.New("no listing for you"))
}

// The members of an address book are listed before the response for the
// address book is written, so that an error listing them is still served as
// the status of the request.
func TestPropFindListErrorStatus(t *testing.T) {
	const abPath = "/user/contacts/default/"
	handler := Handler{Backend: &listErrorBackend{}}

	body := `<?xml version="1.0" encoding="UTF-8"?>
<D:propfind xmlns:D="DAV:"><D:prop><D:resourcetype/></D:prop></D:propfind>`
	req := httptest.NewRequest("PROPFIND", abPath, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/xml")
	req.Header.Set("Depth", "1")
	ctx := context.WithValue(req.Context(), addressBookPathKey, abPath)
	req = req.WithContext(ctx)
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	if w.Code != http.StatusForbidden {
		t.Errorf("PROPFIND returned %v, want 403: %s", w.Code, w.Body.String())
	}
	if strings.Contains(w.Body.String(), "multistatus") {
		t.Errorf("PROPFIND returned a multistatus along with its error: %s", w.Body.String())
	}
}
