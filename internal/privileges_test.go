package internal

import (
	"encoding/xml"
	"sort"
	"strings"
	"testing"
)

// privilegeNames returns the names of the privileges p holds, as
// "namespace local".
func privilegeNames(t *testing.T, p *Privilege) []string {
	t.Helper()
	b, err := xml.Marshal(p)
	if err != nil {
		t.Fatal(err)
	}
	var v struct {
		Raw []RawXMLValue `xml:",any"`
	}
	if err := xml.Unmarshal(b, &v); err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, raw := range v.Raw {
		if name, ok := raw.XMLName(); ok {
			names = append(names, name.Space+" "+name.Local)
		}
	}
	return names
}

// currentUserPrivileges returns the names of the privileges of
// NewCurrentUserPrivilegeSet(readOnly), and reports the DAV:privilege
// elements which don't hold a single privilege.
func currentUserPrivileges(t *testing.T, readOnly bool) []string {
	t.Helper()
	set := NewCurrentUserPrivilegeSet(readOnly)
	var names []string
	for i := range set.Privilege {
		l := privilegeNames(t, &set.Privilege[i])
		if len(l) != 1 {
			t.Errorf("readOnly=%v: DAV:privilege element holds %v, want a single privilege", readOnly, l)
		}
		names = append(names, l...)
	}
	sort.Strings(names)
	return names
}

// RFC 3744 section 5.4: each DAV:privilege element of
// DAV:current-user-privilege-set identifies a single privilege, and DAV:write
// is listed along with the privileges it aggregates.
func TestNewCurrentUserPrivilegeSet(t *testing.T) {
	for _, tc := range []struct {
		readOnly bool
		want     []string
	}{
		{true, []string{"DAV: read"}},
		{false, []string{"DAV: bind", "DAV: read", "DAV: unbind", "DAV: write", "DAV: write-content", "DAV: write-properties"}},
	} {
		got := currentUserPrivileges(t, tc.readOnly)
		if strings.Join(got, ", ") != strings.Join(tc.want, ", ") {
			t.Errorf("readOnly=%v: privileges = %v, want %v", tc.readOnly, got, tc.want)
		}
	}
}

// RFC 3744 section 5.4: "Aggregate privileges and their contained privileges
// are listed", and each one is a non-abstract privilege of
// DAV:supported-privilege-set.
func TestNewCurrentUserPrivilegeSetAggregates(t *testing.T) {
	type supported struct {
		abstract bool
		contains []string // all the privileges it aggregates, recursively
	}
	tree := make(map[string]supported)
	var walk func(sps []SupportedPrivilege) []string
	walk = func(sps []SupportedPrivilege) []string {
		var all []string
		for i := range sps {
			sp := &sps[i]
			contains := walk(sp.SupportedPrivilege)
			for _, name := range privilegeNames(t, &sp.Privilege) {
				tree[name] = supported{abstract: sp.Abstract != nil, contains: contains}
				all = append(all, name)
			}
			all = append(all, contains...)
		}
		return all
	}
	walk(NewSupportedPrivilegeSet().SupportedPrivilege)

	for _, readOnly := range []bool{false, true} {
		listed := make(map[string]bool)
		for _, name := range currentUserPrivileges(t, readOnly) {
			listed[name] = true
		}
		for name := range listed {
			sp, ok := tree[name]
			if !ok || sp.abstract {
				t.Errorf("readOnly=%v: %v isn't a non-abstract supported privilege", readOnly, name)
			}
			for _, c := range sp.contains {
				if !tree[c].abstract && !listed[c] {
					t.Errorf("readOnly=%v: %v is listed without %v, which it aggregates", readOnly, name, c)
				}
			}
		}
	}
}

// Clients detect read-only collections with HasWrite, whether the server
// lists each privilege in its own DAV:privilege element, as RFC 3744 section
// 5.4 asks, or packs them into one, as some servers do.
func TestCurrentUserPrivilegeSetHasWrite(t *testing.T) {
	for _, tc := range []struct {
		privileges string
		want       bool
	}{
		{"", false},
		{"<D:privilege><D:read/></D:privilege>", false},
		{"<D:privilege><D:read/></D:privilege><D:privilege><D:write/></D:privilege>", true},
		{"<D:privilege><D:read/><D:write/></D:privilege>", true},
		{"<D:privilege><D:read/></D:privilege><D:privilege><D:write-content/></D:privilege>", true},
		{"<D:privilege><D:all/></D:privilege>", true},
	} {
		var set CurrentUserPrivilegeSet
		if err := xml.Unmarshal([]byte(`<D:current-user-privilege-set xmlns:D="DAV:">`+tc.privileges+`</D:current-user-privilege-set>`), &set); err != nil {
			t.Fatal(err)
		}
		if got := set.HasWrite(); got != tc.want {
			t.Errorf("HasWrite() = %v for %q, want %v", got, tc.privileges, tc.want)
		}
	}

	for _, readOnly := range []bool{false, true} {
		b, err := xml.Marshal(NewCurrentUserPrivilegeSet(readOnly))
		if err != nil {
			t.Fatal(err)
		}
		var set CurrentUserPrivilegeSet
		if err := xml.Unmarshal(b, &set); err != nil {
			t.Fatal(err)
		}
		if got := set.HasWrite(); got != !readOnly {
			t.Errorf("readOnly=%v: HasWrite() = %v for %s", readOnly, got, b)
		}
	}
}

// RFC 3744 section 5.1: DAV:owner is empty if there's no owner information,
// for instance for a request without a current user (RFC 5397 section 3). An
// empty DAV:href isn't a URL.
func TestAddACLPropsPrincipal(t *testing.T) {
	for _, tc := range []struct {
		principalPath                 string
		owner, principalCollectionSet string
	}{
		{"/principals/alice/",
			`<owner xmlns="DAV:"><href>/principals/alice/</href></owner>`,
			`<principal-collection-set xmlns="DAV:"><href>/principals/</href></principal-collection-set>`},
		{"",
			`<owner xmlns="DAV:"></owner>`,
			`<principal-collection-set xmlns="DAV:"></principal-collection-set>`},
	} {
		props := make(map[xml.Name]PropFindFunc)
		AddACLProps(props, func() (string, error) { return tc.principalPath, nil })
		for name, want := range map[xml.Name]string{
			OwnerName:                  tc.owner,
			PrincipalCollectionSetName: tc.principalCollectionSet,
		} {
			v, err := props[name](nil)
			if err != nil {
				t.Fatalf("%v: %v", name.Local, err)
			}
			b, err := xml.Marshal(v)
			if err != nil {
				t.Fatal(err)
			}
			if string(b) != want {
				t.Errorf("principal %q: %v = %s, want %s", tc.principalPath, name.Local, b, want)
			}
		}
	}
}
