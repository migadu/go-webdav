package push

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"encoding/base64"
	"encoding/xml"
	"errors"
	"math/big"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/emersion/go-webdav/internal"
)

const (
	testPath         = "/user/calendars/work/"
	testPushResource = "https://up.example.net/yohd4yai5Phiz1wi"
	testP256DH       = "BCVxsr7N_eNgVRqvHtD0zTZsEc6-VV-JvLexhqUzORcxaOzi6-AYWXvTBHm4bjyPjs7Vd8pZGH6SRpkNtoIAiw4"
	testAuthSecret   = "BTBZMqHH6r4Tts7J_aSIgg"
)

// The sample registration of the draft (xml/sample-push-register.xml).
const draftSamplePushRegister = `<?xml version="1.0" encoding="utf-8" ?>
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

type fakeBackend struct {
	info    map[string]*PushInfo
	infoErr error

	reg    *PushRegistration
	regErr error

	infoCalls []string
	registers []PushSubscription
}

func newFakeBackend() *fakeBackend {
	return &fakeBackend{
		info: map[string]*PushInfo{
			testPath: {
				Topic:          "O7M1nQ7cKkKTKsoS_j6Z3w",
				ContentUpdate:  &PushTrigger{Depth: internal.DepthOne},
				PropertyUpdate: &PushTrigger{Depth: internal.DepthZero},
			},
		},
		reg: &PushRegistration{
			URL:     "https://example.com/webdav/subscriptions/io6Efei4ooph",
			Expires: time.Date(2024, 10, 2, 7, 28, 0, 0, time.UTC),
		},
	}
}

func (b *fakeBackend) PushInfo(ctx context.Context, path string) (*PushInfo, error) {
	b.infoCalls = append(b.infoCalls, path)
	return b.info[path], b.infoErr
}

func (b *fakeBackend) RegisterPush(ctx context.Context, path string, sub *PushSubscription) (*PushRegistration, error) {
	if path != testPath {
		return nil, errors.New("unexpected path " + path)
	}
	b.registers = append(b.registers, *sub)
	return b.reg, b.regErr
}

func (b *fakeBackend) UnregisterPush(ctx context.Context, path string) (bool, error) {
	id, ok := strings.CutPrefix(path, "/subscriptions/")
	if !ok {
		return false, nil
	}
	if id != "io6Efei4ooph" {
		return true, &internal.HTTPError{Code: http.StatusNotFound}
	}
	return true, nil
}

func serveRegister(t *testing.T, b *fakeBackend, contentType, body string) *httptest.ResponseRecorder {
	t.Helper()
	r := httptest.NewRequest(http.MethodPost, testPath, strings.NewReader(body))
	if contentType != "" {
		r.Header.Set("Content-Type", contentType)
	}
	w := httptest.NewRecorder()
	// As the handlers do
	info, err := Info(r.Context(), b, testPath)
	if err == nil {
		err = ServeRegister(w, r, b, info)
	}
	if err != nil {
		internal.ServeError(w, err)
	}
	return w
}

// checkPrecondition checks that w is a 403 Forbidden response with the
// WebDAV-Push precondition name in its DAV:error body.
func checkPrecondition(t *testing.T, w *httptest.ResponseRecorder, name string) {
	t.Helper()
	if w.Code != http.StatusForbidden {
		t.Fatalf("status = %v, want 403; body:\n%s", w.Code, w.Body.String())
	}
	var davErr internal.Error
	if err := xml.NewDecoder(w.Body).Decode(&davErr); err != nil {
		t.Fatalf("failed to decode DAV:error: %v", err)
	}
	want := xml.Name{Space: Namespace, Local: name}
	for _, raw := range davErr.Raw {
		if n, ok := raw.XMLName(); ok && n == want {
			return
		}
	}
	t.Errorf("DAV:error = %v, want the %v precondition", davErr.Error(), name)
}

func TestServeRegister(t *testing.T) {
	b := newFakeBackend()
	w := serveRegister(t, b, `application/xml; charset="utf-8"`, draftSamplePushRegister)

	if w.Code != http.StatusNoContent {
		t.Fatalf("status = %v, want 204; body:\n%s", w.Code, w.Body.String())
	}
	if got := w.Header().Get("Location"); got != b.reg.URL {
		t.Errorf("Location = %q, want %q", got, b.reg.URL)
	}
	if got, want := w.Header().Get("Expires"), "Wed, 02 Oct 2024 07:28:00 GMT"; got != want {
		t.Errorf("Expires = %q, want %q", got, want)
	}

	if len(b.registers) != 1 {
		t.Fatalf("RegisterPush called %d times, want 1", len(b.registers))
	}
	pubKey, _ := base64.RawURLEncoding.DecodeString(testP256DH)
	authSecret, _ := base64.RawURLEncoding.DecodeString(testAuthSecret)
	want := PushSubscription{
		WebPush: WebPushSubscription{
			PushResource: testPushResource,
			PublicKey:    pubKey,
			AuthSecret:   authSecret,
		},
		// The collection supports content updates up to depth 1: the
		// requested depth is downgraded (draft section 4.1.1)
		ContentUpdate: &PushTrigger{Depth: internal.DepthOne},
		PropertyUpdate: &PushTrigger{
			Depth: internal.DepthZero,
			Props: []xml.Name{{Space: "DAV:", Local: "displayname"}, {Space: "DAV:", Local: "owner"}},
		},
		Expires: time.Date(2023, 12, 20, 10, 3, 31, 0, time.UTC),
	}
	if got := b.registers[0]; !reflect.DeepEqual(got, want) {
		t.Errorf("RegisterPush(%+v), want %+v", got, want)
	}
}

const registerTemplate = `<?xml version="1.0" encoding="utf-8" ?>
<P:push-register xmlns:P="https://bitfire.at/webdav-push" xmlns:D="DAV:">
  %s
</P:push-register>`

const (
	validWebPushSubscription = `<P:push-resource>https://up.example.net/yohd4yai5Phiz1wi</P:push-resource>
<P:content-encoding>aes128gcm</P:content-encoding>
<P:subscription-public-key type="p256dh">BCVxsr7N_eNgVRqvHtD0zTZsEc6-VV-JvLexhqUzORcxaOzi6-AYWXvTBHm4bjyPjs7Vd8pZGH6SRpkNtoIAiw4</P:subscription-public-key>
<P:auth-secret>BTBZMqHH6r4Tts7J_aSIgg</P:auth-secret>`
	validSubscription = `<P:subscription><P:web-push-subscription>` + validWebPushSubscription + `</P:web-push-subscription></P:subscription>`
	validTrigger      = `<P:trigger><P:content-update><D:depth>1</D:depth></P:content-update></P:trigger>`
)

func registerBody(elements ...string) string {
	return strings.Replace(registerTemplate, "%s", strings.Join(elements, "\n  "), 1)
}

// webPushBody returns a registration with a web-push-subscription where old
// is replaced with new.
func webPushBody(old, new string) string {
	return registerBody(strings.Replace(validSubscription, old, new, 1), validTrigger)
}

func TestServeRegisterInvalid(t *testing.T) {
	notOnCurve := []byte(nil)
	if b, err := base64.RawURLEncoding.DecodeString(testP256DH); err == nil {
		b[64] ^= 1
		notOnCurve = b
	}

	for _, tc := range []struct {
		name         string
		contentType  string
		body         string
		code         int
		precondition string
	}{
		{name: "not XML", contentType: "text/plain", body: draftSamplePushRegister, code: http.StatusBadRequest},
		{name: "malformed XML", body: "<P:push-register xmlns:P='https://bitfire.at/webdav-push'>", code: http.StatusBadRequest},
		{name: "other request", body: `<D:propfind xmlns:D="DAV:"><D:allprop/></D:propfind>`, code: http.StatusBadRequest},
		{name: "wrong namespace", body: `<push-register xmlns="urn:example"/>`, code: http.StatusBadRequest},

		{name: "no subscription", body: registerBody(validTrigger), precondition: "invalid-subscription"},
		{name: "two subscriptions", body: registerBody(validSubscription, validSubscription, validTrigger), precondition: "invalid-subscription"},
		{
			name:         "unsupported transport",
			body:         registerBody(`<P:subscription><X:other-push-subscription xmlns:X="urn:example"/></P:subscription>`, validTrigger),
			precondition: "invalid-subscription",
		},
		{
			name:         "two web push subscriptions",
			body:         webPushBody(`</P:web-push-subscription>`, `</P:web-push-subscription><P:web-push-subscription>`+validWebPushSubscription+`</P:web-push-subscription>`),
			precondition: "invalid-subscription",
		},
		{name: "no push resource", body: webPushBody("<P:push-resource>https://up.example.net/yohd4yai5Phiz1wi</P:push-resource>", ""), precondition: "invalid-subscription"},
		{name: "push resource over HTTP", body: webPushBody("https://up.example.net", "http://up.example.net"), precondition: "invalid-subscription"},
		{name: "relative push resource", body: webPushBody("https://up.example.net/", "/"), precondition: "invalid-subscription"},
		{name: "push resource with userinfo", body: webPushBody("https://up.example.net", "https://user:secret@up.example.net"), precondition: "invalid-subscription"},
		{name: "push resource with fragment", body: webPushBody("yohd4yai5Phiz1wi</P:push-resource>", "yohd4yai5Phiz1wi#frag</P:push-resource>"), precondition: "invalid-subscription"},
		{name: "unsupported content encoding", body: webPushBody(">aes128gcm<", ">aesgcm<"), precondition: "invalid-subscription"},
		{name: "no public key", body: webPushBody(`<P:subscription-public-key type="p256dh">`+testP256DH+`</P:subscription-public-key>`, ""), precondition: "invalid-subscription"},
		{name: "unsupported key type", body: webPushBody(`type="p256dh"`, `type="x25519"`), precondition: "invalid-subscription"},
		{name: "public key not base64url", body: webPushBody(testP256DH, "not base64!"), precondition: "invalid-subscription"},
		{name: "public key not on curve", body: webPushBody(testP256DH, base64.RawURLEncoding.EncodeToString(notOnCurve)), precondition: "invalid-subscription"},
		{name: "no auth secret", body: webPushBody("<P:auth-secret>"+testAuthSecret+"</P:auth-secret>", ""), precondition: "invalid-subscription"},
		{name: "short auth secret", body: webPushBody(testAuthSecret, "BTBZMqHH6r4"), precondition: "invalid-subscription"},

		{name: "no trigger", body: registerBody(validSubscription), precondition: "no-supported-trigger"},
		{name: "empty trigger", body: registerBody(validSubscription, "<P:trigger/>"), precondition: "no-supported-trigger"},
		{name: "unknown trigger", body: registerBody(validSubscription, `<P:trigger><X:moon-phase xmlns:X="urn:example"/></P:trigger>`), precondition: "no-supported-trigger"},
		{name: "invalid depth", body: registerBody(validSubscription, `<P:trigger><P:content-update><D:depth>2</D:depth></P:content-update></P:trigger>`), code: http.StatusBadRequest},
		{name: "missing depth", body: registerBody(validSubscription, `<P:trigger><P:content-update/></P:trigger>`), code: http.StatusBadRequest},
	} {
		t.Run(tc.name, func(t *testing.T) {
			b := newFakeBackend()
			contentType := tc.contentType
			if contentType == "" {
				contentType = "application/xml"
			}
			w := serveRegister(t, b, contentType, tc.body)
			if tc.precondition != "" {
				checkPrecondition(t, w, tc.precondition)
			} else if w.Code != tc.code {
				t.Errorf("status = %v, want %v; body:\n%s", w.Code, tc.code, w.Body.String())
			}
			if len(b.registers) != 0 {
				t.Errorf("RegisterPush called for an invalid request")
			}
		})
	}
}

// Subscriptions are processed in a relaxed way (draft section 4.1).
func TestServeRegisterRelaxed(t *testing.T) {
	pubKey, _ := base64.RawURLEncoding.DecodeString(testP256DH)
	authSecret, _ := base64.RawURLEncoding.DecodeString(testAuthSecret)

	for _, tc := range []struct {
		name  string
		info  PushInfo
		body  string
		check func(t *testing.T, sub *PushSubscription)
	}{
		{
			name: "padded base64 and whitespace",
			body: webPushBody(validWebPushSubscription, `<P:push-resource>
  https://up.example.net/yohd4yai5Phiz1wi
</P:push-resource>
<P:subscription-public-key type="p256dh"> `+base64.URLEncoding.EncodeToString(pubKey)+` </P:subscription-public-key>
<P:auth-secret>`+base64.URLEncoding.EncodeToString(authSecret)+`</P:auth-secret>`),
			check: func(t *testing.T, sub *PushSubscription) {
				want := WebPushSubscription{PushResource: testPushResource, PublicKey: pubKey, AuthSecret: authSecret}
				if !reflect.DeepEqual(sub.WebPush, want) {
					t.Errorf("WebPush = %+v, want %+v", sub.WebPush, want)
				}
			},
		},
		{
			name: "no content encoding or key type",
			body: webPushBody(validWebPushSubscription, `<P:push-resource>`+testPushResource+`</P:push-resource>
<P:subscription-public-key>`+testP256DH+`</P:subscription-public-key>
<P:auth-secret>`+testAuthSecret+`</P:auth-secret>`),
		},
		{name: "content encoding is case-insensitive", body: webPushBody(">aes128gcm<", ">AES128GCM<")},
		{
			name: "invalid expiration ignored",
			body: registerBody(validSubscription, validTrigger, "<P:expires>next week</P:expires>"),
			check: func(t *testing.T, sub *PushSubscription) {
				if !sub.Expires.IsZero() {
					t.Errorf("Expires = %v, want zero", sub.Expires)
				}
			},
		},
		{
			name: "unsupported trigger ignored",
			info: PushInfo{Topic: "t", ContentUpdate: &PushTrigger{Depth: internal.DepthInfinity}},
			body: registerBody(validSubscription, `<P:trigger>
  <P:property-update><D:depth>0</D:depth></P:property-update>
  <P:content-update><D:depth>0</D:depth></P:content-update>
  <X:moon-phase xmlns:X="urn:example"/>
</P:trigger>`),
			check: func(t *testing.T, sub *PushSubscription) {
				if sub.PropertyUpdate != nil {
					t.Errorf("PropertyUpdate = %+v, want nil", sub.PropertyUpdate)
				}
				// A lower depth than supported is kept
				if sub.ContentUpdate == nil || sub.ContentUpdate.Depth != internal.DepthZero {
					t.Errorf("ContentUpdate = %+v, want depth 0", sub.ContentUpdate)
				}
			},
		},
		{
			name: "property update downgraded",
			info: PushInfo{Topic: "t", PropertyUpdate: &PushTrigger{Depth: internal.DepthOne}},
			body: registerBody(validSubscription, `<P:trigger><P:property-update><D:depth>infinity</D:depth><D:prop/></P:property-update></P:trigger>`),
			check: func(t *testing.T, sub *PushSubscription) {
				if sub.ContentUpdate != nil {
					t.Errorf("ContentUpdate = %+v, want nil", sub.ContentUpdate)
				}
				want := &PushTrigger{Depth: internal.DepthOne}
				if !reflect.DeepEqual(sub.PropertyUpdate, want) {
					t.Errorf("PropertyUpdate = %+v, want %+v", sub.PropertyUpdate, want)
				}
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			b := newFakeBackend()
			if tc.info.Topic != "" {
				b.info[testPath] = &tc.info
			}
			w := serveRegister(t, b, "application/xml", tc.body)
			if w.Code != http.StatusNoContent {
				t.Fatalf("status = %v, want 204; body:\n%s", w.Code, w.Body.String())
			}
			if len(b.registers) != 1 {
				t.Fatalf("RegisterPush called %d times, want 1", len(b.registers))
			}
			if tc.check != nil {
				tc.check(t, &b.registers[0])
			}
		})
	}
}

func TestServeRegisterNotAvailable(t *testing.T) {
	b := newFakeBackend()
	delete(b.info, testPath)
	w := serveRegister(t, b, "application/xml", draftSamplePushRegister)
	checkPrecondition(t, w, "push-not-available")
	if len(b.registers) != 0 {
		t.Errorf("RegisterPush called for a collection without push")
	}

	// A malformed request is still reported as such
	w = serveRegister(t, b, "application/xml", "<oops")
	if w.Code != http.StatusBadRequest {
		t.Errorf("status = %v, want 400", w.Code)
	}
}

func TestServeRegisterBackendError(t *testing.T) {
	b := newFakeBackend()
	b.regErr = NewPushPreconditionError(PushPreconditionPushNotAvailable)
	b.reg = nil
	checkPrecondition(t, serveRegister(t, b, "application/xml", draftSamplePushRegister), "push-not-available")

	b = newFakeBackend()
	b.regErr = &internal.HTTPError{Code: http.StatusInsufficientStorage}
	b.reg = nil
	if w := serveRegister(t, b, "application/xml", draftSamplePushRegister); w.Code != http.StatusInsufficientStorage {
		t.Errorf("status = %v, want 507", w.Code)
	}

	// The Location and Expires header fields are required
	for _, reg := range []PushRegistration{
		{Expires: time.Now().Add(time.Hour)},
		{URL: "/subscriptions/a"},
	} {
		b = newFakeBackend()
		b.reg = &reg
		w := serveRegister(t, b, "application/xml", draftSamplePushRegister)
		if w.Code != http.StatusInternalServerError {
			t.Errorf("registration %+v: status = %v, want 500", reg, w.Code)
		}
	}
}

func TestServeRegisterLocation(t *testing.T) {
	for _, tc := range []struct {
		url    string
		tls    bool
		noHost bool
		want   string
	}{
		{url: "https://dav.example.org/push/a", want: "https://dav.example.org/push/a"},
		{url: "/push/a", want: "http://example.com/push/a"},
		{url: "/push/a", tls: true, want: "https://example.com/push/a"},
		{url: "a", want: "http://example.com/user/calendars/work/a"},
		// Without a host, the URL can't be absolute: the client resolves
		// the reference against the request URL (RFC 9110 section 10.2.2)
		{url: "/push/a", noHost: true, want: "/push/a"},
		{url: "a", noHost: true, tls: true, want: "/user/calendars/work/a"},
		{url: "https://dav.example.org/push/a", noHost: true, want: "https://dav.example.org/push/a"},
	} {
		b := newFakeBackend()
		b.reg.URL = tc.url
		r := httptest.NewRequest(http.MethodPost, testPath, strings.NewReader(draftSamplePushRegister))
		r.Header.Set("Content-Type", "application/xml")
		if tc.tls {
			r.TLS = &tls.ConnectionState{}
		}
		if tc.noHost {
			r.Host = ""
		}
		w := httptest.NewRecorder()
		if err := ServeRegister(w, r, b, b.info[testPath]); err != nil {
			t.Fatalf("ServeRegister() = %v", err)
		}
		if got := w.Header().Get("Location"); got != tc.want {
			t.Errorf("registration URL %q: Location = %q, want %q", tc.url, got, tc.want)
		}
	}
}

func TestServeUnregister(t *testing.T) {
	for _, tc := range []struct {
		path    string
		handled bool
		code    int
	}{
		{path: "/subscriptions/io6Efei4ooph", handled: true, code: http.StatusNoContent},
		{path: "/subscriptions/unknown", handled: true, code: http.StatusNotFound},
		{path: "/user/calendars/work/event.ics", handled: false},
	} {
		r := httptest.NewRequest(http.MethodDelete, tc.path, nil)
		w := httptest.NewRecorder()
		handled, err := ServeUnregister(w, r, newFakeBackend())
		if handled != tc.handled {
			t.Errorf("DELETE %v: handled = %v, want %v", tc.path, handled, tc.handled)
		}
		if !handled {
			continue
		}
		if err != nil {
			internal.ServeError(w, err)
		}
		if w.Code != tc.code {
			t.Errorf("DELETE %v: status = %v, want %v", tc.path, w.Code, tc.code)
		}
	}
}

func propFindRequest(t *testing.T, body string) *internal.PropFind {
	t.Helper()
	var propfind internal.PropFind
	if err := xml.Unmarshal([]byte(body), &propfind); err != nil {
		t.Fatal(err)
	}
	return &propfind
}

const propNameRequest = `<D:propfind xmlns:D="DAV:"><D:propname/></D:propfind>`

func TestAddProps(t *testing.T) {
	const pushProps = `<D:propfind xmlns:D="DAV:" xmlns:P="https://bitfire.at/webdav-push">
  <D:prop><P:transports/><P:topic/><P:supported-triggers/></D:prop>
</D:propfind>`
	errUnavailable := &internal.HTTPError{Code: http.StatusServiceUnavailable}
	errNotFound := &internal.HTTPError{Code: http.StatusNotFound}

	for _, tc := range []struct {
		name     string
		propfind string
		path     string
		infoErr  error
		props    bool // whether the push properties are added
		called   bool // whether PushInfo is called
	}{
		{name: "prop", propfind: pushProps, path: testPath, props: true, called: true},
		{name: "one prop", propfind: `<D:propfind xmlns:D="DAV:"><D:prop><D:displayname/><topic xmlns="https://bitfire.at/webdav-push"/></D:prop></D:propfind>`, path: testPath, props: true, called: true},
		{name: "propname", propfind: propNameRequest, path: testPath, props: true, called: true},
		{name: "allprop include", propfind: `<D:propfind xmlns:D="DAV:"><D:allprop/><D:include><topic xmlns="https://bitfire.at/webdav-push"/></D:include></D:propfind>`, path: testPath, props: true, called: true},
		// The push properties aren't returned by allprop requests
		{name: "allprop", propfind: `<D:propfind xmlns:D="DAV:"><D:allprop/></D:propfind>`, path: testPath},
		{name: "other props", propfind: `<D:propfind xmlns:D="DAV:"><D:prop><D:displayname/></D:prop></D:propfind>`, path: testPath},
		{name: "no push", propfind: pushProps, path: "/user/calendars/other/", called: true},
		{name: "error", propfind: pushProps, path: testPath, infoErr: errUnavailable, props: true, called: true},
		// A propname request can't report the error, and it's unknown
		// whether push is available
		{name: "propname error", propfind: propNameRequest, path: testPath, infoErr: errUnavailable, called: true},
		// A 404 error is for a path where there's no collection, hence no
		// push
		{name: "not found", propfind: pushProps, path: testPath, infoErr: errNotFound, called: true},
		{name: "propname not found", propfind: propNameRequest, path: testPath, infoErr: errNotFound, called: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			b := newFakeBackend()
			b.infoErr = tc.infoErr
			props := make(map[xml.Name]internal.PropFindFunc)
			AddProps(context.Background(), b, propFindRequest(t, tc.propfind), tc.path, props)

			if called := len(b.infoCalls) > 0; called != tc.called {
				t.Errorf("PushInfo called: %v, want %v", called, tc.called)
			}
			for _, name := range []xml.Name{TransportsName, TopicName, SupportedTriggersName} {
				f, ok := props[name]
				if ok != tc.props {
					t.Errorf("%v added: %v, want %v", name.Local, ok, tc.props)
				}
				if !ok {
					continue
				}
				_, err := f(internal.NewRawXMLElement(name, nil, nil))
				if !errors.Is(err, tc.infoErr) {
					t.Errorf("%v: error = %v, want %v", name.Local, err, tc.infoErr)
				}
			}
		})
	}
}

// A PushInfo the server can't serve as the draft requires is reported as an
// error, and registrations for it are refused. A propname request doesn't
// list the push properties.
func TestAddPropsInvalidInfo(t *testing.T) {
	const topicRequest = `<D:propfind xmlns:D="DAV:"><D:prop><topic xmlns="https://bitfire.at/webdav-push"/></D:prop></D:propfind>`
	p384Key, err := ecdsa.GenerateKey(elliptic.P384(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	one := &PushTrigger{Depth: internal.DepthOne}
	for _, tc := range []struct {
		name string
		info PushInfo
	}{
		{name: "no topic", info: PushInfo{ContentUpdate: one}},
		{name: "no trigger", info: PushInfo{Topic: "t"}},
		{name: "invalid depth", info: PushInfo{Topic: "t", ContentUpdate: &PushTrigger{Depth: internal.Depth(2)}}},
		{name: "P-384 VAPID key", info: PushInfo{Topic: "t", ContentUpdate: one, VAPIDPublicKey: &p384Key.PublicKey}},
		{name: "VAPID key off the curve", info: PushInfo{Topic: "t", ContentUpdate: one, VAPIDPublicKey: &ecdsa.PublicKey{
			Curve: elliptic.P256(), X: big.NewInt(1), Y: big.NewInt(1),
		}}},
		{name: "VAPID key without coordinates", info: PushInfo{Topic: "t", ContentUpdate: one, VAPIDPublicKey: &ecdsa.PublicKey{
			Curve: elliptic.P256(),
		}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			b := newFakeBackend()
			b.info[testPath] = &tc.info

			// Callers tell it from other errors, to leave WebDAV-Push out of
			// OPTIONS responses
			if info, err := Info(context.Background(), b, testPath); info != nil || !errors.Is(err, ErrInvalidInfo) {
				t.Errorf("Info() = %v, %v, want an ErrInvalidInfo error", info, err)
			}

			if w := serveRegister(t, b, "application/xml", draftSamplePushRegister); w.Code != http.StatusInternalServerError {
				t.Errorf("POST: status = %v, want 500", w.Code)
			}

			props := make(map[xml.Name]internal.PropFindFunc)
			AddProps(context.Background(), b, propFindRequest(t, topicRequest), testPath, props)
			f, ok := props[TopicName]
			if !ok {
				t.Fatalf("topic not added")
			}
			if _, err := f(internal.NewRawXMLElement(TopicName, nil, nil)); err == nil {
				t.Errorf("topic: no error")
			}

			props = make(map[xml.Name]internal.PropFindFunc)
			AddProps(context.Background(), b, propFindRequest(t, propNameRequest), testPath, props)
			for name := range props {
				t.Errorf("propname lists %v", name.Local)
			}
		})
	}
}

func TestDowngradeTrigger(t *testing.T) {
	zero, one, inf := internal.DepthZero, internal.DepthOne, internal.DepthInfinity
	for _, tc := range []struct {
		req, sup *internal.Depth
		want     *internal.Depth
	}{
		{req: &inf, sup: &one, want: &one},
		{req: &one, sup: &inf, want: &one},
		{req: &zero, sup: &one, want: &zero},
		{req: &inf, sup: &inf, want: &inf},
		{req: &one, sup: &zero, want: &zero},
		{req: &one, sup: nil, want: nil},
		{req: nil, sup: &one, want: nil},
	} {
		var req, sup *PushTrigger
		if tc.req != nil {
			req = &PushTrigger{Depth: *tc.req, Props: []xml.Name{internal.DisplayNameName}}
		}
		if tc.sup != nil {
			sup = &PushTrigger{Depth: *tc.sup}
		}
		got := downgradeTrigger(req, sup)
		if (got == nil) != (tc.want == nil) {
			t.Errorf("downgradeTrigger(%v, %v) = %v, want %v", tc.req, tc.sup, got, tc.want)
			continue
		}
		if got == nil {
			continue
		}
		if got.Depth != *tc.want {
			t.Errorf("downgradeTrigger(%v, %v) depth = %v, want %v", *tc.req, tc.sup, got.Depth, *tc.want)
		}
		if !reflect.DeepEqual(got.Props, req.Props) {
			t.Errorf("downgradeTrigger() props = %v, want %v", got.Props, req.Props)
		}
	}
}
