package carddav

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"encoding/base64"
	"encoding/xml"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/emersion/go-webdav"
	"github.com/emersion/go-webdav/internal"
)

const pushNamespace = "https://bitfire.at/webdav-push"

var pushPropNames = []xml.Name{
	{Space: pushNamespace, Local: "transports"},
	{Space: pushNamespace, Local: "topic"},
	{Space: pushNamespace, Local: "supported-triggers"},
}

// pushTestBackend is a syncTestBackend supporting WebDAV-Push for the
// principal, the address book home set and the address book. Registration
// URLs are under /push/.
type pushTestBackend struct {
	*syncTestBackend
	info map[string]*webdav.PushInfo

	mu            sync.Mutex
	infoCalls     []string
	registers     []webdav.PushSubscription
	registrations map[string]bool
	deleted       []string
}

func newPushTestBackend(t *testing.T) *pushTestBackend {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	return &pushTestBackend{
		syncTestBackend: newSyncTestBackend(),
		info: map[string]*webdav.PushInfo{
			"/user/": {
				Topic:          "principal-topic",
				VAPIDPublicKey: &key.PublicKey,
				PropertyUpdate: &webdav.PushTrigger{Depth: webdav.DepthZero},
			},
			"/user/contacts/": {
				Topic:          "home-topic",
				VAPIDPublicKey: &key.PublicKey,
				ContentUpdate:  &webdav.PushTrigger{Depth: webdav.DepthOne},
			},
			syncAddressBookPath: {
				Topic:          "addressbook-topic",
				VAPIDPublicKey: &key.PublicKey,
				ContentUpdate:  &webdav.PushTrigger{Depth: webdav.DepthOne},
				PropertyUpdate: &webdav.PushTrigger{Depth: webdav.DepthZero},
			},
		},
		registrations: map[string]bool{"io6Efei4ooph": true},
	}
}

func (b *pushTestBackend) PushInfo(ctx context.Context, path string) (*webdav.PushInfo, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.infoCalls = append(b.infoCalls, path)
	return b.info[path], nil
}

func (b *pushTestBackend) RegisterPush(ctx context.Context, path string, sub *webdav.PushSubscription) (*webdav.PushRegistration, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.registers = append(b.registers, *sub)
	return &webdav.PushRegistration{
		URL:     "https://dav.example.com/push/io6Efei4ooph",
		Expires: time.Date(2024, 10, 2, 7, 28, 0, 0, time.UTC),
	}, nil
}

func (b *pushTestBackend) UnregisterPush(ctx context.Context, path string) (bool, error) {
	id, ok := strings.CutPrefix(path, "/push/")
	if !ok {
		return false, nil
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if !b.registrations[id] {
		return true, webdav.NewHTTPError(http.StatusNotFound, nil)
	}
	delete(b.registrations, id)
	return true, nil
}

func (b *pushTestBackend) DeleteAddressObject(ctx context.Context, path string) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.deleted = append(b.deleted, path)
	return nil
}

func (b *pushTestBackend) pushInfoCalls() []string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return append([]string(nil), b.infoCalls...)
}

const propFindPushProps = `<?xml version="1.0" encoding="utf-8" ?>
<propfind xmlns="DAV:" xmlns:P="https://bitfire.at/webdav-push">
  <prop>
    <P:transports/>
    <P:topic/>
    <P:supported-triggers/>
  </prop>
</propfind>`

type testPushTopic struct {
	XMLName xml.Name `xml:"https://bitfire.at/webdav-push topic"`
	Topic   string   `xml:",chardata"`
}

type testPushTransports struct {
	XMLName xml.Name `xml:"https://bitfire.at/webdav-push transports"`
	WebPush []struct {
		VAPIDPublicKey []struct {
			Type string `xml:"type,attr"`
			Key  string `xml:",chardata"`
		} `xml:"https://bitfire.at/webdav-push vapid-public-key"`
	} `xml:"https://bitfire.at/webdav-push web-push"`
}

type testPushDepth struct {
	Depth string `xml:"DAV: depth"`
}

type testSupportedTriggers struct {
	XMLName        xml.Name        `xml:"https://bitfire.at/webdav-push supported-triggers"`
	ContentUpdate  []testPushDepth `xml:"https://bitfire.at/webdav-push content-update"`
	PropertyUpdate []testPushDepth `xml:"https://bitfire.at/webdav-push property-update"`
}

func pushPropFind(t *testing.T, b Backend, target, depth, body, href string) *internal.Response {
	t.Helper()
	w := serveCardDAV(b, "PROPFIND", target, depth, body)
	if w.Code != http.StatusMultiStatus {
		t.Fatalf("PROPFIND %v: status = %v, want 207; body:\n%s", target, w.Code, w.Body.String())
	}
	var ms internal.MultiStatus
	if err := xml.NewDecoder(w.Body).Decode(&ms); err != nil {
		t.Fatalf("PROPFIND %v: failed to decode multistatus: %v", target, err)
	}
	for i := range ms.Responses {
		for _, h := range ms.Responses[i].Hrefs {
			if h.Path == href {
				return &ms.Responses[i]
			}
		}
	}
	t.Fatalf("PROPFIND %v: no response for %v", target, href)
	return nil
}

func checkPushProps(t *testing.T, resp *internal.Response, info *webdav.PushInfo) {
	t.Helper()
	var topic testPushTopic
	if err := resp.DecodeProp(&topic); err != nil {
		t.Fatalf("topic: %v", err)
	}
	if topic.Topic != info.Topic {
		t.Errorf("topic = %q, want %q", topic.Topic, info.Topic)
	}

	var transports testPushTransports
	if err := resp.DecodeProp(&transports); err != nil {
		t.Fatalf("transports: %v", err)
	}
	want, _ := info.VAPIDPublicKey.Bytes()
	if len(transports.WebPush) != 1 || len(transports.WebPush[0].VAPIDPublicKey) != 1 ||
		transports.WebPush[0].VAPIDPublicKey[0].Type != "p256ecdsa" ||
		transports.WebPush[0].VAPIDPublicKey[0].Key != base64.RawURLEncoding.EncodeToString(want) {
		t.Errorf("transports = %+v, want web-push with the VAPID key", transports)
	}

	var triggers testSupportedTriggers
	if err := resp.DecodeProp(&triggers); err != nil {
		t.Fatalf("supported-triggers: %v", err)
	}
	var wantContent, wantProp []testPushDepth
	if info.ContentUpdate != nil {
		wantContent = []testPushDepth{{info.ContentUpdate.Depth.String()}}
	}
	if info.PropertyUpdate != nil {
		wantProp = []testPushDepth{{info.PropertyUpdate.Depth.String()}}
	}
	if !reflect.DeepEqual(triggers.ContentUpdate, wantContent) || !reflect.DeepEqual(triggers.PropertyUpdate, wantProp) {
		t.Errorf("supported-triggers = %+v, want content-update %v and property-update %v", triggers, wantContent, wantProp)
	}
}

func checkNoPushProps(t *testing.T, resp *internal.Response) {
	t.Helper()
	for _, name := range pushPropNames {
		for _, propstat := range resp.PropStats {
			if propstat.Prop.Get(name) != nil && propstat.Status.Code != http.StatusNotFound {
				t.Errorf("%v: got status %v, want 404", name.Local, propstat.Status.Code)
			}
		}
	}
}

func TestPushPropFind(t *testing.T) {
	b := newPushTestBackend(t)

	resp := pushPropFind(t, b, syncAddressBookPath, "0", propFindPushProps, syncAddressBookPath)
	checkPushProps(t, resp, b.info[syncAddressBookPath])

	for _, p := range []string{"/user/contacts/", syncAddressBookPath} {
		resp := pushPropFind(t, b, "/user/contacts/", "1", propFindPushProps, p)
		checkPushProps(t, resp, b.info[p])
	}

	resp = pushPropFind(t, b, "/user/", "0", propFindPushProps, "/user/")
	checkPushProps(t, resp, b.info["/user/"])

	// Address objects aren't WebDAV-Push collections
	objPath := syncAddressBookPath + "/one.vcf"
	before := len(b.pushInfoCalls())
	resp = pushPropFind(t, b, objPath, "0", propFindPushProps, objPath)
	checkNoPushProps(t, resp)
	if calls := b.pushInfoCalls()[before:]; len(calls) != 0 {
		t.Errorf("PushInfo called for %v", calls)
	}

	// Push properties aren't returned by allprop requests
	w := serveCardDAV(b, "PROPFIND", syncAddressBookPath, "0", `<propfind xmlns="DAV:"><allprop/></propfind>`)
	if strings.Contains(w.Body.String(), pushNamespace) {
		t.Errorf("allprop returned push properties:\n%s", w.Body.String())
	}
}

func pushOptions(t *testing.T, b Backend, path string) (dav []string, allow map[string]bool) {
	t.Helper()
	w := serveCardDAV(b, http.MethodOptions, path, "", "")
	if w.Code != http.StatusNoContent {
		t.Fatalf("OPTIONS %v: status = %v, want 204", path, w.Code)
	}
	for _, v := range strings.Split(w.Header().Get("DAV"), ",") {
		dav = append(dav, strings.TrimSpace(v))
	}
	allow = make(map[string]bool)
	for _, v := range strings.Split(w.Header().Get("Allow"), ",") {
		allow[strings.TrimSpace(v)] = true
	}
	return dav, allow
}

func TestPushOptions(t *testing.T) {
	b := newPushTestBackend(t)
	for _, tc := range []struct {
		path string
		push bool
	}{
		{syncAddressBookPath, true},
		{"/user/contacts/", true},
		{"/user/", true},
		{"/user/contacts/other", false},
		{syncAddressBookPath + "/one.vcf", false},
	} {
		dav, allow := pushOptions(t, b, tc.path)
		hasPush := false
		for _, v := range dav {
			if v == "webdav-push" {
				hasPush = true
			}
		}
		if hasPush != tc.push {
			t.Errorf("OPTIONS %v: DAV = %v, webdav-push listed: %v, want %v", tc.path, dav, hasPush, tc.push)
		}
		if allow["POST"] != tc.push {
			t.Errorf("OPTIONS %v: POST allowed: %v, want %v", tc.path, allow["POST"], tc.push)
		}
	}
}

const pushRegisterRequest = `<?xml version="1.0" encoding="utf-8" ?>
<push-register xmlns="https://bitfire.at/webdav-push" xmlns:D="DAV:">
  <subscription>
    <web-push-subscription>
      <push-resource>https://up.example.net/yohd4yai5Phiz1wi</push-resource>
      <content-encoding>aes128gcm</content-encoding>
      <subscription-public-key type="p256dh">BCVxsr7N_eNgVRqvHtD0zTZsEc6-VV-JvLexhqUzORcxaOzi6-AYWXvTBHm4bjyPjs7Vd8pZGH6SRpkNtoIAiw4</subscription-public-key>
      <auth-secret>BTBZMqHH6r4Tts7J_aSIgg</auth-secret>
    </web-push-subscription>
  </subscription>
  <trigger>
    <content-update>
      <D:depth>1</D:depth>
    </content-update>
  </trigger>
  <expires>Wed, 20 Dec 2023 10:03:31 GMT</expires>
</push-register>`

func checkPushPrecondition(t *testing.T, w *httptest.ResponseRecorder, name string) {
	t.Helper()
	if w.Code != http.StatusForbidden {
		t.Fatalf("status = %v, want 403; body:\n%s", w.Code, w.Body.String())
	}
	var davErr internal.Error
	if err := xml.NewDecoder(w.Body).Decode(&davErr); err != nil {
		t.Fatalf("failed to decode DAV:error: %v", err)
	}
	want := xml.Name{Space: pushNamespace, Local: name}
	for _, raw := range davErr.Raw {
		if n, ok := raw.XMLName(); ok && n == want {
			return
		}
	}
	t.Errorf("DAV:error = %v, want the %v precondition", davErr.Error(), name)
}

func TestPushRegister(t *testing.T) {
	b := newPushTestBackend(t)
	w := serveCardDAV(b, http.MethodPost, syncAddressBookPath, "", pushRegisterRequest)
	if w.Code != http.StatusNoContent {
		t.Fatalf("status = %v, want 204; body:\n%s", w.Code, w.Body.String())
	}
	if got, want := w.Header().Get("Location"), "https://dav.example.com/push/io6Efei4ooph"; got != want {
		t.Errorf("Location = %q, want %q", got, want)
	}
	if got, want := w.Header().Get("Expires"), "Wed, 02 Oct 2024 07:28:00 GMT"; got != want {
		t.Errorf("Expires = %q, want %q", got, want)
	}
	if len(b.registers) != 1 {
		t.Fatalf("RegisterPush called %d times, want 1", len(b.registers))
	}
	sub := b.registers[0]
	if sub.WebPush.PushResource != "https://up.example.net/yohd4yai5Phiz1wi" || len(sub.WebPush.PublicKey) != 65 || len(sub.WebPush.AuthSecret) != 16 {
		t.Errorf("WebPush = %+v", sub.WebPush)
	}
	if want := (&webdav.PushTrigger{Depth: webdav.DepthOne}); !reflect.DeepEqual(sub.ContentUpdate, want) || sub.PropertyUpdate != nil {
		t.Errorf("triggers = %+v, %+v, want content update %+v only", sub.ContentUpdate, sub.PropertyUpdate, want)
	}
	if want := time.Date(2023, 12, 20, 10, 3, 31, 0, time.UTC); !sub.Expires.Equal(want) {
		t.Errorf("Expires = %v, want %v", sub.Expires, want)
	}
}

func TestPushRegisterErrors(t *testing.T) {
	b := newPushTestBackend(t)

	if w := serveCardDAV(b, http.MethodPost, syncAddressBookPath, "", "<push-register"); w.Code != http.StatusBadRequest {
		t.Errorf("malformed XML: status = %v, want 400", w.Code)
	}
	checkPushPrecondition(t, serveCardDAV(b, http.MethodPost, syncAddressBookPath, "",
		strings.Replace(pushRegisterRequest, "https://up.example.net", "http://up.example.net", 1)), "invalid-subscription")
	// The principal only supports property updates
	checkPushPrecondition(t, serveCardDAV(b, http.MethodPost, "/user/", "", pushRegisterRequest), "no-supported-trigger")
	for _, p := range []string{"/user/contacts/other", syncAddressBookPath + "/one.vcf"} {
		checkPushPrecondition(t, serveCardDAV(b, http.MethodPost, p, "", pushRegisterRequest), "push-not-available")
	}
	if len(b.registers) != 0 {
		t.Errorf("RegisterPush called %d times, want none", len(b.registers))
	}
}

func TestPushUnregister(t *testing.T) {
	b := newPushTestBackend(t)

	if w := serveCardDAV(b, http.MethodDelete, "/push/io6Efei4ooph", "", ""); w.Code != http.StatusNoContent {
		t.Errorf("DELETE registration: status = %v, want 204", w.Code)
	}
	if w := serveCardDAV(b, http.MethodDelete, "/push/io6Efei4ooph", "", ""); w.Code != http.StatusNotFound {
		t.Errorf("DELETE unknown registration: status = %v, want 404", w.Code)
	}
	objPath := syncAddressBookPath + "/one.vcf"
	if w := serveCardDAV(b, http.MethodDelete, objPath, "", ""); w.Code != http.StatusNoContent {
		t.Errorf("DELETE address object: status = %v, want 204", w.Code)
	}
	if want := []string{objPath}; !reflect.DeepEqual(b.deleted, want) {
		t.Errorf("deleted %v, want %v", b.deleted, want)
	}
}

// A backend which doesn't implement PushBackend behaves as before.
func TestPushNotImplemented(t *testing.T) {
	b := newSyncTestBackend()

	resp := pushPropFind(t, b, syncAddressBookPath, "0", propFindPushProps, syncAddressBookPath)
	for _, name := range pushPropNames {
		found := false
		for _, propstat := range resp.PropStats {
			if propstat.Prop.Get(name) != nil {
				found = true
				if propstat.Status.Code != http.StatusNotFound {
					t.Errorf("%v: status = %v, want 404", name.Local, propstat.Status.Code)
				}
			}
		}
		if !found {
			t.Errorf("%v not reported", name.Local)
		}
	}

	dav, allow := pushOptions(t, b, syncAddressBookPath)
	if want := []string{"1", "3", "addressbook"}; !reflect.DeepEqual(dav, want) {
		t.Errorf("DAV = %v, want %v", dav, want)
	}
	if allow["POST"] {
		t.Errorf("POST allowed")
	}

	if w := serveCardDAV(b, http.MethodPost, syncAddressBookPath, "", pushRegisterRequest); w.Code != http.StatusMethodNotAllowed {
		t.Errorf("POST: status = %v, want 405", w.Code)
	}
	// The path of a registration URL is no address book or address object
	if w := serveCardDAV(b, http.MethodDelete, "/push/io6Efei4ooph", "", ""); w.Code != http.StatusForbidden {
		t.Errorf("DELETE: status = %v, want 403", w.Code)
	}
}
