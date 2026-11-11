package internal

import (
	"bytes"
	"encoding/xml"
	"fmt"
	"io"
	"reflect"
	"strings"
)

// RawXMLValue is a raw XML value. It implements xml.Unmarshaler and
// xml.Marshaler and can be used to delay XML decoding or precompute an XML
// encoding.
type RawXMLValue struct {
	tok      xml.Token // guaranteed not to be xml.EndElement
	children []RawXMLValue

	// Unfortunately encoding/xml doesn't offer TokenWriter, so we need to
	// cache outgoing data.
	out interface{}
}

// NewRawXMLElement creates a new RawXMLValue for an element. An element in no
// namespace gets an empty default namespace declaration, so that it doesn't
// end up in the namespace of the element it's encoded in.
func NewRawXMLElement(name xml.Name, attr []xml.Attr, children []RawXMLValue) *RawXMLValue {
	if name.Space == "" {
		attr = append([]xml.Attr{{Name: xml.Name{Local: "xmlns"}}}, attr...)
	}
	return &RawXMLValue{tok: xml.StartElement{name, attr}, children: children}
}

// NewRawXMLFragment creates a new RawXMLValue for an element, as
// NewRawXMLElement does, whose content is an XML fragment. Only the elements
// and character data of the content are kept.
func NewRawXMLFragment(name xml.Name, attr []xml.Attr, content []byte) (*RawXMLValue, error) {
	if name.Local == "" {
		return nil, fmt.Errorf("webdav: element without a name")
	}
	val := NewRawXMLElement(name, attr, nil)
	d := xml.NewDecoder(bytes.NewReader(content))
	if err := val.decodeData(d, true); err != nil {
		return nil, err
	}
	return val, nil
}

// decodeData decodes the elements and character data of the content of an
// element, up to its end tag or, for a fragment, up to the end of d.
func (val *RawXMLValue) decodeData(d *xml.Decoder, fragment bool) error {
	for {
		tok, err := d.Token()
		if err == io.EOF && fragment {
			return nil
		} else if err != nil {
			return err
		}
		switch tok := tok.(type) {
		case xml.StartElement:
			child := RawXMLValue{tok: tok.Copy()}
			if err := child.decodeData(d, false); err != nil {
				return err
			}
			val.children = append(val.children, child)
		case xml.EndElement:
			return nil
		case xml.CharData:
			val.children = append(val.children, RawXMLValue{tok: tok.Copy()})
		}
	}
}

// EncodeRawXMLElement encodes a value into a new RawXMLValue. The XML value
// can only be used for marshalling.
func EncodeRawXMLElement(v interface{}) (*RawXMLValue, error) {
	return &RawXMLValue{out: v}, nil
}

// UnmarshalXML implements xml.Unmarshaler.
func (val *RawXMLValue) UnmarshalXML(d *xml.Decoder, start xml.StartElement) error {
	val.tok = start
	val.children = nil
	val.out = nil

	for {
		tok, err := d.Token()
		if err != nil {
			return err
		}
		switch tok := tok.(type) {
		case xml.StartElement:
			child := RawXMLValue{}
			if err := child.UnmarshalXML(d, tok); err != nil {
				return err
			}
			val.children = append(val.children, child)
		case xml.EndElement:
			return nil
		default:
			val.children = append(val.children, RawXMLValue{tok: xml.CopyToken(tok)})
		}
	}
}

// MarshalXML implements xml.Marshaler.
func (val *RawXMLValue) MarshalXML(e *xml.Encoder, start xml.StartElement) error {
	if val.out != nil {
		return e.Encode(val.out)
	}
	return val.encode(e, false, false)
}

// encode encodes the XML value. inNamespace reports whether the element it's
// encoded in is in a namespace. If dataOnly is set, only elements and
// character data are encoded, without comments, processing instructions or
// directives.
func (val *RawXMLValue) encode(e *xml.Encoder, inNamespace, dataOnly bool) error {
	switch tok := val.tok.(type) {
	case xml.StartElement:
		if err := e.EncodeToken(encodableStartElement(tok, inNamespace)); err != nil {
			return err
		}
		for i := range val.children {
			if err := val.children[i].encode(e, tok.Name.Space != "", dataOnly); err != nil {
				return err
			}
		}
		return e.EncodeToken(tok.End())
	case xml.EndElement:
		panic("unexpected end element")
	case xml.CharData:
		return e.EncodeToken(tok)
	default:
		if dataOnly {
			return nil
		}
		return e.EncodeToken(tok)
	}
}

func isNamespaceDecl(name xml.Name) bool {
	return name.Space == "xmlns" || (name.Space == "" && name.Local == "xmlns")
}

// encodableStartElement returns a copy of start for xml.Encoder. xml.Encoder
// declares the namespaces of the element and attribute names itself, and
// garbles the namespace declarations listed among the attributes: these are
// left out. An element in no namespace gets an empty default namespace
// declaration, which xml.Encoder doesn't write, if it's encoded in an element
// in a namespace, as reported by inNamespace, or if it had one, such as
// NewRawXMLElement gives it.
func encodableStartElement(start xml.StartElement, inNamespace bool) xml.StartElement {
	emptyDefault := start.Name.Space == "" && inNamespace
	attr := make([]xml.Attr, 0, len(start.Attr)+1)
	for _, a := range start.Attr {
		if !isNamespaceDecl(a.Name) {
			attr = append(attr, a)
		} else if a.Name.Space == "" && a.Value == "" && start.Name.Space == "" {
			emptyDefault = true
		}
	}
	if emptyDefault {
		attr = append([]xml.Attr{{Name: xml.Name{Local: "xmlns"}}}, attr...)
	}
	return xml.StartElement{Name: start.Name, Attr: attr}
}

// Attr returns the attributes of an element, without the namespace
// declarations.
func (val *RawXMLValue) Attr() []xml.Attr {
	start, ok := val.tok.(xml.StartElement)
	if !ok {
		return nil
	}
	var attr []xml.Attr
	for _, a := range start.Attr {
		if !isNamespaceDecl(a.Name) {
			attr = append(attr, a)
		}
	}
	return attr
}

// InnerXML encodes the content of an element: its child elements and
// character data, without comments, processing instructions or directives.
// The child elements declare the namespaces they use, so that the encoding is
// well-formed on its own.
func (val *RawXMLValue) InnerXML() ([]byte, error) {
	start, ok := val.tok.(xml.StartElement)
	if !ok {
		return nil, fmt.Errorf("webdav: XML value isn't an element")
	}
	var buf bytes.Buffer
	e := xml.NewEncoder(&buf)
	for i := range val.children {
		if err := val.children[i].encode(e, start.Name.Space != "", true); err != nil {
			return nil, err
		}
	}
	if err := e.Flush(); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

var _ xml.Marshaler = (*RawXMLValue)(nil)
var _ xml.Unmarshaler = (*RawXMLValue)(nil)

// MarshalRawXML encodes a raw XML element into a standalone XML document, to
// be decoded by UnmarshalRawXML. The namespaces of the element and of its
// children are declared again, as when encoding it in another document.
func MarshalRawXML(val *RawXMLValue) (string, error) {
	b, err := xml.Marshal(val)
	return string(b), err
}

// UnmarshalRawXML decodes a raw XML element encoded by MarshalRawXML.
func UnmarshalRawXML(s string) (*RawXMLValue, error) {
	var val RawXMLValue
	if err := xml.Unmarshal([]byte(s), &val); err != nil {
		return nil, err
	}
	return &val, nil
}

func (val *RawXMLValue) Decode(v interface{}) error {
	return xml.NewTokenDecoder(val.TokenReader()).Decode(&v)
}

func (val *RawXMLValue) XMLName() (name xml.Name, ok bool) {
	if start, ok := val.tok.(xml.StartElement); ok {
		return start.Name, true
	}
	return xml.Name{}, false
}

// TokenReader returns a stream of tokens for the XML value.
func (val *RawXMLValue) TokenReader() xml.TokenReader {
	if val.out != nil {
		panic("webdav: called RawXMLValue.TokenReader on a marshal-only XML value")
	}
	return &rawXMLValueReader{val: val}
}

type rawXMLValueReader struct {
	val         *RawXMLValue
	start, end  bool
	child       int
	childReader xml.TokenReader
}

func (tr *rawXMLValueReader) Token() (xml.Token, error) {
	if tr.end {
		return nil, io.EOF
	}

	start, ok := tr.val.tok.(xml.StartElement)
	if !ok {
		tr.end = true
		return tr.val.tok, nil
	}

	if !tr.start {
		tr.start = true
		return start, nil
	}

	for tr.child < len(tr.val.children) {
		if tr.childReader == nil {
			tr.childReader = tr.val.children[tr.child].TokenReader()
		}

		tok, err := tr.childReader.Token()
		if err == io.EOF {
			tr.childReader = nil
			tr.child++
		} else {
			return tok, err
		}
	}

	tr.end = true
	return start.End(), nil
}

var _ xml.TokenReader = (*rawXMLValueReader)(nil)

func valueXMLName(v interface{}) (xml.Name, error) {
	t := reflect.TypeOf(v)
	for t.Kind() == reflect.Ptr {
		t = t.Elem()
	}
	if t.Kind() != reflect.Struct {
		return xml.Name{}, fmt.Errorf("webdav: %T is not a struct", v)
	}
	nameField, ok := t.FieldByName("XMLName")
	if !ok {
		return xml.Name{}, fmt.Errorf("webdav: %T is missing an XMLName struct field", v)
	}
	if nameField.Type != reflect.TypeOf(xml.Name{}) {
		return xml.Name{}, fmt.Errorf("webdav: %T.XMLName isn't an xml.Name", v)
	}
	tag := nameField.Tag.Get("xml")
	if tag == "" {
		return xml.Name{}, fmt.Errorf(`webdav: %T.XMLName is missing an "xml" tag`, v)
	}
	name := strings.Split(tag, ",")[0]
	nameParts := strings.Split(name, " ")
	if len(nameParts) != 2 {
		return xml.Name{}, fmt.Errorf("webdav: expected a namespace and local name in %T.XMLName's xml tag", v)
	}
	return xml.Name{nameParts[0], nameParts[1]}, nil
}
