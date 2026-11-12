package carddav

import (
	"context"
	"encoding/xml"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/migadu/go-webdav/internal"
)

// Tests for the access control properties of RFC 3744 section 5.

const propFindACLProps = `<?xml version="1.0" encoding="UTF-8"?>
<D:propfind xmlns:D="DAV:">
  <D:prop>
    <D:owner/>
    <D:supported-privilege-set/>
    <D:principal-collection-set/>
  </D:prop>
</D:propfind>`

// The privileges defined on the resources: DAV:all aggregates all of them,
// DAV:write aggregates the four privileges RFC 3744 section 3.12 requires it
// to.
const davPrivilegeTree = `DAV: all (abstract)
  DAV: read
    DAV: read-current-user-privilege-set (abstract)
  DAV: write
    DAV: write-properties
    DAV: write-content
    DAV: bind
    DAV: unbind
`

const (
	aclPrincipalPath   = "/test/"
	aclHomeSetPath     = "/test/contacts/"
	aclAddressBookPath = "/test/contacts/private/"
	aclObjectPath      = aclAddressBookPath + "alice.vcf"
)

// testSupportedPrivilege decodes a DAV:supported-privilege, independently of
// the server's own types.
type testSupportedPrivilege struct {
	Privilege struct {
		Raw []internal.RawXMLValue `xml:",any"`
	} `xml:"DAV: privilege"`
	Abstract    *struct{} `xml:"DAV: abstract"`
	Description *struct {
		Lang string `xml:"http://www.w3.org/XML/1998/namespace lang,attr"`
		Text string `xml:",chardata"`
	} `xml:"DAV: description"`
	Aggregated []testSupportedPrivilege `xml:"DAV: supported-privilege"`
}

type testSupportedPrivilegeSet struct {
	XMLName    xml.Name                 `xml:"DAV: supported-privilege-set"`
	Privileges []testSupportedPrivilege `xml:"DAV: supported-privilege"`
}

// tree renders the privileges of the set, one per line, indented below the
// privilege which aggregates them. It reports the privileges which don't have
// the single privilege and the description in a human language that RFC 3744
// section 5.3 requires.
func (s *testSupportedPrivilegeSet) tree(t *testing.T) string {
	t.Helper()
	var sb strings.Builder
	var walk func(sps []testSupportedPrivilege, indent string)
	walk = func(sps []testSupportedPrivilege, indent string) {
		for _, sp := range sps {
			var names []string
			for _, raw := range sp.Privilege.Raw {
				if name, ok := raw.XMLName(); ok {
					names = append(names, name.Space+" "+name.Local)
				}
			}
			if len(names) != 1 {
				t.Errorf("supported-privilege has privileges %v, want one", names)
			}
			if sp.Description == nil || strings.TrimSpace(sp.Description.Text) == "" || sp.Description.Lang == "" {
				t.Errorf("supported-privilege %v has description %+v, want a text with xml:lang", names, sp.Description)
			}
			sb.WriteString(indent + strings.Join(names, ", "))
			if sp.Abstract != nil {
				sb.WriteString(" (abstract)")
			}
			sb.WriteString("\n")
			walk(sp.Aggregated, indent+"  ")
		}
	}
	walk(s.Privileges, "")
	return sb.String()
}

// nonAbstract returns the privileges of the set which aren't abstract.
func (s *testSupportedPrivilegeSet) nonAbstract() map[string]bool {
	m := make(map[string]bool)
	var walk func(sps []testSupportedPrivilege)
	walk = func(sps []testSupportedPrivilege) {
		for _, sp := range sps {
			for _, raw := range sp.Privilege.Raw {
				if name, ok := raw.XMLName(); ok && sp.Abstract == nil {
					m[name.Space+" "+name.Local] = true
				}
			}
			walk(sp.Aggregated)
		}
	}
	walk(s.Privileges)
	return m
}

// aclBackend serves alice at any path below the address book, which is
// read-only, along with alice, if readOnly is set.
type aclBackend struct {
	readOnlyObjectBackend
}

func (b *aclBackend) ListAddressBooks(ctx context.Context) ([]AddressBook, error) {
	abs, err := b.readOnlyObjectBackend.ListAddressBooks(ctx)
	for i := range abs {
		abs[i].ReadOnly = b.readOnly
	}
	return abs, err
}

func (b *aclBackend) GetAddressBook(ctx context.Context, path string) (*AddressBook, error) {
	ab, err := b.readOnlyObjectBackend.GetAddressBook(ctx, path)
	if ab != nil {
		ab.ReadOnly = b.readOnly
	}
	return ab, err
}

// serveACLPropFind sends a Depth: 0 PROPFIND request for body to the
// resource at p, and returns the response for it.
func serveACLPropFind(t *testing.T, b Backend, p, body string) (string, *internal.Response) {
	t.Helper()
	req := httptest.NewRequest("PROPFIND", p, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/xml")
	req.Header.Set("Depth", "0")
	ctx := context.WithValue(req.Context(), currentUserPrincipalKey, aclPrincipalPath)
	ctx = context.WithValue(ctx, homeSetPathKey, aclHomeSetPath)
	ctx = context.WithValue(ctx, addressBookPathKey, aclAddressBookPath)
	w := httptest.NewRecorder()
	(&Handler{Backend: b}).ServeHTTP(w, req.WithContext(ctx))
	if w.Code != http.StatusMultiStatus {
		t.Fatalf("PROPFIND %v returned %v:\n%s", p, w.Code, w.Body.String())
	}

	var ms internal.MultiStatus
	if err := xml.Unmarshal(w.Body.Bytes(), &ms); err != nil {
		t.Fatalf("PROPFIND %v: failed to decode multistatus: %v", p, err)
	}
	for i := range ms.Responses {
		for _, h := range ms.Responses[i].Hrefs {
			if h.Path == p {
				return w.Body.String(), &ms.Responses[i]
			}
		}
	}
	t.Fatalf("PROPFIND %v: no response for it in:\n%s", p, w.Body.String())
	return "", nil
}

func TestPropFindACLProps(t *testing.T) {
	for _, tc := range []struct {
		name, path string
	}{
		{"principal", aclPrincipalPath},
		{"home set", aclHomeSetPath},
		{"address book", aclAddressBookPath},
		{"object", aclObjectPath},
	} {
		t.Run(tc.name, func(t *testing.T) {
			body, resp := serveACLPropFind(t, &aclBackend{}, tc.path, propFindACLProps)

			// RFC 3744 section 5.1: the current user owns the resources
			// the server serves for them
			if want := `<owner xmlns="DAV:"><href>/test/</href></owner>`; !strings.Contains(body, want) {
				t.Errorf("want %s in:\n%s", want, body)
			}
			// RFC 3744 section 5.8: the collection which holds the
			// principal
			if want := `<principal-collection-set xmlns="DAV:"><href>/</href></principal-collection-set>`; !strings.Contains(body, want) {
				t.Errorf("want %s in:\n%s", want, body)
			}

			var set testSupportedPrivilegeSet
			if err := resp.DecodeProp(&set); err != nil {
				t.Fatalf("supported-privilege-set: %v", err)
			}
			if got := set.tree(t); got != davPrivilegeTree {
				t.Errorf("supported-privilege-set =\n%s\nwant:\n%s", got, davPrivilegeTree)
			}
		})
	}
}

// testPrivilegeSet decodes a DAV:current-user-privilege-set, independently
// of the server's own types.
type testPrivilegeSet struct {
	XMLName    xml.Name `xml:"DAV: current-user-privilege-set"`
	Privileges []struct {
		Raw []internal.RawXMLValue `xml:",any"`
	} `xml:"DAV: privilege"`
}

// RFC 3744 section 5.4: each privilege of DAV:current-user-privilege-set is a
// non-abstract privilege of DAV:supported-privilege-set, in its own
// DAV:privilege element.
func TestCurrentUserPrivilegesSupported(t *testing.T) {
	const body = `<?xml version="1.0" encoding="UTF-8"?>
<D:propfind xmlns:D="DAV:">
  <D:prop>
    <D:current-user-privilege-set/>
    <D:supported-privilege-set/>
  </D:prop>
</D:propfind>`

	for _, readOnly := range []bool{false, true} {
		b := &aclBackend{readOnlyObjectBackend{readOnly: readOnly}}
		for _, p := range []string{aclAddressBookPath, aclObjectPath} {
			_, resp := serveACLPropFind(t, b, p, body)

			var set testSupportedPrivilegeSet
			if err := resp.DecodeProp(&set); err != nil {
				t.Fatalf("PROPFIND %v: supported-privilege-set: %v", p, err)
			}
			supported := set.nonAbstract()

			var current testPrivilegeSet
			if err := resp.DecodeProp(&current); err != nil {
				t.Fatalf("PROPFIND %v: current-user-privilege-set: %v", p, err)
			}
			for _, priv := range current.Privileges {
				if len(priv.Raw) != 1 {
					t.Errorf("readOnly=%v: PROPFIND %v: DAV:privilege element holds %d privileges, want one", readOnly, p, len(priv.Raw))
				}
				for _, raw := range priv.Raw {
					name, _ := raw.XMLName()
					if !supported[name.Space+" "+name.Local] {
						t.Errorf("readOnly=%v: PROPFIND %v: current user privilege %v isn't a non-abstract supported privilege", readOnly, p, name)
					}
				}
			}
		}
	}
}

// RFC 3744 section 5: an allprop PROPFIND request doesn't return the access
// control properties.
func TestAllPropNoACLProps(t *testing.T) {
	const allprop = `<?xml version="1.0" encoding="utf-8" ?>
<D:propfind xmlns:D="DAV:"><D:allprop/></D:propfind>`

	for _, p := range []string{aclPrincipalPath, aclHomeSetPath, aclAddressBookPath, aclObjectPath} {
		body, _ := serveACLPropFind(t, &aclBackend{}, p, allprop)
		for _, name := range []string{"owner", "supported-privilege-set", "principal-collection-set"} {
			if strings.Contains(body, "<"+name+" ") {
				t.Errorf("allprop PROPFIND %v returned DAV:%v:\n%s", p, name, body)
			}
		}
	}
}
