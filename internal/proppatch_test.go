package internal

import (
	"encoding/xml"
	"fmt"
	"net/http"
	"reflect"
	"testing"
)

// patchRecorder is a PropPatchFunc which records the instructions it is
// called for, and fails those listed in errs.
type patchRecorder struct {
	calls []string
	errs  map[string]int
}

func (rec *patchRecorder) patch(raw *RawXMLValue, remove bool) error {
	name, _ := raw.XMLName()
	call := "set " + name.Local
	if remove {
		call = "remove " + name.Local
	}
	rec.calls = append(rec.calls, call)
	if code, ok := rec.errs[call]; ok {
		return &HTTPError{Code: code}
	}
	return nil
}

// propStatuses lists the properties of resp along with their status codes.
func propStatuses(t *testing.T, resp *Response) []string {
	t.Helper()

	b, err := xml.Marshal(resp)
	if err != nil {
		t.Fatal(err)
	}
	var decoded Response
	if err := xml.Unmarshal(b, &decoded); err != nil {
		t.Fatal(err)
	}
	var l []string
	for _, propstat := range decoded.PropStats {
		for _, raw := range propstat.Prop.Raw {
			name, _ := raw.XMLName()
			l = append(l, fmt.Sprintf("%v %v", name.Local, propstat.Status.Code))
		}
	}
	return l
}

func patchProps(t *testing.T, body string, rec *patchRecorder) []string {
	t.Helper()

	var update PropertyUpdate
	if err := xml.Unmarshal([]byte(body), &update); err != nil {
		t.Fatal(err)
	}
	resp, err := PatchProps("/dir/", &update, rec.patch, func() error { return nil })
	if err != nil {
		t.Fatalf("PatchProps() = %v", err)
	}
	return propStatuses(t, resp)
}

// RFC 4918 section 9.2: servers must process PROPPATCH instructions in
// document order.
func TestPatchPropsDocumentOrder(t *testing.T) {
	rec := &patchRecorder{}
	got := patchProps(t, `<?xml version="1.0" encoding="UTF-8"?>
<D:propertyupdate xmlns:D="DAV:" xmlns:X="http://example.com/ns/">
  <D:set><D:prop><D:displayname>Friends</D:displayname></D:prop></D:set>
  <D:remove><D:prop><D:displayname/></D:prop></D:remove>
  <D:set><D:prop><X:color>red</X:color></D:prop></D:set>
  <D:remove><D:prop><X:order/></D:prop></D:remove>
</D:propertyupdate>`, rec)

	wantCalls := []string{"set displayname", "remove displayname", "set color", "remove order"}
	if !reflect.DeepEqual(rec.calls, wantCalls) {
		t.Errorf("calls = %q, want %q", rec.calls, wantCalls)
	}
	want := []string{"displayname 200", "color 200", "order 200"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("propstats = %q, want %q", got, want)
	}
}

// A property named by several instructions is reported once, with the status
// of its failed instruction.
func TestPatchPropsSamePropertyFailed(t *testing.T) {
	rec := &patchRecorder{errs: map[string]int{"remove displayname": http.StatusForbidden}}
	got := patchProps(t, `<?xml version="1.0" encoding="UTF-8"?>
<D:propertyupdate xmlns:D="DAV:" xmlns:X="http://example.com/ns/">
  <D:set><D:prop><D:displayname>Friends</D:displayname></D:prop></D:set>
  <D:remove><D:prop><D:displayname/></D:prop></D:remove>
  <D:set><D:prop><D:displayname>Family</D:displayname><X:color>red</X:color></D:prop></D:set>
</D:propertyupdate>`, rec)

	want := []string{"displayname 403", "color 424"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("propstats = %q, want %q", got, want)
	}
}

// Updates built by hand carry no document order: removals come first.
func TestPatchPropsHandBuilt(t *testing.T) {
	color := xml.Name{Space: "http://example.com/ns/", Local: "color"}
	update := PropertyUpdate{
		Set:    []Set{{Prop: Prop{Raw: []RawXMLValue{*NewRawXMLElement(DisplayNameName, nil, nil)}}}},
		Remove: []Remove{{Prop: Prop{Raw: []RawXMLValue{*NewRawXMLElement(color, nil, nil)}}}},
	}
	rec := &patchRecorder{}
	if _, err := PatchProps("/dir/", &update, rec.patch, func() error { return nil }); err != nil {
		t.Fatalf("PatchProps() = %v", err)
	}
	wantCalls := []string{"remove color", "set displayname"}
	if !reflect.DeepEqual(rec.calls, wantCalls) {
		t.Errorf("calls = %q, want %q", rec.calls, wantCalls)
	}
}
