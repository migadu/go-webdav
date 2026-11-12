package carddav

import (
	"bytes"
	"context"
	"encoding/xml"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"

	"github.com/emersion/go-vcard"
)

const addressDataCard = "BEGIN:VCARD\r\n" +
	"VERSION:4.0\r\n" +
	"UID:uid-address-data\r\n" +
	"FN:Address Data\r\n" +
	"item1.TEL;TYPE=home:+10000000001\r\n" +
	"TEL;TYPE=cell:+10000000002\r\n" +
	"EMAIL:address-data@example.com\r\n" +
	"NOTE:not requested\r\n" +
	"END:VCARD\r\n"

// newAddressDataBackend returns a backend with a single address object made
// of addressDataCard. Like many backends, it ignores the address-data
// requested and returns whole vCards.
func newAddressDataBackend(t *testing.T) (*syncTestBackend, string) {
	t.Helper()
	card, err := vcard.NewDecoder(strings.NewReader(addressDataCard)).Decode()
	if err != nil {
		t.Fatal(err)
	}
	ao := AddressObject{Path: syncAddressBookPath + "/address-data.vcf", ETag: "1", Card: card}
	b := newSyncTestBackend()
	b.objects = []AddressObject{ao}
	return b, ao.Path
}

// RFC 6352 section 10.4: "When used in an address book REPORT request, the
// CARDDAV:address-data XML element specifies which parts of address object
// resources need to be returned in the response." Section 10.4.2: "The
// "novalue" attribute can be used by clients to request that the actual
// value of the property not be returned (if the "novalue" attribute is set to
// "yes"). In that case, the server will return just the vCard property name
// and any vCard parameters and a trailing ":" without the subsequent value
// data."
//
// The Handler applies it to every REPORT, whatever the backend returns.
func TestHandlerAddressDataProps(t *testing.T) {
	const addressData = `<C:address-data>
      <C:prop name="FN"/>
      <C:prop name="TEL" novalue="yes"/>
    </C:address-data>`
	want := []string{
		"BEGIN:VCARD",
		"VERSION:4.0",
		"FN:Address Data",
		"item1.TEL;TYPE=home:",
		"TEL;TYPE=cell:",
		"END:VCARD",
	}

	b, objPath := newAddressDataBackend(t)
	h := &Handler{Backend: b}

	for _, tc := range []struct {
		name, body string
	}{
		{"addressbook-query", `<?xml version="1.0" encoding="utf-8"?>
<C:addressbook-query xmlns:D="DAV:" xmlns:C="urn:ietf:params:xml:ns:carddav">
  <D:prop>` + addressData + `</D:prop>
  <C:filter/>
</C:addressbook-query>`},
		{"addressbook-multiget", `<?xml version="1.0" encoding="utf-8"?>
<C:addressbook-multiget xmlns:D="DAV:" xmlns:C="urn:ietf:params:xml:ns:carddav">
  <D:prop>` + addressData + `</D:prop>
  <D:href>` + objPath + `</D:href>
</C:addressbook-multiget>`},
		{"sync-collection", `<?xml version="1.0" encoding="utf-8"?>
<D:sync-collection xmlns:D="DAV:" xmlns:C="urn:ietf:params:xml:ns:carddav">
  <D:sync-token/>
  <D:sync-level>1</D:sync-level>
  <D:prop>` + addressData + `</D:prop>
</D:sync-collection>`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			res := serveTestRequest(t, h, "REPORT", syncAddressBookPath, tc.body)
			resp := responsesByPath(t, decodeTestMultiStatus(t, res))[objPath]
			if resp == nil {
				t.Fatalf("no response for %v", objPath)
			}
			var data addressDataResp
			if err := resp.DecodeProp(&data); err != nil {
				t.Fatalf("address-data: %v", err)
			}
			got := strings.Split(strings.TrimRight(strings.ReplaceAll(string(data.Data), "\r\n", "\n"), "\n"), "\n")
			if strings.Join(got, "\n") != strings.Join(want, "\n") {
				t.Errorf("address-data =\n%v\nwant:\n%v", strings.Join(got, "\n"), strings.Join(want, "\n"))
			}
		})
	}

	t.Run("invalid novalue", func(t *testing.T) {
		res := serveTestRequest(t, h, "REPORT", syncAddressBookPath, `<?xml version="1.0" encoding="utf-8"?>
<C:addressbook-multiget xmlns:D="DAV:" xmlns:C="urn:ietf:params:xml:ns:carddav">
  <D:prop><C:address-data><C:prop name="TEL" novalue="maybe"/></C:address-data></D:prop>
  <D:href>`+objPath+`</D:href>
</C:addressbook-multiget>`)
		res.Body.Close()
		if res.StatusCode != http.StatusBadRequest {
			t.Errorf("got status %v, want 400 Bad Request", res.Status)
		}
	})

	// <!ATTLIST prop name CDATA #REQUIRED novalue (yes | no) "no"> (RFC
	// 6352 section 10.4.2)
	for _, p := range []string{`<C:prop/>`, `<C:prop name="" novalue="yes"/>`} {
		t.Run("prop without a name: "+p, func(t *testing.T) {
			res := serveTestRequest(t, h, "REPORT", syncAddressBookPath, `<?xml version="1.0" encoding="utf-8"?>
<C:addressbook-multiget xmlns:D="DAV:" xmlns:C="urn:ietf:params:xml:ns:carddav">
  <D:prop><C:address-data><C:prop name="FN"/>`+p+`</C:address-data></D:prop>
  <D:href>`+objPath+`</D:href>
</C:addressbook-multiget>`)
			res.Body.Close()
			if res.StatusCode != http.StatusBadRequest {
				t.Errorf("got status %v, want 400 Bad Request", res.Status)
			}
		})
	}
}

// The client never sends a prop element without a name, which the server
// would refuse.
func TestEncodeAddressDataEmptyName(t *testing.T) {
	if _, err := encodeAddressPropReq(&AddressDataRequest{Props: []string{"FN", ""}}); err == nil {
		t.Errorf("encodeAddressPropReq() with an empty name succeeded")
	}
}

func TestDecodeAddressDataNoValue(t *testing.T) {
	for _, tc := range []struct {
		name, props string
		want        AddressDataRequest
	}{
		{"novalue", `<C:prop name="FN" novalue="no"/><C:prop name="TEL" novalue="yes"/>`,
			AddressDataRequest{Props: []string{"FN", "TEL"}, NoValue: []string{"TEL"}}},
		// A property requested both ways is returned with its value
		{"also requested with its value", `<C:prop name="TEL" novalue="yes"/><C:prop name="tel"/>`,
			AddressDataRequest{Props: []string{"TEL", "tel"}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var el addressDataReq
			body := `<C:address-data xmlns:C="urn:ietf:params:xml:ns:carddav">` + tc.props + `</C:address-data>`
			if err := xml.Unmarshal([]byte(body), &el); err != nil {
				t.Fatal(err)
			}
			got, err := decodeAddressDataReq(&el)
			if err != nil {
				t.Fatalf("decodeAddressDataReq() = %v", err)
			}
			if !reflect.DeepEqual(*got, tc.want) {
				t.Errorf("decodeAddressDataReq() = %+v, want %+v", *got, tc.want)
			}
		})
	}
}

// A field matched by a name of Props that isn't in NoValue keeps its value,
// and filtering again changes nothing: the Handler filters the address data
// of backends which already did with Filter.
func TestFilterPropertiesNoValue(t *testing.T) {
	ao := decodeGroupedCard(t)
	req := AddressDataRequest{Props: []string{"TEL", "item1.TEL"}, NoValue: []string{"tel"}}
	want := "BEGIN:VCARD\r\n" +
		"VERSION:4.0\r\n" +
		"item1.TEL:+10000000001\r\n" +
		"item2.TEL:\r\n" +
		"TEL:\r\n" +
		"END:VCARD\r\n"

	got := filterProperties(req, ao)
	for i := 0; i < 2; i++ {
		var buf bytes.Buffer
		if err := vcard.NewEncoder(&buf).Encode(got.Card); err != nil {
			t.Fatalf("Encode() = %v", err)
		}
		if buf.String() != want {
			t.Errorf("filterProperties() =\n%v\nwant:\n%v", buf.String(), want)
		}
		got = filterProperties(req, got)
	}

	if v := ao.Card[vcard.FieldTelephone][2].Value; v != "+10000000003" {
		t.Errorf("filterProperties() changed the original card: TEL = %q", v)
	}
}

// addressDataReqBackend records the address-data request of the last
// GetAddressObject call.
type addressDataReqBackend struct {
	*syncTestBackend
	req AddressDataRequest
}

func (b *addressDataReqBackend) GetAddressObject(ctx context.Context, path string, req *AddressDataRequest) (*AddressObject, error) {
	b.req = *req
	return b.syncTestBackend.GetAddressObject(ctx, path, req)
}

// The client sends novalue="yes" for the names in NoValue.
func TestClientAddressDataNoValue(t *testing.T) {
	sb, objPath := newAddressDataBackend(t)
	b := &addressDataReqBackend{syncTestBackend: sb}
	ts := httptest.NewServer(&Handler{Backend: b})
	defer ts.Close()

	c, err := NewClient(ts.Client(), ts.URL)
	if err != nil {
		t.Fatal(err)
	}
	dataReq := AddressDataRequest{Props: []string{"FN", "TEL"}, NoValue: []string{"TEL"}}
	aos, err := c.MultiGetAddressBook(context.Background(), syncAddressBookPath, &AddressBookMultiGet{
		Paths:       []string{objPath},
		DataRequest: dataReq,
	})
	if err != nil {
		t.Fatalf("MultiGetAddressBook() = %v", err)
	}
	if !reflect.DeepEqual(b.req, dataReq) {
		t.Errorf("backend got DataRequest %+v, want %+v", b.req, dataReq)
	}
	if len(aos) != 1 {
		t.Fatalf("got %d address objects, want 1", len(aos))
	}
	card := aos[0].Card
	if v := card.Value(vcard.FieldFormattedName); v != "Address Data" {
		t.Errorf("FN = %q, want %q", v, "Address Data")
	}
	tels := card[vcard.FieldTelephone]
	if len(tels) != 2 {
		t.Fatalf("got %d TEL, want 2", len(tels))
	}
	for _, tel := range tels {
		if tel.Value != "" || len(tel.Params.Types()) != 1 {
			t.Errorf("TEL = %+v, want its TYPE but no value", tel)
		}
	}
	if f := card.Get(vcard.FieldEmail); f != nil {
		t.Errorf("EMAIL = %+v, want none", f)
	}
}
