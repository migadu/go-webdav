package webdav

import (
	"encoding/xml"
	"errors"
	"net/http"
	"reflect"
	"strings"
	"testing"

	"github.com/emersion/go-webdav/internal"
)

// testPushMessage decodes a push-message element (draft-bitfire-webdav-push
// section 5.1) to compare push messages regardless of how they're encoded.
type testPushMessage struct {
	XMLName        xml.Name                 `xml:"https://bitfire.at/webdav-push push-message"`
	Topics         []string                 `xml:"https://bitfire.at/webdav-push topic"`
	ContentUpdates []testPushContentUpdate  `xml:"https://bitfire.at/webdav-push content-update"`
	PropUpdates    []testPushPropertyUpdate `xml:"https://bitfire.at/webdav-push property-update"`
}

type testPushContentUpdate struct {
	SyncTokens []string `xml:"DAV: sync-token"`
}

type testPushPropertyUpdate struct {
	Props []internal.Prop `xml:"DAV: prop"`
}

func (msg *testPushMessage) propNames() [][]xml.Name {
	var l [][]xml.Name
	for _, pu := range msg.PropUpdates {
		for _, prop := range pu.Props {
			names := []xml.Name{}
			for _, raw := range prop.Raw {
				if name, ok := raw.XMLName(); ok {
					names = append(names, name)
				}
			}
			l = append(l, names)
		}
	}
	return l
}

func decodeTestPushMessage(t *testing.T, data string) *testPushMessage {
	t.Helper()
	var msg testPushMessage
	if err := xml.Unmarshal([]byte(data), &msg); err != nil {
		t.Fatalf("failed to decode push message: %v\n%s", err, data)
	}
	return &msg
}

// The sample push message of the draft (xml/sample-push-message.xml).
const draftSamplePushMessage = `<?xml version="1.0" encoding="utf-8" ?>
<push-message xmlns="https://bitfire.at/webdav-push" xmlns:D="DAV:">
  <topic>O7M1nQ7cKkKTKsoS_j6Z3w</topic>
  <content-update>
    <D:sync-token>http://example.com/sync/10</D:sync-token>
  </content-update>
  <property-update />
</push-message>`

// The VAPID key rotation message of the draft
// (xml/push-message-vapid-key.xml).
const draftKeyRotationPushMessage = `<?xml version="1.0" encoding="utf-8" ?>
<push-message xmlns="https://bitfire.at/webdav-push" xmlns:D="DAV:">
  <property-update>
    <D:prop>
      <transports />
    </D:prop>
  </property-update>
</push-message>`

func TestPushMessageMarshalXML(t *testing.T) {
	for _, tc := range []struct {
		name string
		msg  *PushMessage
		want string
	}{
		{
			name: "draft sample",
			msg: &PushMessage{
				Topic:          "O7M1nQ7cKkKTKsoS_j6Z3w",
				ContentUpdate:  true,
				SyncToken:      "http://example.com/sync/10",
				PropertyUpdate: true,
			},
			want: draftSamplePushMessage,
		},
		{
			name: "key rotation",
			msg:  NewVAPIDKeyRotationMessage(),
			want: draftKeyRotationPushMessage,
		},
		{
			name: "content update without sync token",
			msg:  &PushMessage{Topic: "abc", ContentUpdate: true},
			want: `<P:push-message xmlns:P="https://bitfire.at/webdav-push"><P:topic>abc</P:topic><P:content-update/></P:push-message>`,
		},
		{
			name: "property update with properties",
			msg: &PushMessage{
				Topic:          "abc",
				PropertyUpdate: true,
				Props:          []xml.Name{{Space: "DAV:", Local: "displayname"}},
			},
			want: `<push-message xmlns="https://bitfire.at/webdav-push"><topic>abc</topic><property-update><prop xmlns="DAV:"><displayname/></prop></property-update></push-message>`,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			b, err := xml.Marshal(tc.msg)
			if err != nil {
				t.Fatalf("xml.Marshal() = %v", err)
			}
			got := decodeTestPushMessage(t, string(b))
			want := decodeTestPushMessage(t, tc.want)
			// Ignore the whitespace of the reference documents
			for i := range want.Topics {
				want.Topics[i] = strings.TrimSpace(want.Topics[i])
			}
			for i := range want.ContentUpdates {
				for j := range want.ContentUpdates[i].SyncTokens {
					want.ContentUpdates[i].SyncTokens[j] = strings.TrimSpace(want.ContentUpdates[i].SyncTokens[j])
				}
			}
			if !reflect.DeepEqual(got.Topics, want.Topics) {
				t.Errorf("topics = %q, want %q", got.Topics, want.Topics)
			}
			if !reflect.DeepEqual(got.ContentUpdates, want.ContentUpdates) {
				t.Errorf("content updates = %+v, want %+v", got.ContentUpdates, want.ContentUpdates)
			}
			if len(got.PropUpdates) != len(want.PropUpdates) {
				t.Errorf("%d property updates, want %d", len(got.PropUpdates), len(want.PropUpdates))
			}
			if !reflect.DeepEqual(got.propNames(), want.propNames()) {
				t.Errorf("property update props = %v, want %v", got.propNames(), want.propNames())
			}
		})
	}
}

func TestParsePushDontNotify(t *testing.T) {
	const (
		url1 = "https://example.com/webdav/subscriptions/io6Efei4ooph"
		url2 = "https://example.com/webdav/subscriptions/ahSh7aiv"
	)
	for _, tc := range []struct {
		name   string
		values []string
		all    bool
		urls   []string
	}{
		{name: "missing"},
		{name: "single", values: []string{`"` + url1 + `"`}, urls: []string{url1}},
		{name: "list", values: []string{`"` + url1 + `", "` + url2 + `"`}, urls: []string{url1, url2}},
		{name: "several lines", values: []string{`"` + url1 + `"`, ` "` + url2 + `" `}, urls: []string{url1, url2}},
		{name: "empty list elements", values: []string{`, "` + url1 + `",, `}, urls: []string{url1}},
		{name: "asterisk", values: []string{"*"}, all: true},
		{name: "asterisk with whitespace", values: []string{" * "}, all: true},
		{name: "comma in quoted string", values: []string{`"https://example.com/a,b"`}, urls: []string{"https://example.com/a,b"}},
		{name: "quoted pair", values: []string{`"https:\/\/example.com\/sub"`}, urls: []string{"https://example.com/sub"}},
		{
			// A server MUST ignore invalid values
			name:   "invalid values",
			values: []string{url1 + `, "relative/path", "` + url2 + `" junk, "mailto:x@example.com", "https://example.com/ok"`, `"unterminated`},
			urls:   []string{"https://example.com/ok"},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := make(http.Header)
			for _, v := range tc.values {
				h.Add("Push-Dont-Notify", v)
			}
			all, urls := ParsePushDontNotify(h)
			if all != tc.all {
				t.Errorf("all = %v, want %v", all, tc.all)
			}
			if !reflect.DeepEqual(urls, tc.urls) {
				t.Errorf("urls = %q, want %q", urls, tc.urls)
			}
		})
	}
}

func TestNewPushPreconditionError(t *testing.T) {
	for _, p := range []PushPreconditionType{
		PushPreconditionInvalidSubscription,
		PushPreconditionPushNotAvailable,
		PushPreconditionNoSupportedTrigger,
	} {
		err := NewPushPreconditionError(p)
		var httpErr *HTTPError
		if !errors.As(err, &httpErr) || httpErr.Code != http.StatusForbidden {
			t.Errorf("%v: got %v, want a 403 HTTPError", p, err)
		}
		var errElt *internal.Error
		if !errors.As(err, &errElt) || len(errElt.Raw) != 1 {
			t.Fatalf("%v: got %v, want a DAV:error with a single element", p, err)
		}
		want := xml.Name{Space: "https://bitfire.at/webdav-push", Local: string(p)}
		if name, _ := errElt.Raw[0].XMLName(); name != want {
			t.Errorf("%v: DAV:error holds %v, want %v", p, name, want)
		}
	}
}
