package internal

import (
	"encoding/xml"
	"net/http"
	"testing"
)

// RFC 4918 section 9.1: allprop returns the dead properties and the live ones
// RFC 4918 defines, plus those named in an include element.
func TestNewPropFindResponseAllProp(t *testing.T) {
	calendarDescriptionName := xml.Name{"urn:ietf:params:xml:ns:caldav", "calendar-description"}
	maxResourceSizeName := xml.Name{"urn:ietf:params:xml:ns:caldav", "max-resource-size"}
	props := func() map[xml.Name]PropFindFunc {
		return map[xml.Name]PropFindFunc{
			DisplayNameName:          PropFindValue(&DisplayName{Name: "Work"}),
			CurrentUserPrincipalName: PropFindValue(&CurrentUserPrincipal{Href: &Href{Path: "/user/"}}),
			calendarDescriptionName:  PropFindValue(NewRawXMLElement(calendarDescriptionName, nil, nil)),
			maxResourceSizeName:      PropFindValue(NewRawXMLElement(maxResourceSizeName, nil, nil)),
		}
	}

	for _, tc := range []struct {
		name     string
		propfind PropFind
		want     []xml.Name
	}{
		{
			name:     "allprop",
			propfind: PropFind{AllProp: &struct{}{}},
			want:     []xml.Name{ResourceTypeName, DisplayNameName},
		},
		{
			name: "allprop with include",
			propfind: PropFind{
				AllProp: &struct{}{},
				Include: &Include{Raw: []RawXMLValue{*NewRawXMLElement(calendarDescriptionName, nil, nil)}},
			},
			want: []xml.Name{ResourceTypeName, DisplayNameName, calendarDescriptionName},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			resp, err := NewPropFindResponse("/user/calendars/work/", &tc.propfind, props())
			if err != nil {
				t.Fatal(err)
			}
			got := make(map[xml.Name]bool)
			for _, propstat := range resp.PropStats {
				if propstat.Status.Code != http.StatusOK {
					t.Errorf("unexpected propstat status %v", propstat.Status.Code)
				}
				for _, raw := range propstat.Prop.Raw {
					b, err := xml.Marshal(&raw)
					if err != nil {
						t.Fatal(err)
					}
					var v RawXMLValue
					if err := xml.Unmarshal(b, &v); err != nil {
						t.Fatal(err)
					}
					name, _ := v.XMLName()
					got[name] = true
				}
			}
			if len(got) != len(tc.want) {
				t.Errorf("got properties %v, want %v", got, tc.want)
			}
			for _, name := range tc.want {
				if !got[name] {
					t.Errorf("property %v missing, got %v", name, got)
				}
			}
		})
	}
}
