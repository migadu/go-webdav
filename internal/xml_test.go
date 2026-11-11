package internal

import (
	"bytes"
	"encoding/xml"
	"io"
	"testing"
)

const rawXML = `<?xml version="1.0" encoding="UTF-8"?>
<bookstore>
  <book category="COOKING">
    <title lang="en">Everyday Italian</title>
    <author>Giada De Laurentiis</author>
    <year>2005</year>
  </book>

  <book category="CHILDREN">
    <title lang="en">Harry Potter</title>
    <author>J K. Rowling</author>
    <year>2005</year>
  </book>
</bookstore>`

func TestRawXMLValue(t *testing.T) {
	// TODO: test XML namespaces too

	var rawValue RawXMLValue
	if err := xml.Unmarshal([]byte(rawXML), &rawValue); err != nil {
		t.Fatalf("xml.Unmarshal() = %v", err)
	}

	b, err := xml.Marshal(&rawValue)
	if err != nil {
		t.Fatalf("xml.Marshal() = %v", err)
	}

	s := xml.Header + string(b)
	if s != rawXML {
		t.Errorf("input doesn't match output:\n%v\nvs.\n%v", rawXML, s)
	}
}

// Encoding a decoded value keeps the namespaces of its elements and
// attributes, without garbling the namespace declarations nor moving an
// element in no namespace into the namespace of its parent.
func TestRawXMLValue_namespaces(t *testing.T) {
	const in = `<Z:a xmlns:Z="urn:z" Z:x="1"><b>no namespace</b><Z:c><d xmlns="">no namespace</d></Z:c></Z:a>`
	var rawValue RawXMLValue
	if err := xml.Unmarshal([]byte(in), &rawValue); err != nil {
		t.Fatalf("xml.Unmarshal() = %v", err)
	}

	b, err := xml.Marshal(&rawValue)
	if err != nil {
		t.Fatalf("xml.Marshal() = %v", err)
	}
	want := `<a xmlns="urn:z" xmlns:_="urn:z" _:x="1"><b xmlns="">no namespace</b><c xmlns="urn:z"><d xmlns="">no namespace</d></c></a>`
	if string(b) != want {
		t.Errorf("xml.Marshal() = %s, want %s", b, want)
	}

	inner, err := rawValue.InnerXML()
	if err != nil {
		t.Fatalf("InnerXML() = %v", err)
	}
	want = `<b xmlns="">no namespace</b><c xmlns="urn:z"><d xmlns="">no namespace</d></c>`
	if string(inner) != want {
		t.Errorf("InnerXML() = %s, want %s", inner, want)
	}

	// An element built in no namespace stays in no namespace
	prop := Prop{Raw: []RawXMLValue{*NewRawXMLElement(xml.Name{Local: "nonamespace"}, nil, nil)}}
	b, err = xml.Marshal(&prop)
	if err != nil {
		t.Fatalf("xml.Marshal() = %v", err)
	}
	var decoded Prop
	if err := xml.Unmarshal(b, &decoded); err != nil {
		t.Fatalf("xml.Unmarshal() = %v", err)
	}
	if name, _ := decoded.Raw[0].XMLName(); name != (xml.Name{Local: "nonamespace"}) {
		t.Errorf("%s decodes to %v, want an element in no namespace", b, name)
	}
}

func TestRawXMLValue_TokenReader(t *testing.T) {
	var rawValue RawXMLValue
	if err := xml.Unmarshal([]byte(rawXML), &rawValue); err != nil {
		t.Fatalf("xml.Unmarshal() = %v", err)
	}

	tr := rawValue.TokenReader()

	var buf bytes.Buffer
	enc := xml.NewEncoder(&buf)
	for {
		tok, err := tr.Token()
		if err == io.EOF {
			break
		} else if err != nil {
			t.Fatalf("TokenReader.Token() = %v", err)
		}

		if err := enc.EncodeToken(tok); err != nil {
			t.Fatalf("Encoder.EncodeToken() = %v", err)
		}
	}
	if err := enc.Flush(); err != nil {
		t.Fatalf("Encoder.Flush() = %v", err)
	}

	s := xml.Header + buf.String()
	if s != rawXML {
		t.Errorf("input doesn't match output:\n%v\nvs.\n%v", rawXML, s)
	}
}
