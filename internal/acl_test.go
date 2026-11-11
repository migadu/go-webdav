package internal

import (
	"encoding/xml"
	"fmt"
	"strings"
	"testing"
)

func decodePropInsideMultiStatus(data []byte, v interface{}) error {
	var ms MultiStatus
	err := xml.Unmarshal(data, &ms)
	if err != nil {
		return err
	}
	if len(ms.Responses) != 1 {
		return fmt.Errorf("expected 1 <response>, got %d", len(ms.Responses))
	}
	return ms.Responses[0].DecodeProp(v)
}

func checkSupportedPrivilege(t *testing.T, sp SupportedPrivilege, privilege Privilege, abstract bool, description string, aggregated int) {
	t.Helper()

	got, err := xml.Marshal(&sp.Privilege)
	if err != nil {
		t.Fatal(err)
	}
	want, err := xml.Marshal(&privilege)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != string(want) {
		t.Errorf("expected %s, got %s", want, got)
	}
	if abstract != (sp.Abstract != nil) {
		t.Errorf("abstract = %v, want %v", sp.Abstract != nil, abstract)
	}
	if strings.TrimSpace(sp.Description.Text) != description {
		t.Errorf("expected description %q, got %q", description, strings.TrimSpace(sp.Description.Text))
	}
	if sp.Description.Lang != "en" {
		t.Errorf("expected lang %q, got %q", "en", sp.Description.Lang)
	}
	if len(sp.SupportedPrivilege) != aggregated {
		t.Fatalf("expected %d <supported-privilege>, got %d", aggregated, len(sp.SupportedPrivilege))
	}
}

var granted = &struct{}{}

/* rfc3744#section-5.1.1 */
func TestOwnerUnmarshal(t *testing.T) {
	var owner Owner
	err := decodePropInsideMultiStatus([]byte(`<?xml version="1.0" encoding="utf-8" ?>
   <D:multistatus xmlns:D="DAV:">
     <D:response>
       <D:href>http://www.example.com/papers/</D:href>
       <D:propstat>
         <D:prop>
           <D:owner>
             <D:href>http://www.example.com/acl/users/gstein</D:href>
           </D:owner>
         </D:prop>
         <D:status>HTTP/1.1 200 OK</D:status>
       </D:propstat>
     </D:response>
   </D:multistatus>`), &owner)
	if err != nil {
		t.Fatal(err)
	}
	if owner.Href.String() != "http://www.example.com/acl/users/gstein" {
		t.Fatalf("expected http://www.example.com/acl/users/gstein, got %s", owner.Href.String())
	}
}

/* rfc3744#section-5.3.1 */
func TestSupportedPrivilegeSetUnmarshal(t *testing.T) {
	var sps SupportedPrivilegeSet
	err := decodePropInsideMultiStatus([]byte(`<?xml version="1.0" encoding="utf-8" ?>
   <D:multistatus xmlns:D="DAV:">
     <D:response>
       <D:href>http://www.example.com/papers/</D:href>
       <D:propstat>
         <D:prop>
           <D:supported-privilege-set>
             <D:supported-privilege>
               <D:privilege><D:all/></D:privilege>
              <D:abstract/>
               <D:description xml:lang="en">
                 Any operation
               </D:description>
               <D:supported-privilege>
                 <D:privilege><D:read/></D:privilege>
                 <D:description xml:lang="en">
                   Read any object
                 </D:description>
                 <D:supported-privilege>
                   <D:privilege><D:read-acl/></D:privilege>
                   <D:abstract/>
                   <D:description xml:lang="en">Read ACL</D:description>
                 </D:supported-privilege>
                 <D:supported-privilege>
                   <D:privilege>
                     <D:read-current-user-privilege-set/>
                   </D:privilege>
                   <D:abstract/>
                   <D:description xml:lang="en">
                     Read current user privilege set property
                   </D:description>
                 </D:supported-privilege>
               </D:supported-privilege>
               <D:supported-privilege>
                 <D:privilege><D:write/></D:privilege>
                 <D:description xml:lang="en">
                   Write any object
                 </D:description>
                 <D:supported-privilege>
                   <D:privilege><D:write-acl/></D:privilege>
                   <D:description xml:lang="en">
                     Write ACL
                   </D:description>
                   <D:abstract/>
                 </D:supported-privilege>
                 <D:supported-privilege>
                   <D:privilege><D:write-properties/></D:privilege>
                   <D:description xml:lang="en">
                     Write properties
                   </D:description>
                 </D:supported-privilege>
                 <D:supported-privilege>
                   <D:privilege><D:write-content/></D:privilege>
                   <D:description xml:lang="en">
                     Write resource content
                   </D:description>
                 </D:supported-privilege>
               </D:supported-privilege>
               <D:supported-privilege>
                 <D:privilege><D:unlock/></D:privilege>
                 <D:description xml:lang="en">
                   Unlock resource
                 </D:description>
               </D:supported-privilege>
             </D:supported-privilege>
           </D:supported-privilege-set>
         </D:prop>
         <D:status>HTTP/1.1 200 OK</D:status>
       </D:propstat>
     </D:response>
   </D:multistatus>`), &sps)
	if err != nil {
		t.Fatal(err)
	}
	if len(sps.SupportedPrivilege) != 1 {
		t.Fatalf("expected 1 <supported-privilege>, got %d", len(sps.SupportedPrivilege))
	}
	sp := sps.SupportedPrivilege[0]

	checkSupportedPrivilege(t, sp, Privilege{All: granted}, true, "Any operation", 3)

	checkSupportedPrivilege(t, sp.SupportedPrivilege[0], Privilege{Read: granted}, false, "Read any object", 2)
	checkSupportedPrivilege(t, sp.SupportedPrivilege[1], Privilege{Write: granted}, false, "Write any object", 3)
	checkSupportedPrivilege(t, sp.SupportedPrivilege[2], Privilege{Unlock: granted}, false, "Unlock resource", 0)

	checkSupportedPrivilege(t, sp.SupportedPrivilege[0].SupportedPrivilege[0], Privilege{ReadACL: granted}, true, "Read ACL", 0)
	checkSupportedPrivilege(t, sp.SupportedPrivilege[0].SupportedPrivilege[1], Privilege{ReadCurrentUserPrivilegeSet: granted}, true, "Read current user privilege set property", 0)

	checkSupportedPrivilege(t, sp.SupportedPrivilege[1].SupportedPrivilege[0], Privilege{WriteACL: granted}, true, "Write ACL", 0)
	checkSupportedPrivilege(t, sp.SupportedPrivilege[1].SupportedPrivilege[1], Privilege{WriteProperties: granted}, false, "Write properties", 0)
	checkSupportedPrivilege(t, sp.SupportedPrivilege[1].SupportedPrivilege[2], Privilege{WriteContent: granted}, false, "Write resource content", 0)
}

func TestSupportedPrivilegeMarshal(t *testing.T) {
	for _, tc := range []struct {
		sp   SupportedPrivilege
		want string
	}{
		{
			SupportedPrivilege{
				Privilege:   Privilege{All: granted},
				Abstract:    granted,
				Description: Description{Lang: "en", Text: "Any operation"},
			},
			`<supported-privilege xmlns="DAV:"><privilege xmlns="DAV:"><all xmlns="DAV:"></all></privilege><abstract xmlns="DAV:"></abstract><description xmlns="DAV:" xml:lang="en">Any operation</description></supported-privilege>`,
		},
		{
			NewSupportedPrivilege(Privilege{Write: granted}, "Write any object",
				NewSupportedPrivilege(Privilege{WriteContent: granted}, "Write resource content"),
			),
			`<supported-privilege xmlns="DAV:"><privilege xmlns="DAV:"><write xmlns="DAV:"></write></privilege><description xmlns="DAV:" xml:lang="en">Write any object</description>` +
				`<supported-privilege xmlns="DAV:"><privilege xmlns="DAV:"><write-content xmlns="DAV:"></write-content></privilege><description xmlns="DAV:" xml:lang="en">Write resource content</description></supported-privilege>` +
				`</supported-privilege>`,
		},
	} {
		buf, err := xml.Marshal(&tc.sp)
		if err != nil {
			t.Fatal(err)
		}
		if string(buf) != tc.want {
			t.Errorf("expected %s, got %s", tc.want, buf)
		}
	}
}

func TestNewPrincipalCollectionSet(t *testing.T) {
	for _, tc := range []struct {
		principalPath string
		want          []string
	}{
		{"/principals/users/alice/", []string{"/principals/users/"}},
		{"/principals/users/alice", []string{"/principals/users/"}},
		{"/alice/", []string{"/"}},
		{"/", nil},
		{"", nil},
	} {
		set := NewPrincipalCollectionSet(tc.principalPath)
		var got []string
		for _, href := range set.Hrefs {
			got = append(got, href.String())
		}
		if strings.Join(got, " ") != strings.Join(tc.want, " ") {
			t.Errorf("NewPrincipalCollectionSet(%q) = %v, want %v", tc.principalPath, got, tc.want)
		}
	}
}
