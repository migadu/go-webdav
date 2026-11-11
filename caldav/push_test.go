package caldav

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
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/emersion/go-webdav"
	"github.com/emersion/go-webdav/internal"
)

const (
	pushNamespace       = "https://bitfire.at/webdav-push"
	pushCalendarPath    = "/user/calendars/work/"
	nonPushCalendarPath = "/user/calendars/holidays/"
	pushObjectPath      = "/user/calendars/work/event.ics"
	pushInboxPath       = "/user/calendars/inbox/"
)

var (
	pushTransportsName        = xml.Name{Space: pushNamespace, Local: "transports"}
	pushTopicName             = xml.Name{Space: pushNamespace, Local: "topic"}
	pushSupportedTriggersName = xml.Name{Space: pushNamespace, Local: "supported-triggers"}
	pushPropNames             = []xml.Name{pushTransportsName, pushTopicName, pushSupportedTriggersName}
)

// pushTestBackend is a testBackend supporting WebDAV-Push for the principal,
// the calendar home set and the work calendar, but not for the holidays
// calendar. Registration URLs are under /push/.
type pushTestBackend struct {
	testBackend
	vapidKey *ecdsa.PrivateKey
	info     map[string]*webdav.PushInfo
	infoErr  map[string]error // PushInfo errors, by path

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
	b := &pushTestBackend{
		testBackend: testBackend{
			calendars: []Calendar{
				{Path: pushCalendarPath, Name: "Work"},
				{Path: nonPushCalendarPath, Name: "Holidays"},
			},
			objectMap: map[string][]CalendarObject{
				pushCalendarPath: {newSyncTestObject("event", "Event", "etag-event")},
			},
		},
		vapidKey:      key,
		registrations: map[string]bool{"io6Efei4ooph": true},
	}
	b.objectMap[pushCalendarPath][0].Path = pushObjectPath
	b.info = map[string]*webdav.PushInfo{
		"/user/": {
			Topic:          "principal-topic",
			VAPIDPublicKey: &key.PublicKey,
			PropertyUpdate: &webdav.PushTrigger{Depth: webdav.DepthZero},
		},
		"/user/calendars/": {
			Topic:          "home-topic",
			VAPIDPublicKey: &key.PublicKey,
			ContentUpdate:  &webdav.PushTrigger{Depth: webdav.DepthOne},
		},
		pushCalendarPath: {
			Topic:          "O7M1nQ7cKkKTKsoS_j6Z3w",
			VAPIDPublicKey: &key.PublicKey,
			ContentUpdate:  &webdav.PushTrigger{Depth: webdav.DepthOne},
			PropertyUpdate: &webdav.PushTrigger{Depth: webdav.DepthZero},
		},
		pushInboxPath: {
			Topic:         "inbox-topic",
			ContentUpdate: &webdav.PushTrigger{Depth: webdav.DepthOne},
		},
	}
	return b
}

func (b *pushTestBackend) PushInfo(ctx context.Context, path string) (*webdav.PushInfo, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.infoCalls = append(b.infoCalls, path)
	if err := b.infoErr[path]; err != nil {
		return nil, err
	}
	return b.info[path], nil
}

func (b *pushTestBackend) RegisterPush(ctx context.Context, path string, sub *webdav.PushSubscription) (*webdav.PushRegistration, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.registers = append(b.registers, *sub)
	b.registrations["io6Efei4ooph"] = true
	expires := time.Date(2024, 10, 2, 7, 28, 0, 0, time.UTC)
	if !sub.Expires.IsZero() && sub.Expires.Before(expires) {
		expires = sub.Expires
	}
	return &webdav.PushRegistration{URL: "/push/io6Efei4ooph", Expires: expires}, nil
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

func (b *pushTestBackend) DeleteCalendarObject(ctx context.Context, path string) error {
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

// pushInboxBackend is a pushTestBackend with a scheduling Inbox.
type pushInboxBackend struct {
	*pushTestBackend
}

func (b pushInboxBackend) GetInbox(ctx context.Context) (*Inbox, error) {
	return &Inbox{Path: pushInboxPath, UserAddressSet: []string{"mailto:user@example.com"}}, nil
}

// deleteRecorder is a testBackend, without WebDAV-Push support, recording
// the resources it deletes.
type deleteRecorder struct {
	testBackend
	deleted *[]string
}

func (b deleteRecorder) DeleteCalendarObject(ctx context.Context, path string) error {
	*b.deleted = append(*b.deleted, path)
	return nil
}

const propFindPushProps = `<?xml version="1.0" encoding="utf-8" ?>
<propfind xmlns="DAV:" xmlns:P="https://bitfire.at/webdav-push">
  <prop>
    <P:transports/>
    <P:topic/>
    <P:supported-triggers/>
  </prop>
</propfind>`

type testPushTransports struct {
	XMLName xml.Name `xml:"https://bitfire.at/webdav-push transports"`
	WebPush []struct {
		VAPIDPublicKey []struct {
			Type string `xml:"type,attr"`
			Key  string `xml:",chardata"`
		} `xml:"https://bitfire.at/webdav-push vapid-public-key"`
	} `xml:"https://bitfire.at/webdav-push web-push"`
}

type testPushTopic struct {
	XMLName xml.Name `xml:"https://bitfire.at/webdav-push topic"`
	Topic   string   `xml:",chardata"`
}

type testPushDepth struct {
	Depth string `xml:"DAV: depth"`
}

type testSupportedTriggers struct {
	XMLName        xml.Name        `xml:"https://bitfire.at/webdav-push supported-triggers"`
	ContentUpdate  []testPushDepth `xml:"https://bitfire.at/webdav-push content-update"`
	PropertyUpdate []testPushDepth `xml:"https://bitfire.at/webdav-push property-update"`
}

func checkPushProps(t *testing.T, resp *internal.Response, info *webdav.PushInfo) {
	t.Helper()

	var transports testPushTransports
	if err := resp.DecodeProp(&transports); err != nil {
		t.Fatalf("transports: %v", err)
	}
	if len(transports.WebPush) != 1 {
		t.Fatalf("transports lists %d web-push elements, want 1", len(transports.WebPush))
	}
	keys := transports.WebPush[0].VAPIDPublicKey
	if info.VAPIDPublicKey == nil {
		if len(keys) != 0 {
			t.Errorf("unexpected vapid-public-key %+v", keys)
		}
	} else {
		want, _ := info.VAPIDPublicKey.Bytes()
		if len(keys) != 1 || keys[0].Type != "p256ecdsa" || keys[0].Key != base64.RawURLEncoding.EncodeToString(want) {
			t.Errorf("vapid-public-key = %+v, want the p256ecdsa key %v", keys, base64.RawURLEncoding.EncodeToString(want))
		}
	}

	var topic testPushTopic
	if err := resp.DecodeProp(&topic); err != nil {
		t.Fatalf("topic: %v", err)
	}
	if topic.Topic != info.Topic {
		t.Errorf("topic = %q, want %q", topic.Topic, info.Topic)
	}

	var triggers testSupportedTriggers
	if err := resp.DecodeProp(&triggers); err != nil {
		t.Fatalf("supported-triggers: %v", err)
	}
	checkTriggerDepth := func(name string, got []testPushDepth, want *webdav.PushTrigger) {
		t.Helper()
		if want == nil {
			if len(got) != 0 {
				t.Errorf("unexpected %v trigger %+v", name, got)
			}
			return
		}
		if len(got) != 1 || got[0].Depth != want.Depth.String() {
			t.Errorf("%v triggers = %+v, want depth %v", name, got, want.Depth)
		}
	}
	checkTriggerDepth("content-update", triggers.ContentUpdate, info.ContentUpdate)
	checkTriggerDepth("property-update", triggers.PropertyUpdate, info.PropertyUpdate)
}

func checkNoPushProps(t *testing.T, resp *internal.Response) {
	t.Helper()
	for _, name := range pushPropNames {
		if code := propStatus(t, resp, name); code != http.StatusNotFound {
			t.Errorf("%v: got status %v, want 404", name.Local, code)
		}
	}
}

func TestPushPropFindCalendar(t *testing.T) {
	b := newPushTestBackend(t)
	h := &Handler{Backend: b}

	resp := propFindResponse(t, h, pushCalendarPath, "0", propFindPushProps, pushCalendarPath)
	checkPushProps(t, resp, b.info[pushCalendarPath])

	resp = propFindResponse(t, h, nonPushCalendarPath, "0", propFindPushProps, nonPushCalendarPath)
	checkNoPushProps(t, resp)
}

func TestPushPropFindHomeSet(t *testing.T) {
	b := newPushTestBackend(t)
	h := &Handler{Backend: b}

	for _, p := range []string{"/user/calendars/", pushCalendarPath} {
		resp := propFindResponse(t, h, "/user/calendars/", "1", propFindPushProps, p)
		checkPushProps(t, resp, b.info[p])
	}
	resp := propFindResponse(t, h, "/user/calendars/", "1", propFindPushProps, nonPushCalendarPath)
	checkNoPushProps(t, resp)

	resp = propFindResponse(t, h, "/user/", "0", propFindPushProps, "/user/")
	checkPushProps(t, resp, b.info["/user/"])
}

func TestPushPropFindInbox(t *testing.T) {
	b := newPushTestBackend(t)
	h := &Handler{Backend: pushInboxBackend{b}}

	resp := propFindResponse(t, h, pushInboxPath, "0", propFindPushProps, pushInboxPath)
	checkPushProps(t, resp, b.info[pushInboxPath])
}

// Calendar objects aren't WebDAV-Push collections.
func TestPushPropFindCalendarObject(t *testing.T) {
	b := newPushTestBackend(t)
	h := &Handler{Backend: b}

	resp := propFindResponse(t, h, pushObjectPath, "0", propFindPushProps, pushObjectPath)
	checkNoPushProps(t, resp)
	if calls := b.pushInfoCalls(); len(calls) != 0 {
		t.Errorf("PushInfo called for %v", calls)
	}
}

// The push properties aren't returned by allprop requests, and the backend
// isn't asked about push when they aren't requested.
func TestPushPropFindNotRequested(t *testing.T) {
	b := newPushTestBackend(t)
	h := &Handler{Backend: b}

	for _, body := range []string{
		`<propfind xmlns="DAV:"><allprop/></propfind>`,
		`<propfind xmlns="DAV:"><prop><displayname/><resourcetype/></prop></propfind>`,
	} {
		w := serveCalDAV(b, "PROPFIND", "/user/calendars/", "1", body)
		if w.Code != http.StatusMultiStatus {
			t.Fatalf("status = %v, want 207", w.Code)
		}
		if strings.Contains(w.Body.String(), pushNamespace) {
			t.Errorf("push properties returned for %s:\n%s", body, w.Body.String())
		}
	}
	if calls := b.pushInfoCalls(); len(calls) != 0 {
		t.Errorf("PushInfo called for %v", calls)
	}

	// Unless they're included
	include := `<propfind xmlns="DAV:" xmlns:P="https://bitfire.at/webdav-push"><allprop/><include><P:topic/></include></propfind>`
	resp := propFindResponse(t, h, pushCalendarPath, "0", include, pushCalendarPath)
	var topic testPushTopic
	if err := resp.DecodeProp(&topic); err != nil || topic.Topic != b.info[pushCalendarPath].Topic {
		t.Errorf("included topic = %q, %v", topic.Topic, err)
	}
}

func TestPushPropName(t *testing.T) {
	b := newPushTestBackend(t)
	h := &Handler{Backend: b}
	propname := `<propfind xmlns="DAV:"><propname/></propfind>`

	for _, tc := range []struct {
		path string
		push bool
	}{
		{pushCalendarPath, true},
		{nonPushCalendarPath, false},
	} {
		resp := propFindResponse(t, h, tc.path, "0", propname, tc.path)
		for _, name := range pushPropNames {
			found := false
			for _, propstat := range resp.PropStats {
				if propstat.Prop.Get(name) != nil {
					found = true
				}
			}
			if found != tc.push {
				t.Errorf("%v: %v listed: %v, want %v", tc.path, name.Local, found, tc.push)
			}
		}
	}
}

func options(t *testing.T, b Backend, path string) (dav []string, allow map[string]bool) {
	t.Helper()
	w := serveCalDAV(b, http.MethodOptions, path, "", "")
	if w.Code != http.StatusNoContent {
		t.Fatalf("OPTIONS %v: status = %v, want 204; body:\n%s", path, w.Code, w.Body.String())
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

// The webdav-push capability is advertised for the resources supporting
// WebDAV-Push (draft section 3).
func TestPushOptions(t *testing.T) {
	b := newPushTestBackend(t)
	for _, tc := range []struct {
		backend Backend
		path    string
		push    bool
	}{
		{b, pushCalendarPath, true},
		{b, "/user/calendars/", true},
		{b, "/user/", true},
		{pushInboxBackend{b}, pushInboxPath, true},
		{b, nonPushCalendarPath, false},
		{b, pushObjectPath, false},
	} {
		dav, allow := options(t, tc.backend, tc.path)
		if got := containsString(dav, "webdav-push"); got != tc.push {
			t.Errorf("OPTIONS %v: DAV = %v, webdav-push listed: %v, want %v", tc.path, dav, got, tc.push)
		}
		if allow["POST"] != tc.push {
			t.Errorf("OPTIONS %v: POST allowed: %v, want %v", tc.path, allow["POST"], tc.push)
		}
	}
}

// PushInfo may return a 404 error for a path where there's no collection,
// which means that push isn't available there, as if it returned nil.
func TestPushInfoNotFound(t *testing.T) {
	const newCalendarPath = "/user/calendars/new/"
	b := newPushTestBackend(t)
	b.infoErr = map[string]error{newCalendarPath: webdav.NewHTTPError(http.StatusNotFound, nil)}

	// OPTIONS still works before MKCALENDAR
	t.Run("OPTIONS", func(t *testing.T) {
		dav, allow := options(t, b, newCalendarPath)
		if containsString(dav, "webdav-push") || allow["POST"] {
			t.Errorf("DAV = %v, Allow = %v, want no push", dav, allow)
		}
		if !allow["MKCALENDAR"] {
			t.Errorf("Allow = %v, want MKCALENDAR", allow)
		}
	})

	// A registration is refused like for other collections without push
	t.Run("POST", func(t *testing.T) {
		if w := serveCalDAV(b, http.MethodPost, newCalendarPath, "", "<push-register"); w.Code != http.StatusBadRequest {
			t.Errorf("malformed XML: status = %v, want 400", w.Code)
		}
		checkPushPrecondition(t, serveCalDAV(b, http.MethodPost, newCalendarPath, "", pushRegisterRequest), "push-not-available")
		if len(b.registers) != 0 {
			t.Errorf("RegisterPush called %d times, want none", len(b.registers))
		}
	})
}

// When PushInfo fails, a propname request doesn't list the push properties,
// since it's unknown whether push is available, and a request for them gets
// the error.
func TestPushPropNameError(t *testing.T) {
	b := newPushTestBackend(t)
	b.infoErr = map[string]error{pushCalendarPath: webdav.NewHTTPError(http.StatusServiceUnavailable, nil)}
	h := &Handler{Backend: b}

	resp := propFindResponse(t, h, pushCalendarPath, "0", `<propfind xmlns="DAV:"><propname/></propfind>`, pushCalendarPath)
	for _, propstat := range resp.PropStats {
		for _, name := range pushPropNames {
			if propstat.Prop.Get(name) != nil {
				t.Errorf("propname lists %v with status %v", name.Local, propstat.Status.Code)
			}
		}
	}

	resp = propFindResponse(t, h, pushCalendarPath, "0", propFindPushProps, pushCalendarPath)
	for _, name := range pushPropNames {
		if code := propStatus(t, resp, name); code != http.StatusServiceUnavailable {
			t.Errorf("%v: got status %v, want 503", name.Local, code)
		}
	}
}

// The draft example (draft section 4.1.3): a registration with the sample
// body of the draft.
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

func TestPushRegister(t *testing.T) {
	b := newPushTestBackend(t)
	w := serveCalDAV(b, http.MethodPost, pushCalendarPath, "", pushRegisterRequest)

	if w.Code != http.StatusNoContent {
		t.Fatalf("status = %v, want 204; body:\n%s", w.Code, w.Body.String())
	}
	if got, want := w.Header().Get("Location"), "http://example.com/push/io6Efei4ooph"; got != want {
		t.Errorf("Location = %q, want %q", got, want)
	}
	if got, want := w.Header().Get("Expires"), "Wed, 20 Dec 2023 10:03:31 GMT"; got != want {
		t.Errorf("Expires = %q, want %q", got, want)
	}

	if len(b.registers) != 1 {
		t.Fatalf("RegisterPush called %d times, want 1", len(b.registers))
	}
	sub := b.registers[0]
	if sub.WebPush.PushResource != "https://up.example.net/yohd4yai5Phiz1wi" || len(sub.WebPush.PublicKey) != 65 || len(sub.WebPush.AuthSecret) != 16 {
		t.Errorf("WebPush = %+v", sub.WebPush)
	}
	if want := (&webdav.PushTrigger{Depth: webdav.DepthOne}); !reflect.DeepEqual(sub.ContentUpdate, want) {
		t.Errorf("ContentUpdate = %+v, want %+v (downgraded from infinity)", sub.ContentUpdate, want)
	}
	wantProp := &webdav.PushTrigger{
		Depth: webdav.DepthZero,
		Props: []xml.Name{internal.DisplayNameName, {Space: "DAV:", Local: "owner"}},
	}
	if !reflect.DeepEqual(sub.PropertyUpdate, wantProp) {
		t.Errorf("PropertyUpdate = %+v, want %+v", sub.PropertyUpdate, wantProp)
	}
	if want := time.Date(2023, 12, 20, 10, 3, 31, 0, time.UTC); !sub.Expires.Equal(want) {
		t.Errorf("Expires = %v, want %v", sub.Expires, want)
	}
}

// checkPushPrecondition checks that w is a 403 Forbidden response with the
// WebDAV-Push precondition name.
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

func TestPushRegisterErrors(t *testing.T) {
	b := newPushTestBackend(t)

	// Invalid requests
	w := serveCalDAV(b, http.MethodPost, pushCalendarPath, "", "<push-register")
	if w.Code != http.StatusBadRequest {
		t.Errorf("malformed XML: status = %v, want 400", w.Code)
	}
	w = serveCalDAV(b, http.MethodPost, pushCalendarPath, "", `<propfind xmlns="DAV:"><allprop/></propfind>`)
	if w.Code != http.StatusBadRequest {
		t.Errorf("other request: status = %v, want 400", w.Code)
	}

	// Unsupported transport, or a Web Push subscription without TLS or
	// encryption keys
	for _, body := range []string{
		strings.Replace(pushRegisterRequest, "<web-push-subscription>", `<X:other-subscription xmlns:X="urn:example"/><web-push-subscription>`, 1),
		strings.Replace(pushRegisterRequest, "https://up.example.net", "http://up.example.net", 1),
		strings.Replace(pushRegisterRequest, "<auth-secret>BTBZMqHH6r4Tts7J_aSIgg</auth-secret>", "", 1),
	} {
		checkPushPrecondition(t, serveCalDAV(b, http.MethodPost, pushCalendarPath, "", body), "invalid-subscription")
	}

	// The principal only supports property updates
	contentOnly := strings.Replace(pushRegisterRequest, "<property-update>", "<X:property-update xmlns:X='urn:example'>", 1)
	contentOnly = strings.Replace(contentOnly, "</property-update>", "</X:property-update>", 1)
	checkPushPrecondition(t, serveCalDAV(b, http.MethodPost, "/user/", "", contentOnly), "no-supported-trigger")

	// Collections without push, and calendar objects
	for _, p := range []string{nonPushCalendarPath, pushObjectPath} {
		checkPushPrecondition(t, serveCalDAV(b, http.MethodPost, p, "", pushRegisterRequest), "push-not-available")
	}

	if len(b.registers) != 0 {
		t.Errorf("RegisterPush called %d times, want none", len(b.registers))
	}
	for _, p := range b.pushInfoCalls() {
		if p == pushObjectPath {
			t.Errorf("PushInfo called for the calendar object %v", p)
		}
	}
}

func TestPushRegisterInbox(t *testing.T) {
	b := newPushTestBackend(t)
	w := serveCalDAV(pushInboxBackend{b}, http.MethodPost, pushInboxPath, "", pushRegisterRequest)
	if w.Code != http.StatusNoContent {
		t.Fatalf("status = %v, want 204; body:\n%s", w.Code, w.Body.String())
	}
	if len(b.registers) != 1 || b.registers[0].PropertyUpdate != nil || b.registers[0].ContentUpdate == nil {
		t.Errorf("RegisterPush(%+v), want a content update trigger only", b.registers)
	}
}

func TestPushUnregister(t *testing.T) {
	b := newPushTestBackend(t)

	w := serveCalDAV(b, http.MethodDelete, "/push/io6Efei4ooph", "", "")
	if w.Code != http.StatusNoContent {
		t.Errorf("DELETE registration: status = %v, want 204", w.Code)
	}
	if b.registrations["io6Efei4ooph"] {
		t.Errorf("registration not removed")
	}
	// Removed, or unknown (draft section 4.3)
	w = serveCalDAV(b, http.MethodDelete, "/push/io6Efei4ooph", "", "")
	if w.Code != http.StatusNotFound {
		t.Errorf("DELETE unknown registration: status = %v, want 404", w.Code)
	}

	// Other resources are deleted as usual
	w = serveCalDAV(b, http.MethodDelete, pushObjectPath, "", "")
	if w.Code != http.StatusNoContent {
		t.Errorf("DELETE calendar object: status = %v, want 204", w.Code)
	}
	if want := []string{pushObjectPath}; !reflect.DeepEqual(b.deleted, want) {
		t.Errorf("deleted %v, want %v", b.deleted, want)
	}
}

// A backend which doesn't implement PushBackend behaves as before.
func TestPushNotImplemented(t *testing.T) {
	var deleted []string
	b := deleteRecorder{
		testBackend: testBackend{
			calendars: []Calendar{{Path: pushCalendarPath}},
			objectMap: map[string][]CalendarObject{pushCalendarPath: {}},
		},
		deleted: &deleted,
	}
	h := &Handler{Backend: b}

	resp := propFindResponse(t, h, pushCalendarPath, "0", propFindPushProps, pushCalendarPath)
	checkNoPushProps(t, resp)
	resp = propFindResponse(t, h, pushCalendarPath, "0", `<propfind xmlns="DAV:"><propname/></propfind>`, pushCalendarPath)
	for _, propstat := range resp.PropStats {
		for _, name := range pushPropNames {
			if propstat.Prop.Get(name) != nil {
				t.Errorf("propname lists %v", name.Local)
			}
		}
	}

	dav, allow := options(t, b, pushCalendarPath)
	if want := []string{"1", "3", "calendar-access"}; !reflect.DeepEqual(dav, want) {
		t.Errorf("DAV = %v, want %v", dav, want)
	}
	var methods []string
	for m := range allow {
		methods = append(methods, m)
	}
	sort.Strings(methods)
	if want := []string{"DELETE", "MKCALENDAR", "MKCOL", "OPTIONS", "PROPFIND", "REPORT"}; !reflect.DeepEqual(methods, want) {
		t.Errorf("Allow = %v, want %v", methods, want)
	}

	w := serveCalDAV(b, http.MethodPost, pushCalendarPath, "", pushRegisterRequest)
	if w.Code != http.StatusMethodNotAllowed {
		t.Errorf("POST: status = %v, want 405", w.Code)
	}

	w = serveCalDAV(b, http.MethodDelete, "/push/io6Efei4ooph", "", "")
	if w.Code != http.StatusNoContent {
		t.Errorf("DELETE: status = %v, want 204", w.Code)
	}
	if want := []string{"/push/io6Efei4ooph"}; !reflect.DeepEqual(deleted, want) {
		t.Errorf("deleted %v, want %v", deleted, want)
	}
}
