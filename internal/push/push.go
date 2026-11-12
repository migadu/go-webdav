// Package push implements the server side of WebDAV-Push
// (draft-bitfire-webdav-push-00) for the CalDAV and CardDAV servers and
// webdav.ServePrincipal: the push properties of collections and principals,
// and the registration and removal of push subscriptions.
package push

import (
	"context"
	"crypto/ecdh"
	"crypto/elliptic"
	"encoding/base64"
	"encoding/xml"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"

	"github.com/emersion/go-webdav/internal"
)

// Namespace is the XML namespace of WebDAV-Push.
const Namespace = "https://bitfire.at/webdav-push"

// The push properties (draft-bitfire-webdav-push section 2.1).
var (
	TransportsName        = xml.Name{Space: Namespace, Local: "transports"}
	TopicName             = xml.Name{Space: Namespace, Local: "topic"}
	SupportedTriggersName = xml.Name{Space: Namespace, Local: "supported-triggers"}
)

// Backend is the WebDAV-Push part of a backend: caldav.PushBackend,
// carddav.PushBackend and webdav.PushBackend.
type Backend interface {
	PushInfo(ctx context.Context, path string) (*PushInfo, error)
	RegisterPush(ctx context.Context, path string, sub *PushSubscription) (*PushRegistration, error)
	UnregisterPush(ctx context.Context, path string) (bool, error)
}

type transports struct {
	XMLName xml.Name         `xml:"https://bitfire.at/webdav-push transports"`
	WebPush webPushTransport `xml:"web-push"`
}

type webPushTransport struct {
	VAPIDPublicKey *vapidPublicKey `xml:"vapid-public-key,omitempty"`
}

type vapidPublicKey struct {
	Type string `xml:"type,attr"`
	Key  string `xml:",chardata"`
}

type topic struct {
	XMLName xml.Name `xml:"https://bitfire.at/webdav-push topic"`
	Topic   string   `xml:",chardata"`
}

type supportedTriggers struct {
	XMLName        xml.Name      `xml:"https://bitfire.at/webdav-push supported-triggers"`
	ContentUpdate  *triggerDepth `xml:"content-update,omitempty"`
	PropertyUpdate *triggerDepth `xml:"property-update,omitempty"`
}

type triggerDepth struct {
	Depth string `xml:"DAV: depth"`
}

// requested reports whether a PROPFIND request may return push properties:
// if it names one of them, or asks for all property names. They aren't
// returned by allprop requests, unless they're included.
func requested(propfind *internal.PropFind) bool {
	if propfind.PropName != nil {
		return true
	}
	var raws []internal.RawXMLValue
	if propfind.Prop != nil {
		raws = propfind.Prop.Raw
	} else if propfind.AllProp != nil && propfind.Include != nil {
		raws = propfind.Include.Raw
	}
	for i := range raws {
		if name, ok := raws[i].XMLName(); ok && name.Space == Namespace {
			return true
		}
	}
	return false
}

// ErrInvalidInfo is wrapped by the error Info returns for push information
// which can't be served as the draft requires. The server can't meet the
// requirements of the draft for the collection then, so OPTIONS doesn't
// advertise WebDAV-Push for it (draft-bitfire-webdav-push section 2), while
// the push properties and registrations fail with the error.
var ErrInvalidInfo = errors.New("webdav: invalid push information")

// Info returns the push information of the collection at path, or nil if push
// isn't available for it. Backends may return a 404 error for a path where
// there's no collection: push isn't available there either. Information
// which can't be served as the draft requires is reported with an error
// wrapping ErrInvalidInfo.
func Info(ctx context.Context, b Backend, path string) (*PushInfo, error) {
	info, err := b.PushInfo(ctx, path)
	if internal.IsNotFound(err) {
		return nil, nil
	} else if err != nil {
		return nil, err
	}
	if info != nil {
		if err := checkInfo(info); err != nil {
			return nil, err
		}
	}
	return info, nil
}

// AddProps adds the push properties of the collection at path to props, if
// the PROPFIND request may return them and push is available for the
// collection: they mustn't be served for other resources
// (draft-bitfire-webdav-push section 2.1). If Info fails, the properties are
// reported with its error, except for a propname request, which doesn't list
// them.
func AddProps(ctx context.Context, b Backend, propfind *internal.PropFind, path string, props map[xml.Name]internal.PropFindFunc) {
	if !requested(propfind) {
		return
	}
	info, err := Info(ctx, b, path)
	if err == nil && info == nil {
		return
	}
	if err != nil {
		// A propname response lists names without calling the PropFindFuncs,
		// so it can't carry the error, and whether push is available is
		// unknown: leave the properties out
		if propfind.PropName != nil {
			return
		}
		f := func(*internal.RawXMLValue) (interface{}, error) {
			return nil, err
		}
		props[TransportsName] = f
		props[TopicName] = f
		props[SupportedTriggersName] = f
		return
	}

	var vapidKey *vapidPublicKey
	if info.VAPIDPublicKey != nil {
		// Info checked the key
		b, _ := info.VAPIDPublicKey.Bytes()
		vapidKey = &vapidPublicKey{Type: "p256ecdsa", Key: base64.RawURLEncoding.EncodeToString(b)}
	}
	triggers := supportedTriggers{}
	if info.ContentUpdate != nil {
		triggers.ContentUpdate = &triggerDepth{Depth: info.ContentUpdate.Depth.String()}
	}
	if info.PropertyUpdate != nil {
		triggers.PropertyUpdate = &triggerDepth{Depth: info.PropertyUpdate.Depth.String()}
	}

	props[TransportsName] = internal.PropFindValue(&transports{
		WebPush: webPushTransport{VAPIDPublicKey: vapidKey},
	})
	props[TopicName] = internal.PropFindValue(&topic{Topic: info.Topic})
	props[SupportedTriggersName] = internal.PropFindValue(&triggers)
}

// checkInfo checks that the push information returned by a backend can be
// served as the draft requires. Its errors wrap ErrInvalidInfo.
func checkInfo(info *PushInfo) error {
	if info.Topic == "" {
		return fmt.Errorf("%w: no topic", ErrInvalidInfo)
	}
	if info.ContentUpdate == nil && info.PropertyUpdate == nil {
		return fmt.Errorf("%w: no supported trigger", ErrInvalidInfo)
	}
	for _, t := range []*PushTrigger{info.ContentUpdate, info.PropertyUpdate} {
		if t != nil && !validDepth(t.Depth) {
			return fmt.Errorf("%w: trigger with an invalid depth %d", ErrInvalidInfo, int(t.Depth))
		}
	}
	if k := info.VAPIDPublicKey; k != nil {
		if k.Curve != elliptic.P256() {
			return fmt.Errorf("%w: VAPID public key isn't on the P-256 curve", ErrInvalidInfo)
		}
		// Bytes panics if a coordinate is missing, and fails if the point
		// isn't on the curve
		if k.X == nil || k.Y == nil {
			return fmt.Errorf("%w: VAPID public key without coordinates", ErrInvalidInfo)
		}
		if _, err := k.Bytes(); err != nil {
			return fmt.Errorf("%w: VAPID public key: %v", ErrInvalidInfo, err)
		}
	}
	return nil
}

func validDepth(d internal.Depth) bool {
	switch d {
	case internal.DepthZero, internal.DepthOne, internal.DepthInfinity:
		return true
	}
	return false
}

// https://github.com/bitfireAT/webdav-push, appendix A

type pushRegister struct {
	XMLName      xml.Name       `xml:"https://bitfire.at/webdav-push push-register"`
	Subscription []subscription `xml:"https://bitfire.at/webdav-push subscription"`
	Trigger      []trigger      `xml:"https://bitfire.at/webdav-push trigger"`
	Expires      string         `xml:"https://bitfire.at/webdav-push expires"`
}

type subscription struct {
	WebPush []webPushSubscription  `xml:"https://bitfire.at/webdav-push web-push-subscription"`
	Other   []internal.RawXMLValue `xml:",any"`
}

type webPushSubscription struct {
	PushResource    []string             `xml:"https://bitfire.at/webdav-push push-resource"`
	ContentEncoding []string             `xml:"https://bitfire.at/webdav-push content-encoding"`
	PublicKey       []subscriptionPubKey `xml:"https://bitfire.at/webdav-push subscription-public-key"`
	AuthSecret      []string             `xml:"https://bitfire.at/webdav-push auth-secret"`
}

type subscriptionPubKey struct {
	Type string `xml:"type,attr"`
	Key  string `xml:",chardata"`
}

type trigger struct {
	ContentUpdate  []triggerUpdate `xml:"https://bitfire.at/webdav-push content-update"`
	PropertyUpdate []triggerUpdate `xml:"https://bitfire.at/webdav-push property-update"`
}

type triggerUpdate struct {
	Depth []string       `xml:"DAV: depth"`
	Prop  *internal.Prop `xml:"DAV: prop"`
}

var (
	errInvalidSubscription = NewPushPreconditionError(PushPreconditionInvalidSubscription)
	errPushNotAvailable    = NewPushPreconditionError(PushPreconditionPushNotAvailable)
	errNoSupportedTrigger  = NewPushPreconditionError(PushPreconditionNoSupportedTrigger)
)

// ServeRegister serves a push subscription registration, a POST request with
// a push-register body (draft-bitfire-webdav-push section 3.1), for the
// collection at r.URL.Path. info is the push information of the collection,
// as returned by Info: nil if push isn't available for it.
func ServeRegister(w http.ResponseWriter, r *http.Request, b Backend, info *PushInfo) error {
	var req pushRegister
	if err := internal.DecodeXMLRequest(r, &req); err != nil {
		return err
	}

	if info == nil {
		return errPushNotAvailable
	}

	sub := PushSubscription{}
	if len(req.Subscription) != 1 {
		return errInvalidSubscription
	}
	webPush, err := decodeWebPushSubscription(&req.Subscription[0])
	if err != nil {
		return err
	}
	sub.WebPush = *webPush

	// Triggers which aren't supported are downgraded or ignored (section
	// 3.1)
	if len(req.Trigger) > 1 {
		return internal.HTTPErrorf(http.StatusBadRequest, "webdav: more than one trigger in push-register request")
	} else if len(req.Trigger) == 0 {
		return errNoSupportedTrigger
	}
	contentUpdate, err := decodeTrigger(req.Trigger[0].ContentUpdate, false)
	if err != nil {
		return err
	}
	propertyUpdate, err := decodeTrigger(req.Trigger[0].PropertyUpdate, true)
	if err != nil {
		return err
	}
	sub.ContentUpdate = downgradeTrigger(contentUpdate, info.ContentUpdate)
	sub.PropertyUpdate = downgradeTrigger(propertyUpdate, info.PropertyUpdate)
	if sub.ContentUpdate == nil && sub.PropertyUpdate == nil {
		return errNoSupportedTrigger
	}

	// An invalid expiration time is ignored: the server replies with the
	// one it chose anyway
	if s := strings.TrimSpace(req.Expires); s != "" {
		if t, err := http.ParseTime(s); err == nil {
			sub.Expires = t
		}
	}

	reg, err := b.RegisterPush(r.Context(), r.URL.Path, &sub)
	if err != nil {
		return err
	}
	if reg == nil || reg.URL == "" || reg.Expires.IsZero() {
		return fmt.Errorf("webdav: push registration without a URL or an expiration time")
	}
	loc, err := absoluteURL(r, reg.URL)
	if err != nil {
		return err
	}

	// There is no additional information to return for Web Push
	// subscriptions: 204 No Content (section 3.1.3)
	w.Header().Set("Location", loc)
	w.Header().Set("Expires", reg.Expires.UTC().Format(http.TimeFormat))
	w.WriteHeader(http.StatusNoContent)
	return nil
}

// decodeWebPushSubscription decodes the subscription of a registration,
// which must hold a single Web Push subscription with the encryption keys
// (draft-bitfire-webdav-push sections 6.1 and 6.3).
func decodeWebPushSubscription(sub *subscription) (*WebPushSubscription, error) {
	if len(sub.WebPush) != 1 || len(sub.Other) != 0 {
		return nil, errInvalidSubscription
	}
	wp := &sub.WebPush[0]
	if len(wp.PushResource) != 1 || len(wp.ContentEncoding) > 1 || len(wp.PublicKey) != 1 || len(wp.AuthSecret) != 1 {
		return nil, errInvalidSubscription
	}

	// HTTP over TLS must be used with push services (section 7.3). The push
	// resource is the target URI of the push message requests: it can't have
	// userinfo (RFC 9110 section 4.2.4) or a fragment, which
	// webdav.SendWebPush refuses too.
	pushResource := strings.TrimSpace(wp.PushResource[0])
	if u, err := url.Parse(pushResource); err != nil || u.Scheme != "https" || u.Host == "" || u.User != nil || u.Fragment != "" {
		return nil, errInvalidSubscription
	}

	// Push messages must be encrypted (section 6), and aes128gcm is the only
	// content coding
	if len(wp.ContentEncoding) == 1 && !strings.EqualFold(strings.TrimSpace(wp.ContentEncoding[0]), "aes128gcm") {
		return nil, errInvalidSubscription
	}
	if t := strings.TrimSpace(wp.PublicKey[0].Type); t != "" && t != "p256dh" {
		return nil, errInvalidSubscription
	}
	pubKey, err := decodeBase64URL(wp.PublicKey[0].Key)
	if err != nil {
		return nil, errInvalidSubscription
	}
	// The key must be on the curve (RFC 8291 section 7)
	if _, err := ecdh.P256().NewPublicKey(pubKey); err != nil {
		return nil, errInvalidSubscription
	}
	authSecret, err := decodeBase64URL(wp.AuthSecret[0])
	if err != nil || len(authSecret) != 16 {
		return nil, errInvalidSubscription
	}

	return &WebPushSubscription{
		PushResource: pushResource,
		PublicKey:    pubKey,
		AuthSecret:   authSecret,
	}, nil
}

// decodeBase64URL decodes base64url data, with or without padding.
func decodeBase64URL(s string) ([]byte, error) {
	s = strings.TrimRight(strings.TrimSpace(s), "=")
	return base64.RawURLEncoding.DecodeString(s)
}

// decodeTrigger decodes the content-update or property-update element of a
// registration, if any.
func decodeTrigger(l []triggerUpdate, props bool) (*PushTrigger, error) {
	if len(l) == 0 {
		return nil, nil
	} else if len(l) > 1 {
		return nil, internal.HTTPErrorf(http.StatusBadRequest, "webdav: duplicate trigger in push-register request")
	}
	tu := &l[0]
	if len(tu.Depth) != 1 {
		return nil, internal.HTTPErrorf(http.StatusBadRequest, "webdav: push trigger without a single depth")
	}
	depth, err := internal.ParseDepth(strings.TrimSpace(tu.Depth[0]))
	if err != nil {
		return nil, &internal.HTTPError{Code: http.StatusBadRequest, Err: err}
	}
	t := &PushTrigger{Depth: depth}
	if props && tu.Prop != nil {
		for i := range tu.Prop.Raw {
			if name, ok := tu.Prop.Raw[i].XMLName(); ok {
				t.Props = append(t.Props, name)
			}
		}
	}
	return t, nil
}

// depthOrder orders depths from the lowest to the highest.
func depthOrder(d internal.Depth) int {
	if d == internal.DepthInfinity {
		return 2
	}
	return int(d)
}

// downgradeTrigger returns the trigger req, requested by a client, as
// supported by a collection: nil if the collection doesn't support it, and
// with the depth sup supports at most (draft-bitfire-webdav-push sections
// 3.1.1 and 3.1.2).
func downgradeTrigger(req, sup *PushTrigger) *PushTrigger {
	if req == nil || sup == nil {
		return nil
	}
	t := *req
	if depthOrder(t.Depth) > depthOrder(sup.Depth) {
		t.Depth = sup.Depth
	}
	return &t
}

// absoluteURL resolves ref against the URL of the request r, for the Location
// header field of a registration, which must be an absolute URL
// (draft-bitfire-webdav-push section 3.1.3). An absolute URL is returned as
// is. It fails if the result has no host, which a client couldn't send the
// DELETE request removing the registration to: for a path if r has no host,
// as the URL of r isn't known then, or for an absolute URL without a host.
func absoluteURL(r *http.Request, ref string) (string, error) {
	u, err := url.Parse(ref)
	if err != nil {
		return "", fmt.Errorf("webdav: invalid push registration URL %q: %v", ref, err)
	}
	if u.IsAbs() && u.Host != "" {
		return ref, nil
	}
	base := url.URL{Scheme: "http", Host: r.Host, Path: r.URL.Path}
	if r.TLS != nil {
		base.Scheme = "https"
	}
	loc := base.ResolveReference(u)
	if loc.Host == "" {
		return "", fmt.Errorf("webdav: can't make push registration URL %q absolute, with request host %q", ref, r.Host)
	}
	return loc.String(), nil
}

// ServeUnregister serves the removal of a push subscription registration, a
// DELETE request for its registration URL (draft-bitfire-webdav-push section
// 3.3). It reports false if the request isn't for a registration URL, and
// must then be served as an ordinary DELETE request.
func ServeUnregister(w http.ResponseWriter, r *http.Request, b Backend) (bool, error) {
	ok, err := b.UnregisterPush(r.Context(), r.URL.Path)
	if err != nil {
		return true, err
	} else if !ok {
		return false, nil
	}
	w.WriteHeader(http.StatusNoContent)
	return true, nil
}
