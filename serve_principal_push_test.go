package webdav_test

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"encoding/base64"
	"encoding/xml"
	"errors"
	"math/big"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/emersion/go-webdav"
	"github.com/emersion/go-webdav/caldav"
	"github.com/emersion/go-webdav/carddav"
	"github.com/emersion/go-webdav/internal"
)

const testPushNamespace = "https://bitfire.at/webdav-push"

var testPushPropNames = []xml.Name{
	{Space: testPushNamespace, Local: "transports"},
	{Space: testPushNamespace, Local: "topic"},
	{Space: testPushNamespace, Local: "supported-triggers"},
}

// One backend serves WebDAV-Push for the CalDAV and CardDAV servers and for
// the principal they share.
var (
	_ caldav.PushBackend  = webdav.PushBackend(nil)
	_ carddav.PushBackend = webdav.PushBackend(nil)
	_ webdav.PushBackend  = caldav.PushBackend(nil)
	_ webdav.PushBackend  = carddav.PushBackend(nil)
)

// principalPushBackend supports WebDAV-Push for testPrincipalPath, with
// property updates of depth 0, as the draft recommends for principals
// (draft-bitfire-webdav-push section 5). Registration URLs are under /push/.
type principalPushBackend struct {
	vapidKey *ecdsa.PrivateKey
	info     map[string]*webdav.PushInfo
	infoErr  error

	mu            sync.Mutex
	infoCalls     []string
	registerPaths []string
	registers     []webdav.PushSubscription
	registrations map[string]bool
}

func newPrincipalPushBackend(t *testing.T) *principalPushBackend {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	return &principalPushBackend{
		vapidKey: key,
		info: map[string]*webdav.PushInfo{
			testPrincipalPath: {
				Topic:          "principal-topic",
				VAPIDPublicKey: &key.PublicKey,
				PropertyUpdate: &webdav.PushTrigger{Depth: webdav.DepthZero},
			},
		},
		registrations: map[string]bool{"io6Efei4ooph": true},
	}
}

func (b *principalPushBackend) PushInfo(ctx context.Context, path string) (*webdav.PushInfo, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.infoCalls = append(b.infoCalls, path)
	if b.infoErr != nil {
		return nil, b.infoErr
	}
	return b.info[path], nil
}

func (b *principalPushBackend) RegisterPush(ctx context.Context, path string, sub *webdav.PushSubscription) (*webdav.PushRegistration, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.registerPaths = append(b.registerPaths, path)
	b.registers = append(b.registers, *sub)
	b.registrations["io6Efei4ooph"] = true
	return &webdav.PushRegistration{
		URL:     "/push/io6Efei4ooph",
		Expires: time.Date(2024, 10, 2, 7, 28, 0, 0, time.UTC),
	}, nil
}

func (b *principalPushBackend) UnregisterPush(ctx context.Context, path string) (bool, error) {
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

func newPushPrincipalOptions(b webdav.PushBackend) *webdav.ServePrincipalOptions {
	return &webdav.ServePrincipalOptions{
		CurrentUserPrincipalPath: testPrincipalPath,
		Capabilities:             []webdav.Capability{caldav.CapabilityCalendar, carddav.CapabilityAddressBook},
		Push:                     b,
	}
}

// servePrincipalAt sends a request for path to ServePrincipal.
func servePrincipalAt(opts *webdav.ServePrincipalOptions, method, path, body string) *httptest.ResponseRecorder {
	var r *http.Request
	if body != "" {
		r = httptest.NewRequest(method, path, strings.NewReader(body))
		r.Header.Set("Content-Type", "application/xml")
	} else {
		r = httptest.NewRequest(method, path, nil)
	}
	w := httptest.NewRecorder()
	webdav.ServePrincipal(w, r, opts)
	return w
}

func splitHeader(h http.Header, name string) map[string]bool {
	m := make(map[string]bool)
	for _, v := range h.Values(name) {
		for _, s := range strings.Split(v, ",") {
			m[strings.TrimSpace(s)] = true
		}
	}
	return m
}

// propStatus returns the status of each property of a PROPFIND response.
func propStatus(t *testing.T, w *httptest.ResponseRecorder) map[xml.Name]int {
	t.Helper()
	resps := decodePrincipalResponses(t, w)
	if len(resps) != 1 {
		t.Fatalf("got %d responses, want 1:\n%s", len(resps), w.Body)
	}
	m := make(map[xml.Name]int)
	for _, ps := range resps[0].PropStats {
		for _, raw := range ps.Prop.Raw {
			if name, ok := raw.XMLName(); ok {
				m[name] = ps.Status.Code
			}
		}
	}
	return m
}

// draft-bitfire-webdav-push section 2: a resource supporting WebDAV-Push
// has webdav-push in its DAV header field, and POST in Allow for the
// registrations.
func TestServePrincipalPushOPTIONS(t *testing.T) {
	b := newPrincipalPushBackend(t)
	w := servePrincipalAt(newPushPrincipalOptions(b), http.MethodOptions, testPrincipalPath, "")
	if w.Code != http.StatusNoContent {
		t.Fatalf("status = %v, want 204", w.Code)
	}
	dav := splitHeader(w.Header(), "DAV")
	for _, c := range []string{"1", "3", "calendar-access", "addressbook", "webdav-push"} {
		if !dav[c] {
			t.Errorf("DAV: %q, want %v", w.Header().Values("DAV"), c)
		}
	}
	if allow := splitHeader(w.Header(), "Allow"); !allow[http.MethodPost] || !allow["PROPFIND"] {
		t.Errorf("Allow: %q, want POST and PROPFIND", w.Header().Values("Allow"))
	}
	if !reflect.DeepEqual(b.infoCalls, []string{testPrincipalPath}) {
		t.Errorf("PushInfo calls = %q, want %q", b.infoCalls, testPrincipalPath)
	}

	// Without push information, the principal doesn't support WebDAV-Push
	b.info = nil
	w = servePrincipalAt(newPushPrincipalOptions(b), http.MethodOptions, testPrincipalPath, "")
	if splitHeader(w.Header(), "DAV")["webdav-push"] || splitHeader(w.Header(), "Allow")[http.MethodPost] {
		t.Errorf("without push information: DAV: %q, Allow: %q, want neither webdav-push nor POST", w.Header().Values("DAV"), w.Header().Values("Allow"))
	}
}

type testTransports struct {
	XMLName xml.Name `xml:"https://bitfire.at/webdav-push transports"`
	WebPush *struct {
		VAPIDPublicKey struct {
			Type string `xml:"type,attr"`
			Key  string `xml:",chardata"`
		} `xml:"vapid-public-key"`
	} `xml:"web-push"`
}

type testTopic struct {
	XMLName xml.Name `xml:"https://bitfire.at/webdav-push topic"`
	Topic   string   `xml:",chardata"`
}

type testSupportedTriggers struct {
	XMLName       xml.Name `xml:"https://bitfire.at/webdav-push supported-triggers"`
	ContentUpdate *struct {
		Depth string `xml:"DAV: depth"`
	} `xml:"content-update"`
	PropertyUpdate *struct {
		Depth string `xml:"DAV: depth"`
	} `xml:"property-update"`
}

const propFindPushProps = `<?xml version="1.0" encoding="utf-8"?>
<d:propfind xmlns:d="DAV:" xmlns:p="https://bitfire.at/webdav-push">
  <d:prop>
    <p:transports/>
    <p:topic/>
    <p:supported-triggers/>
  </d:prop>
</d:propfind>`

// draft-bitfire-webdav-push section 2.1: a resource supporting WebDAV-Push
// has the transports, topic and supported-triggers properties.
func TestServePrincipalPushPROPFIND(t *testing.T) {
	b := newPrincipalPushBackend(t)
	opts := newPushPrincipalOptions(b)

	w := servePrincipalAt(opts, "PROPFIND", testPrincipalPath, propFindPushProps)
	resps := decodePrincipalResponses(t, w)
	if len(resps) != 1 {
		t.Fatalf("got %d responses, want 1:\n%s", len(resps), w.Body)
	}
	var transports testTransports
	if err := resps[0].DecodeProp(&transports); err != nil {
		t.Fatalf("transports: %v\n%s", err, w.Body)
	}
	wantKey, _ := b.vapidKey.PublicKey.Bytes()
	if transports.WebPush == nil || transports.WebPush.VAPIDPublicKey.Type != "p256ecdsa" || transports.WebPush.VAPIDPublicKey.Key != base64.RawURLEncoding.EncodeToString(wantKey) {
		t.Errorf("transports isn't web-push with the VAPID key:\n%s", w.Body)
	}
	var topic testTopic
	if err := resps[0].DecodeProp(&topic); err != nil {
		t.Errorf("topic: %v", err)
	} else if topic.Topic != "principal-topic" {
		t.Errorf("topic = %q, want principal-topic", topic.Topic)
	}
	var triggers testSupportedTriggers
	if err := resps[0].DecodeProp(&triggers); err != nil {
		t.Errorf("supported-triggers: %v", err)
	} else if triggers.ContentUpdate != nil || triggers.PropertyUpdate == nil || triggers.PropertyUpdate.Depth != "0" {
		t.Errorf("supported-triggers isn't property-update of depth 0:\n%s", w.Body)
	}

	// propname lists the push properties, allprop doesn't return them
	// unless they're included
	status := propStatus(t, servePrincipalAt(opts, "PROPFIND", testPrincipalPath, `<d:propfind xmlns:d="DAV:"><d:propname/></d:propfind>`))
	for _, name := range testPushPropNames {
		if status[name] != http.StatusOK {
			t.Errorf("propname: %v has status %v, want 200", name.Local, status[name])
		}
	}
	w = servePrincipalAt(opts, "PROPFIND", testPrincipalPath, `<d:propfind xmlns:d="DAV:"><d:allprop/></d:propfind>`)
	if strings.Contains(w.Body.String(), testPushNamespace) {
		t.Errorf("allprop returns push properties:\n%s", w.Body)
	}
	status = propStatus(t, servePrincipalAt(opts, "PROPFIND", testPrincipalPath, `<d:propfind xmlns:d="DAV:" xmlns:p="https://bitfire.at/webdav-push"><d:allprop/><d:include><p:topic/></d:include></d:propfind>`))
	if status[testPushPropNames[1]] != http.StatusOK {
		t.Errorf("allprop with include: topic has status %v, want 200", status[testPushPropNames[1]])
	}

	// Without push information, the properties aren't served
	b.info = nil
	status = propStatus(t, servePrincipalAt(opts, "PROPFIND", testPrincipalPath, propFindPushProps))
	for _, name := range testPushPropNames {
		if status[name] != http.StatusNotFound {
			t.Errorf("without push information: %v has status %v, want 404", name.Local, status[name])
		}
	}

	// If PushInfo fails, the properties get its error, and propname doesn't
	// list them, as push may not be available
	b.infoErr = webdav.NewHTTPError(http.StatusServiceUnavailable, errors.New("unavailable"))
	status = propStatus(t, servePrincipalAt(opts, "PROPFIND", testPrincipalPath, propFindPushProps))
	for _, name := range testPushPropNames {
		if status[name] != http.StatusServiceUnavailable {
			t.Errorf("PushInfo error: %v has status %v, want 503", name.Local, status[name])
		}
	}
	status = propStatus(t, servePrincipalAt(opts, "PROPFIND", testPrincipalPath, `<d:propfind xmlns:d="DAV:"><d:propname/></d:propfind>`))
	for _, name := range testPushPropNames {
		if _, ok := status[name]; ok {
			t.Errorf("PushInfo error: propname lists %v", name.Local)
		}
	}
}

// The sample registration of the draft (xml/sample-push-register.xml).
const testPushRegister = `<?xml version="1.0" encoding="utf-8" ?>
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
      <D:depth>infinity</D:depth>
    </content-update>
    <property-update>
      <D:depth>0</D:depth>
      <D:prop>
        <D:displayname/>
        <D:owner/>
      </D:prop>
    </property-update>
  </trigger>
  <expires>Wed, 20 Dec 2023 10:03:31 GMT</expires>
</push-register>`

// checkPushPrecondition checks that w is a 403 response with the WebDAV-Push
// precondition p (draft-bitfire-webdav-push section 3.1.3).
func checkPushPrecondition(t *testing.T, w *httptest.ResponseRecorder, p string) {
	t.Helper()
	if w.Code != http.StatusForbidden {
		t.Fatalf("status = %v, want 403; body:\n%s", w.Code, w.Body)
	}
	var e internal.Error
	if err := xml.Unmarshal(w.Body.Bytes(), &e); err != nil {
		t.Fatalf("failed to decode DAV:error: %v\n%s", err, w.Body)
	}
	for _, raw := range e.Raw {
		if name, ok := raw.XMLName(); ok && name == (xml.Name{Space: testPushNamespace, Local: p}) {
			return
		}
	}
	t.Errorf("DAV:error doesn't hold %v:\n%s", p, w.Body)
}

// draft-bitfire-webdav-push section 3.1: a client registers a push
// subscription with a POST request with a push-register body. The triggers
// the principal doesn't support are ignored.
func TestServePrincipalPushRegister(t *testing.T) {
	b := newPrincipalPushBackend(t)
	opts := newPushPrincipalOptions(b)

	w := servePrincipalAt(opts, http.MethodPost, testPrincipalPath, testPushRegister)
	if w.Code != http.StatusNoContent {
		t.Fatalf("status = %v, want 204; body:\n%s", w.Code, w.Body)
	}
	if loc, want := w.Header().Get("Location"), "http://example.com/push/io6Efei4ooph"; loc != want {
		t.Errorf("Location: %q, want %q", loc, want)
	}
	if exp, want := w.Header().Get("Expires"), "Wed, 02 Oct 2024 07:28:00 GMT"; exp != want {
		t.Errorf("Expires: %q, want %q", exp, want)
	}
	if !reflect.DeepEqual(b.registerPaths, []string{testPrincipalPath}) {
		t.Fatalf("RegisterPush paths = %q, want %q", b.registerPaths, testPrincipalPath)
	}
	sub := b.registers[0]
	if sub.WebPush.PushResource != "https://up.example.net/yohd4yai5Phiz1wi" || len(sub.WebPush.AuthSecret) != 16 {
		t.Errorf("WebPush = %+v, want the push resource and auth secret of the request", sub.WebPush)
	}
	if sub.ContentUpdate != nil {
		t.Errorf("ContentUpdate = %+v, want nil: the principal doesn't support it", sub.ContentUpdate)
	}
	wantProps := []xml.Name{{Space: "DAV:", Local: "displayname"}, {Space: "DAV:", Local: "owner"}}
	if sub.PropertyUpdate == nil || sub.PropertyUpdate.Depth != webdav.DepthZero || !reflect.DeepEqual(sub.PropertyUpdate.Props, wantProps) {
		t.Errorf("PropertyUpdate = %+v, want depth 0 with %v", sub.PropertyUpdate, wantProps)
	}
	if want := time.Date(2023, 12, 20, 10, 3, 31, 0, time.UTC); !sub.Expires.Equal(want) {
		t.Errorf("Expires = %v, want %v", sub.Expires, want)
	}

	// A malformed body is a bad request
	if w := servePrincipalAt(opts, http.MethodPost, testPrincipalPath, "<push-register"); w.Code != http.StatusBadRequest {
		t.Errorf("malformed body: status = %v, want 400", w.Code)
	}

	// Only content updates, which the principal doesn't support
	contentOnly := strings.Replace(testPushRegister, testPushRegister[strings.Index(testPushRegister, "<property-update>"):strings.Index(testPushRegister, "</trigger>")], "", 1)
	checkPushPrecondition(t, servePrincipalAt(opts, http.MethodPost, testPrincipalPath, contentOnly), "no-supported-trigger")

	// Without push information, push isn't available
	b.info = nil
	checkPushPrecondition(t, servePrincipalAt(opts, http.MethodPost, testPrincipalPath, testPushRegister), "push-not-available")
	if len(b.registers) != 1 {
		t.Errorf("RegisterPush called %d times, want 1", len(b.registers))
	}
}

// draft-bitfire-webdav-push section 3.3: a client removes a registration
// with a DELETE request for its registration URL. Other DELETE requests are
// refused as before.
func TestServePrincipalPushUnregister(t *testing.T) {
	b := newPrincipalPushBackend(t)
	opts := newPushPrincipalOptions(b)

	if w := servePrincipalAt(opts, http.MethodDelete, "/push/io6Efei4ooph", ""); w.Code != http.StatusNoContent {
		t.Errorf("DELETE registration: status = %v, want 204; body:\n%s", w.Code, w.Body)
	}
	if b.registrations["io6Efei4ooph"] {
		t.Errorf("registration not removed")
	}
	if w := servePrincipalAt(opts, http.MethodDelete, "/push/io6Efei4ooph", ""); w.Code != http.StatusNotFound {
		t.Errorf("DELETE removed registration: status = %v, want 404", w.Code)
	}

	w := servePrincipalAt(opts, http.MethodDelete, testPrincipalPath, "")
	if w.Code != http.StatusMethodNotAllowed {
		t.Errorf("DELETE principal: status = %v, want 405", w.Code)
	}
	if allow := splitHeader(w.Header(), "Allow"); !allow[http.MethodPost] || allow[http.MethodDelete] {
		t.Errorf("DELETE principal: Allow: %q, want POST but not DELETE", w.Header().Values("Allow"))
	}
}

// With push information the server can't serve as the draft requires, here
// a VAPID key off the P-256 curve, the principal doesn't support WebDAV-Push:
// OPTIONS and 405 responses don't advertise it (draft-bitfire-webdav-push
// section 2), and the push properties and registrations fail as when
// PushInfo fails.
func TestServePrincipalPushInvalidInfo(t *testing.T) {
	b := newPrincipalPushBackend(t)
	b.info[testPrincipalPath].VAPIDPublicKey = &ecdsa.PublicKey{Curve: elliptic.P256(), X: big.NewInt(1), Y: big.NewInt(1)}
	opts := newPushPrincipalOptions(b)

	w := servePrincipalAt(opts, http.MethodOptions, testPrincipalPath, "")
	if w.Code != http.StatusNoContent {
		t.Fatalf("OPTIONS: status = %v, want 204", w.Code)
	}
	if splitHeader(w.Header(), "DAV")["webdav-push"] || splitHeader(w.Header(), "Allow")[http.MethodPost] {
		t.Errorf("OPTIONS: DAV: %q, Allow: %q, want neither webdav-push nor POST", w.Header().Values("DAV"), w.Header().Values("Allow"))
	}
	w = servePrincipalAt(opts, http.MethodPut, testPrincipalPath, "")
	if w.Code != http.StatusMethodNotAllowed {
		t.Errorf("PUT: status = %v, want 405", w.Code)
	}
	if got, want := w.Header().Get("Allow"), "OPTIONS, PROPFIND"; got != want {
		t.Errorf("PUT: Allow: %q, want %q", got, want)
	}

	status := propStatus(t, servePrincipalAt(opts, "PROPFIND", testPrincipalPath, propFindPushProps))
	for _, name := range testPushPropNames {
		if status[name] != http.StatusInternalServerError {
			t.Errorf("%v has status %v, want 500", name.Local, status[name])
		}
	}
	status = propStatus(t, servePrincipalAt(opts, "PROPFIND", testPrincipalPath, `<d:propfind xmlns:d="DAV:"><d:propname/></d:propfind>`))
	for _, name := range testPushPropNames {
		if _, ok := status[name]; ok {
			t.Errorf("propname lists %v", name.Local)
		}
	}

	if w := servePrincipalAt(opts, http.MethodPost, testPrincipalPath, testPushRegister); w.Code != http.StatusInternalServerError {
		t.Errorf("POST: status = %v, want 500", w.Code)
	}
	if len(b.registers) != 0 {
		t.Errorf("RegisterPush called %d times, want none", len(b.registers))
	}
}

// Without a push backend, ServePrincipal doesn't call one: POST and DELETE
// are refused, and the push properties aren't served.
func TestServePrincipalNoPush(t *testing.T) {
	opts := &webdav.ServePrincipalOptions{CurrentUserPrincipalPath: testPrincipalPath}
	for _, method := range []string{http.MethodPost, http.MethodDelete} {
		w := servePrincipalAt(opts, method, testPrincipalPath, testPushRegister)
		if w.Code != http.StatusMethodNotAllowed {
			t.Errorf("%v: status = %v, want 405", method, w.Code)
		}
		if got, want := w.Header().Get("Allow"), "OPTIONS, PROPFIND"; got != want {
			t.Errorf("%v: Allow: %q, want %q", method, got, want)
		}
	}
	status := propStatus(t, servePrincipalAt(opts, "PROPFIND", testPrincipalPath, propFindPushProps))
	for _, name := range testPushPropNames {
		if status[name] != http.StatusNotFound {
			t.Errorf("%v has status %v, want 404", name.Local, status[name])
		}
	}
}
