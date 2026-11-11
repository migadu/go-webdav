package internal

import (
	"encoding/xml"
	"testing"
)

// An element decoded from a document is encoded into another standalone
// document, and back, with its namespaces declared once.
func TestMarshalRawXML(t *testing.T) {
	for _, tc := range []struct {
		doc, want string
	}{
		{
			`<D:lockinfo xmlns:D="DAV:"><D:owner><D:href>x</D:href></D:owner></D:lockinfo>`,
			`<owner xmlns="DAV:"><href xmlns="DAV:">x</href></owner>`,
		},
		{
			`<lockinfo xmlns="DAV:"><owner xmlns:x="urn:x"><x:name a="b">alice</x:name></owner></lockinfo>`,
			`<owner xmlns="DAV:"><name xmlns="urn:x" a="b">alice</name></owner>`,
		},
		{
			`<lockinfo xmlns="DAV:"><owner><name xmlns="urn:y">bob</name></owner></lockinfo>`,
			`<owner xmlns="DAV:"><name xmlns="urn:y">bob</name></owner>`,
		},
	} {
		var v struct {
			Owner *RawXMLValue `xml:"owner"`
		}
		if err := xml.Unmarshal([]byte(tc.doc), &v); err != nil {
			t.Fatal(err)
		}
		s, err := MarshalRawXML(v.Owner)
		if err != nil || s != tc.want {
			t.Errorf("MarshalRawXML(%v) = %v, %v, want %v", tc.doc, s, err, tc.want)
			continue
		}

		val, err := UnmarshalRawXML(s)
		if err != nil {
			t.Fatalf("UnmarshalRawXML(%v) = %v", s, err)
		}
		b, err := xml.Marshal(val)
		if err != nil || string(b) != tc.want {
			t.Errorf("UnmarshalRawXML(%v) encodes to %s, %v", s, b, err)
		}
	}
}
