package webdav_test

import (
	"context"
	"encoding/xml"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"slices"
	"sort"
	"strings"
	"sync"
	"testing"

	"github.com/emersion/go-webdav"
)

// deadPropFileSystem is an in-memory FileSystem which stores dead properties.
type deadPropFileSystem struct {
	mu    sync.Mutex
	files map[string]webdav.FileInfo
	props map[string][]webdav.DeadProperty

	patches  [][]webdav.DeadPropertyPatch // passed to PatchDeadProperties
	patchErr error                        // returned by PatchDeadProperties
	gets     int                          // calls to DeadProperties
}

var _ webdav.DeadPropertyFileSystem = (*deadPropFileSystem)(nil)

func newDeadPropFileSystem() *deadPropFileSystem {
	return &deadPropFileSystem{
		files: map[string]webdav.FileInfo{
			"/dir/":      {Path: "/dir/", IsDir: true},
			"/dir/a.txt": {Path: "/dir/a.txt", Size: 3, MIMEType: "text/plain", ETag: "a1"},
		},
		props: make(map[string][]webdav.DeadProperty),
	}
}

func (fs *deadPropFileSystem) Open(ctx context.Context, name string) (io.ReadCloser, error) {
	return io.NopCloser(strings.NewReader("abc")), nil
}

func (fs *deadPropFileSystem) Stat(ctx context.Context, name string) (*webdav.FileInfo, error) {
	fs.mu.Lock()
	defer fs.mu.Unlock()
	fi, ok := fs.files[name]
	if !ok {
		return nil, webdav.NewHTTPError(http.StatusNotFound, nil)
	}
	return &fi, nil
}

func (fs *deadPropFileSystem) ReadDir(ctx context.Context, name string, recursive bool) ([]webdav.FileInfo, error) {
	fs.mu.Lock()
	defer fs.mu.Unlock()
	var l []webdav.FileInfo
	for p, fi := range fs.files {
		if strings.HasPrefix(p, name) {
			l = append(l, fi)
		}
	}
	return l, nil
}

func (fs *deadPropFileSystem) Create(ctx context.Context, name string, body io.ReadCloser, opts *webdav.CreateOptions) (*webdav.FileInfo, bool, error) {
	return nil, false, webdav.NewHTTPError(http.StatusForbidden, nil)
}

func (fs *deadPropFileSystem) RemoveAll(ctx context.Context, name string, opts *webdav.RemoveAllOptions) error {
	return webdav.NewHTTPError(http.StatusForbidden, nil)
}

func (fs *deadPropFileSystem) Mkdir(ctx context.Context, name string) error {
	return webdav.NewHTTPError(http.StatusForbidden, nil)
}

func (fs *deadPropFileSystem) Copy(ctx context.Context, name, dest string, options *webdav.CopyOptions) (bool, error) {
	return false, webdav.NewHTTPError(http.StatusForbidden, nil)
}

func (fs *deadPropFileSystem) Move(ctx context.Context, name, dest string, options *webdav.MoveOptions) (bool, error) {
	return false, webdav.NewHTTPError(http.StatusForbidden, nil)
}

func (fs *deadPropFileSystem) DeadProperties(ctx context.Context, name string) ([]webdav.DeadProperty, error) {
	fs.mu.Lock()
	defer fs.mu.Unlock()
	fs.gets++
	return slices.Clone(fs.props[name]), nil
}

func (fs *deadPropFileSystem) PatchDeadProperties(ctx context.Context, name string, patch []webdav.DeadPropertyPatch) error {
	fs.mu.Lock()
	defer fs.mu.Unlock()
	fs.patches = append(fs.patches, patch)
	if fs.patchErr != nil {
		return fs.patchErr
	}
	props := slices.Clone(fs.props[name])
	for _, p := range patch {
		props = slices.DeleteFunc(props, func(prop webdav.DeadProperty) bool {
			return prop.XMLName == p.Property.XMLName
		})
		if !p.Remove {
			props = append(props, p.Property)
		}
	}
	fs.props[name] = props
	return nil
}

// propNames lists the names of props.
func (fs *deadPropFileSystem) propNames(name string) []string {
	fs.mu.Lock()
	defer fs.mu.Unlock()
	var l []string
	for _, prop := range fs.props[name] {
		l = append(l, nameKey(prop.XMLName))
	}
	return l
}

// patchCalls describes the instructions of each call to PatchDeadProperties.
func (fs *deadPropFileSystem) patchCalls() [][]string {
	fs.mu.Lock()
	defer fs.mu.Unlock()
	var calls [][]string
	for _, patch := range fs.patches {
		var l []string
		for _, p := range patch {
			op := "set "
			if p.Remove {
				op = "remove "
			}
			l = append(l, op+p.Property.XMLName.Local)
		}
		calls = append(calls, l)
	}
	return calls
}

const (
	zNamespace  = "http://ns.example.com/z/"
	msNamespace = "urn:schemas-microsoft-com:"
)

var (
	authorsName = xml.Name{zNamespace, "Authors"}
	colorName   = xml.Name{zNamespace, "color"}
	getETagName = xml.Name{"DAV:", "getetag"}
)

// authorsProp is a dead property whose value has attributes, child elements,
// mixed content, an element in no namespace and significant whitespace.
const authorsProp = `<Z:Authors xmlns:Z="http://ns.example.com/z/" Z:kind="list" xml:lang="en">
	<Z:Author>Jim Whitehead</Z:Author>
	<Z:Author Z:role="editor">Roy <Z:b>Fielding</Z:b> &amp; co</Z:Author>
	<plain xmlns="" a="1">no namespace</plain>
</Z:Authors>`

const win32Prop = `<M:Win32LastModifiedTime xmlns:M="urn:schemas-microsoft-com:">Wed, 05 Nov 2026 10:00:00 GMT</M:Win32LastModifiedTime>`

func nameKey(name xml.Name) string {
	return "{" + name.Space + "}" + name.Local
}

// writeCanonicalToken writes a token in a form which only depends on what RFC
// 4918 section 4.3 requires servers to preserve: the namespace and local name
// of elements and attributes, attribute values, and character data. Namespace
// declarations and comments are left out.
func writeCanonicalToken(sb *strings.Builder, tok xml.Token) {
	switch tok := tok.(type) {
	case xml.StartElement:
		sb.WriteString("<" + nameKey(tok.Name))
		var attrs []string
		for _, attr := range tok.Attr {
			if attr.Name.Space == "xmlns" || (attr.Name.Space == "" && attr.Name.Local == "xmlns") {
				continue
			}
			attrs = append(attrs, fmt.Sprintf(" %v=%q", nameKey(attr.Name), attr.Value))
		}
		sort.Strings(attrs)
		sb.WriteString(strings.Join(attrs, "") + ">")
	case xml.EndElement:
		sb.WriteString("</" + nameKey(tok.Name) + ">")
	case xml.CharData:
		xml.EscapeText(sb, tok)
	}
}

func canonicalXML(t *testing.T, s string) string {
	t.Helper()
	var sb strings.Builder
	d := xml.NewDecoder(strings.NewReader(s))
	for {
		tok, err := d.Token()
		if err == io.EOF {
			break
		} else if err != nil {
			t.Fatalf("decoding %q: %v", s, err)
		}
		writeCanonicalToken(&sb, tok)
	}
	return sb.String()
}

// canonicalProps decodes the child elements of an element, such as DAV:prop
// or DAV:error, in canonical form.
type canonicalProps []canonicalProp

type canonicalProp struct {
	name xml.Name
	xml  string
}

func (props *canonicalProps) UnmarshalXML(d *xml.Decoder, start xml.StartElement) error {
	var sb strings.Builder
	var name xml.Name
	depth := 0
	for {
		tok, err := d.Token()
		if err != nil {
			return err
		}
		switch tok := tok.(type) {
		case xml.StartElement:
			if depth == 0 {
				name = tok.Name
				sb.Reset()
			}
			depth++
		case xml.EndElement:
			if depth == 0 {
				return nil
			}
			depth--
			if depth == 0 {
				writeCanonicalToken(&sb, tok)
				*props = append(*props, canonicalProp{name, sb.String()})
				continue
			}
		}
		if depth > 0 {
			writeCanonicalToken(&sb, tok)
		}
	}
}

// davProp is a property of a multistatus response.
type davProp struct {
	status int
	xml    string   // canonical form of the property element
	errors []string // conditions of the DAV:error element of its propstat
}

// parseMultiStatus returns the properties of each resource of a multistatus
// response, by href and then by name.
func parseMultiStatus(t *testing.T, w *httptest.ResponseRecorder) map[string]map[string]davProp {
	t.Helper()
	if w.Code != http.StatusMultiStatus {
		t.Fatalf("status = %v, want %v; body:\n%s", w.Code, http.StatusMultiStatus, w.Body.String())
	}
	var ms struct {
		Responses []struct {
			Href      string `xml:"DAV: href"`
			PropStats []struct {
				Prop   canonicalProps `xml:"DAV: prop"`
				Status string         `xml:"DAV: status"`
				Error  canonicalProps `xml:"DAV: error"`
			} `xml:"DAV: propstat"`
		} `xml:"DAV: response"`
	}
	if err := xml.Unmarshal(w.Body.Bytes(), &ms); err != nil {
		t.Fatalf("decoding multistatus: %v; body:\n%s", err, w.Body.String())
	}
	m := make(map[string]map[string]davProp)
	for _, resp := range ms.Responses {
		props := make(map[string]davProp)
		for _, propstat := range resp.PropStats {
			var code int
			if _, err := fmt.Sscanf(propstat.Status, "HTTP/1.1 %d", &code); err != nil {
				t.Fatalf("malformed status %q", propstat.Status)
			}
			var conds []string
			for _, cond := range propstat.Error {
				conds = append(conds, nameKey(cond.name))
			}
			for _, prop := range propstat.Prop {
				key := nameKey(prop.name)
				if _, dup := props[key]; dup {
					t.Errorf("%v: property %v listed twice", resp.Href, key)
				}
				props[key] = davProp{status: code, xml: prop.xml, errors: conds}
			}
		}
		m[resp.Href] = props
	}
	return m
}

func davRequest(h http.Handler, method, target, depth, body string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, target, strings.NewReader(body))
	if body != "" {
		req.Header.Set("Content-Type", `application/xml; charset="utf-8"`)
	}
	if depth != "" {
		req.Header.Set("Depth", depth)
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	return w
}

func propPatchBody(instructions ...string) string {
	return `<?xml version="1.0" encoding="utf-8"?>
<D:propertyupdate xmlns:D="DAV:" xmlns:Z="http://ns.example.com/z/">` + strings.Join(instructions, "") + `</D:propertyupdate>`
}

func setInstruction(props ...string) string {
	return "<D:set><D:prop>" + strings.Join(props, "") + "</D:prop></D:set>"
}

func removeInstruction(props ...string) string {
	return "<D:remove><D:prop>" + strings.Join(props, "") + "</D:prop></D:remove>"
}

func propFindBody(props ...string) string {
	return `<?xml version="1.0" encoding="utf-8"?>
<D:propfind xmlns:D="DAV:" xmlns:Z="http://ns.example.com/z/"><D:prop>` + strings.Join(props, "") + `</D:prop></D:propfind>`
}

const (
	allPropBody  = `<?xml version="1.0" encoding="utf-8"?><D:propfind xmlns:D="DAV:"><D:allprop/></D:propfind>`
	propNameBody = `<?xml version="1.0" encoding="utf-8"?><D:propfind xmlns:D="DAV:"><D:propname/></D:propfind>`
)

// checkStatuses checks the status of each property of a resource, and that
// it has no others.
func checkStatuses(t *testing.T, props map[string]davProp, want map[string]int) {
	t.Helper()
	got := make(map[string]int)
	for name, prop := range props {
		got[name] = prop.status
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("property statuses = %v, want %v", got, want)
	}
}

// setTestProps sets authorsProp, win32Prop and DAV:displayname on /dir/a.txt.
func setTestProps(t *testing.T, h http.Handler) {
	t.Helper()
	w := davRequest(h, "PROPPATCH", "/dir/a.txt", "", propPatchBody(
		setInstruction(authorsProp, win32Prop, "<D:displayname>Report</D:displayname>"),
	))
	checkStatuses(t, parseMultiStatus(t, w)["/dir/a.txt"], map[string]int{
		nameKey(authorsName): http.StatusOK,
		"{urn:schemas-microsoft-com:}Win32LastModifiedTime": http.StatusOK,
		"{DAV:}displayname": http.StatusOK,
	})
}

func TestDeadPropertiesSet(t *testing.T) {
	fs := newDeadPropFileSystem()
	h := &webdav.Handler{FileSystem: fs}
	setTestProps(t, h)

	if got, want := fs.patchCalls(), [][]string{{"set Authors", "set Win32LastModifiedTime", "set displayname"}}; !reflect.DeepEqual(got, want) {
		t.Fatalf("PatchDeadProperties calls = %q, want %q", got, want)
	}

	// The backend gets the property as is, and can store it as XML
	authors := fs.props["/dir/a.txt"][0]
	if authors.XMLName != authorsName {
		t.Errorf("XMLName = %v, want %v", authors.XMLName, authorsName)
	}
	wantAttr := []xml.Attr{
		{Name: xml.Name{zNamespace, "kind"}, Value: "list"},
		{Name: xml.Name{"http://www.w3.org/XML/1998/namespace", "lang"}, Value: "en"},
	}
	if !reflect.DeepEqual(authors.Attr, wantAttr) {
		t.Errorf("Attr = %v, want %v", authors.Attr, wantAttr)
	}
	b, err := xml.Marshal(&authors)
	if err != nil {
		t.Fatalf("xml.Marshal() = %v", err)
	}
	if got, want := canonicalXML(t, string(b)), canonicalXML(t, authorsProp); got != want {
		t.Errorf("stored property:\n%v\nwant:\n%v", got, want)
	}
}

func TestDeadPropertiesPropFind(t *testing.T) {
	fs := newDeadPropFileSystem()
	h := &webdav.Handler{FileSystem: fs}
	setTestProps(t, h)
	win32Key := "{urn:schemas-microsoft-com:}Win32LastModifiedTime"

	t.Run("prop", func(t *testing.T) {
		w := davRequest(h, "PROPFIND", "/dir/a.txt", "0", propFindBody(
			"<Z:Authors/>", `<M:Win32LastModifiedTime xmlns:M="urn:schemas-microsoft-com:"/>`,
			"<Z:missing/>", "<D:getetag/>", "<D:displayname/>",
		))
		props := parseMultiStatus(t, w)["/dir/a.txt"]
		checkStatuses(t, props, map[string]int{
			nameKey(authorsName):          http.StatusOK,
			win32Key:                      http.StatusOK,
			"{" + zNamespace + "}missing": http.StatusNotFound,
			"{DAV:}getetag":               http.StatusOK,
			"{DAV:}displayname":           http.StatusOK,
		})
		if got, want := props[nameKey(authorsName)].xml, canonicalXML(t, authorsProp); got != want {
			t.Errorf("Authors:\n%v\nwant:\n%v", got, want)
		}
		if got, want := props[win32Key].xml, canonicalXML(t, win32Prop); got != want {
			t.Errorf("Win32LastModifiedTime = %v, want %v", got, want)
		}
		if got, want := props["{DAV:}displayname"].xml, canonicalXML(t, `<displayname xmlns="DAV:">Report</displayname>`); got != want {
			t.Errorf("displayname = %v, want %v", got, want)
		}
	})

	// RFC 4918 section 9.1: allprop returns the dead properties
	t.Run("allprop", func(t *testing.T) {
		w := davRequest(h, "PROPFIND", "/dir/", "1", allPropBody)
		ms := parseMultiStatus(t, w)
		props := ms["/dir/a.txt"]
		checkStatuses(t, props, map[string]int{
			nameKey(authorsName):     http.StatusOK,
			win32Key:                 http.StatusOK,
			"{DAV:}displayname":      http.StatusOK,
			"{DAV:}resourcetype":     http.StatusOK,
			"{DAV:}getcontentlength": http.StatusOK,
			"{DAV:}getcontenttype":   http.StatusOK,
			"{DAV:}getetag":          http.StatusOK,
		})
		if got, want := props[nameKey(authorsName)].xml, canonicalXML(t, authorsProp); got != want {
			t.Errorf("Authors:\n%v\nwant:\n%v", got, want)
		}
		checkStatuses(t, ms["/dir/"], map[string]int{"{DAV:}resourcetype": http.StatusOK})
	})

	t.Run("propname", func(t *testing.T) {
		w := davRequest(h, "PROPFIND", "/dir/a.txt", "0", propNameBody)
		props := parseMultiStatus(t, w)["/dir/a.txt"]
		checkStatuses(t, props, map[string]int{
			nameKey(authorsName):     http.StatusOK,
			win32Key:                 http.StatusOK,
			"{DAV:}displayname":      http.StatusOK,
			"{DAV:}resourcetype":     http.StatusOK,
			"{DAV:}getcontentlength": http.StatusOK,
			"{DAV:}getcontenttype":   http.StatusOK,
			"{DAV:}getetag":          http.StatusOK,
		})
		if got, want := props[nameKey(authorsName)].xml, canonicalXML(t, `<Authors xmlns="http://ns.example.com/z/"/>`); got != want {
			t.Errorf("Authors = %v, want %v", got, want)
		}
	})

	// Listing live properties, as Client.Stat and Client.ReadDir do, doesn't
	// need the dead ones
	t.Run("live only", func(t *testing.T) {
		gets := fs.gets
		w := davRequest(h, "PROPFIND", "/dir/", "1", propFindBody("<D:resourcetype/>", "<D:getetag/>"))
		parseMultiStatus(t, w)
		if fs.gets != gets {
			t.Errorf("DeadProperties called %v times", fs.gets-gets)
		}
	})
}

func TestDeadPropertiesRemove(t *testing.T) {
	fs := newDeadPropFileSystem()
	h := &webdav.Handler{FileSystem: fs}
	setTestProps(t, h)

	// Removing a property which doesn't exist isn't an error (RFC 4918
	// section 14.23)
	w := davRequest(h, "PROPPATCH", "/dir/a.txt", "", propPatchBody(removeInstruction("<Z:Authors/>", "<Z:missing/>")))
	checkStatuses(t, parseMultiStatus(t, w)["/dir/a.txt"], map[string]int{
		nameKey(authorsName):          http.StatusOK,
		"{" + zNamespace + "}missing": http.StatusOK,
	})
	want := []string{"{urn:schemas-microsoft-com:}Win32LastModifiedTime", "{DAV:}displayname"}
	if got := fs.propNames("/dir/a.txt"); !reflect.DeepEqual(got, want) {
		t.Errorf("stored properties = %v, want %v", got, want)
	}

	w = davRequest(h, "PROPFIND", "/dir/a.txt", "0", propFindBody("<Z:Authors/>"))
	checkStatuses(t, parseMultiStatus(t, w)["/dir/a.txt"], map[string]int{nameKey(authorsName): http.StatusNotFound})
}

// RFC 4918 section 9.2: the instructions are processed in document order.
func TestDeadPropertiesDocumentOrder(t *testing.T) {
	fs := newDeadPropFileSystem()
	h := &webdav.Handler{FileSystem: fs}

	w := davRequest(h, "PROPPATCH", "/dir/a.txt", "", propPatchBody(
		setInstruction("<Z:color>red</Z:color>"),
		removeInstruction("<Z:color/>", "<Z:shape/>"),
		setInstruction("<Z:shape>round</Z:shape>"),
	))
	checkStatuses(t, parseMultiStatus(t, w)["/dir/a.txt"], map[string]int{
		nameKey(colorName):          http.StatusOK,
		"{" + zNamespace + "}shape": http.StatusOK,
	})
	if got, want := fs.patchCalls(), [][]string{{"set color", "remove color", "remove shape", "set shape"}}; !reflect.DeepEqual(got, want) {
		t.Errorf("PatchDeadProperties calls = %q, want %q", got, want)
	}
	if got, want := fs.propNames("/dir/a.txt"), []string{"{" + zNamespace + "}shape"}; !reflect.DeepEqual(got, want) {
		t.Errorf("stored properties = %v, want %v", got, want)
	}
}

// RFC 4918 section 9.2: a PROPPATCH request is executed in full or not at
// all, and protected properties cannot be modified (section 9.2.1).
func TestDeadPropertiesProtected(t *testing.T) {
	fs := newDeadPropFileSystem()
	h := &webdav.Handler{FileSystem: fs}
	setTestProps(t, h)

	w := davRequest(h, "PROPPATCH", "/dir/a.txt", "", propPatchBody(
		setInstruction("<Z:color>red</Z:color>", `<D:getetag>"forged"</D:getetag>`),
		removeInstruction("<Z:Authors/>", "<D:resourcetype/>"),
	))
	props := parseMultiStatus(t, w)["/dir/a.txt"]
	checkStatuses(t, props, map[string]int{
		nameKey(colorName):   http.StatusFailedDependency,
		nameKey(authorsName): http.StatusFailedDependency,
		"{DAV:}getetag":      http.StatusForbidden,
		"{DAV:}resourcetype": http.StatusForbidden,
	})
	wantErrors := []string{"{DAV:}cannot-modify-protected-property"}
	for _, name := range []string{"{DAV:}getetag", "{DAV:}resourcetype"} {
		if got := props[name].errors; !reflect.DeepEqual(got, wantErrors) {
			t.Errorf("%v: error conditions = %v, want %v", name, got, wantErrors)
		}
	}

	if got := len(fs.patches); got != 1 {
		t.Errorf("PatchDeadProperties called %v times after the failed request", got-1)
	}
	want := []string{nameKey(authorsName), "{urn:schemas-microsoft-com:}Win32LastModifiedTime", "{DAV:}displayname"}
	if got := fs.propNames("/dir/a.txt"); !reflect.DeepEqual(got, want) {
		t.Errorf("stored properties = %v, want %v", got, want)
	}

	w = davRequest(h, "PROPFIND", "/dir/a.txt", "0", propFindBody("<D:getetag/>"))
	if got, want := parseMultiStatus(t, w)["/dir/a.txt"]["{DAV:}getetag"].xml, canonicalXML(t, `<getetag xmlns="DAV:">"a1"</getetag>`); got != want {
		t.Errorf("getetag = %v, want %v", got, want)
	}
}

// A property in no namespace (as the litmus propnullns test sets) stays in
// no namespace, rather than ending up in the DAV: namespace of the response.
func TestDeadPropertiesNoNamespace(t *testing.T) {
	fs := newDeadPropFileSystem()
	h := &webdav.Handler{FileSystem: fs}
	const prop = `<nonamespace xmlns="">value</nonamespace>`

	w := davRequest(h, "PROPPATCH", "/dir/a.txt", "", propPatchBody(setInstruction(prop)))
	checkStatuses(t, parseMultiStatus(t, w)["/dir/a.txt"], map[string]int{"{}nonamespace": http.StatusOK})

	w = davRequest(h, "PROPFIND", "/dir/a.txt", "0", propFindBody(`<nonamespace xmlns=""/>`))
	props := parseMultiStatus(t, w)["/dir/a.txt"]
	checkStatuses(t, props, map[string]int{"{}nonamespace": http.StatusOK})
	if got, want := props["{}nonamespace"].xml, canonicalXML(t, prop); got != want {
		t.Errorf("nonamespace = %v, want %v", got, want)
	}

	w = davRequest(h, "PROPFIND", "/dir/a.txt", "0", propNameBody)
	if _, ok := parseMultiStatus(t, w)["/dir/a.txt"]["{}nonamespace"]; !ok {
		t.Errorf("propname doesn't list the property in no namespace:\n%s", w.Body.String())
	}
}

func TestDeadPropertiesPatchError(t *testing.T) {
	fs := newDeadPropFileSystem()
	fs.patchErr = webdav.NewHTTPError(http.StatusInsufficientStorage, nil)
	h := &webdav.Handler{FileSystem: fs}

	w := davRequest(h, "PROPPATCH", "/dir/a.txt", "", propPatchBody(setInstruction("<Z:color>red</Z:color>")))
	if w.Code != http.StatusInsufficientStorage {
		t.Errorf("status = %v, want %v", w.Code, http.StatusInsufficientStorage)
	}
}

// A malformed value stored by the backend fails this property only.
func TestDeadPropertiesMalformedValue(t *testing.T) {
	fs := newDeadPropFileSystem()
	fs.props["/dir/a.txt"] = []webdav.DeadProperty{
		{XMLName: colorName, InnerXML: []byte("<unclosed>")},
		{XMLName: xml.Name{zNamespace, "shape"}, InnerXML: []byte("round")},
	}
	h := &webdav.Handler{FileSystem: fs}

	w := davRequest(h, "PROPFIND", "/dir/a.txt", "0", propFindBody("<Z:color/>", "<Z:shape/>"))
	props := parseMultiStatus(t, w)["/dir/a.txt"]
	checkStatuses(t, props, map[string]int{
		nameKey(colorName):          http.StatusInternalServerError,
		"{" + zNamespace + "}shape": http.StatusOK,
	})
	if got, want := props["{"+zNamespace+"}shape"].xml, canonicalXML(t, `<shape xmlns="http://ns.example.com/z/">round</shape>`); got != want {
		t.Errorf("shape = %v, want %v", got, want)
	}
}

// Without DeadPropertyFileSystem, no property can be modified.
func TestDeadPropertiesUnsupported(t *testing.T) {
	fs := struct{ webdav.FileSystem }{newDeadPropFileSystem()}
	h := &webdav.Handler{FileSystem: fs}

	w := davRequest(h, "PROPPATCH", "/dir/a.txt", "", propPatchBody(
		setInstruction("<Z:color>red</Z:color>", "<D:displayname>Report</D:displayname>", `<D:getetag>"forged"</D:getetag>`),
	))
	props := parseMultiStatus(t, w)["/dir/a.txt"]
	checkStatuses(t, props, map[string]int{
		nameKey(colorName):  http.StatusForbidden,
		"{DAV:}displayname": http.StatusForbidden,
		"{DAV:}getetag":     http.StatusForbidden,
	})
	// RFC 4918 section 9.2.1: a protected property gets its precondition
	// all the same
	for name, want := range map[string][]string{
		nameKey(colorName):  nil,
		"{DAV:}displayname": nil,
		"{DAV:}getetag":     {"{DAV:}cannot-modify-protected-property"},
	} {
		if got := props[name].errors; !reflect.DeepEqual(got, want) {
			t.Errorf("%v: error conditions = %v, want %v", name, got, want)
		}
	}

	w = davRequest(h, http.MethodOptions, "/dir/a.txt", "", "")
	if allow := w.Header().Get("Allow"); strings.Contains(allow, "PROPPATCH") {
		t.Errorf("Allow = %q, want no PROPPATCH", allow)
	}
}

func TestDeadPropertiesOptions(t *testing.T) {
	h := &webdav.Handler{FileSystem: newDeadPropFileSystem()}
	w := davRequest(h, http.MethodOptions, "/dir/a.txt", "", "")
	if allow := w.Header().Get("Allow"); !strings.Contains(allow, "PROPPATCH") {
		t.Errorf("Allow = %q, want PROPPATCH", allow)
	}
}

func TestDeadPropertyXML(t *testing.T) {
	var prop webdav.DeadProperty
	if err := xml.Unmarshal([]byte(authorsProp), &prop); err != nil {
		t.Fatalf("xml.Unmarshal() = %v", err)
	}
	if prop.XMLName != authorsName {
		t.Errorf("XMLName = %v, want %v", prop.XMLName, authorsName)
	}
	// InnerXML is well-formed on its own: it declares the namespaces it uses
	inner := canonicalXML(t, string(prop.InnerXML))
	if !strings.Contains(inner, "<{"+zNamespace+"}Author {"+zNamespace+"}role=\"editor\">") {
		t.Errorf("InnerXML = %s, canonical form %s lacks the namespaced Author element", prop.InnerXML, inner)
	}

	b, err := xml.Marshal(prop)
	if err != nil {
		t.Fatalf("xml.Marshal() = %v", err)
	}
	if got, want := canonicalXML(t, string(b)), canonicalXML(t, authorsProp); got != want {
		t.Errorf("xml.Marshal() = %s\ncanonical form:\n%v\nwant:\n%v", b, got, want)
	}

	// A property built by hand
	prop = webdav.DeadProperty{
		XMLName:  xml.Name{msNamespace, "Win32FileAttributes"},
		InnerXML: []byte("00000020"),
	}
	b, err = xml.Marshal(prop)
	if err != nil {
		t.Fatalf("xml.Marshal() = %v", err)
	}
	if got, want := string(b), `<Win32FileAttributes xmlns="urn:schemas-microsoft-com:">00000020</Win32FileAttributes>`; got != want {
		t.Errorf("xml.Marshal() = %v, want %v", got, want)
	}

	prop.InnerXML = []byte("<unclosed>")
	if _, err := xml.Marshal(prop); err == nil {
		t.Errorf("xml.Marshal() of a malformed value succeeded")
	}
}
