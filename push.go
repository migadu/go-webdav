package webdav

import (
	"encoding/xml"
	"net/http"
	"net/url"
	"strings"

	"github.com/emersion/go-webdav/internal"
	"github.com/emersion/go-webdav/internal/push"
)

// CapabilityPush is the DAV capability of the resources which support
// WebDAV-Push (draft-bitfire-webdav-push section 2).
//
// WebDAV-Push lets clients register push subscriptions for collections, so
// that the server notifies them of changes instead of them polling the
// collections. It's specified by draft-bitfire-webdav-push-00
// (https://github.com/bitfireAT/webdav-push), which DAVx⁵ implements. The
// CalDAV and CardDAV servers support it for backends implementing
// caldav.PushBackend and carddav.PushBackend.
var CapabilityPush = Capability("webdav-push")

// Depth is the depth of a request or of a WebDAV-Push trigger: whether it
// applies to the members of a collection (RFC 4918 section 10.2).
type Depth = internal.Depth

const (
	// DepthZero applies to the resource only.
	DepthZero = internal.DepthZero
	// DepthOne applies to the resource and its internal members.
	DepthOne = internal.DepthOne
	// DepthInfinity applies to the resource and all of its members.
	DepthInfinity = internal.DepthInfinity
)

// The push types below are aliases of types defined in an internal package,
// which the CalDAV and CardDAV servers and ServePrincipal share.

// PushInfo describes the WebDAV-Push support of a collection, served in its
// transports, topic and supported-triggers properties
// (draft-bitfire-webdav-push section 2.1).
//
// Web Push (RFC 8030) is the only push transport: clients register Web Push
// subscriptions, and push messages are encrypted for them (RFC 8291).
//
// Its fields are:
//
//   - Topic string: the push topic of the collection, which identifies it in
//     push messages. It must be unique on the server, and it must not change
//     while the collection exists, whichever URL the collection is reached
//     by. A random UUID would do.
//   - VAPIDPublicKey *ecdsa.PublicKey: if set, the public key of the
//     server's VAPID key pair, an ECDSA key on the P-256 curve (RFC 8292).
//     Clients restrict the subscriptions they create to it, and push
//     messages must then be sent with a token signed with the private key
//     (see WebPushOptions.VAPIDKey). It's usually the same for all
//     collections. Setting it is recommended: some clients, such as DAVx⁵,
//     only register subscriptions when it is.
//   - ContentUpdate, PropertyUpdate *PushTrigger: the triggers of push
//     notifications the collection supports, with the maximum depth of each.
//     At least one must be set. Clients requesting a trigger with a larger
//     depth get it with this depth, and a trigger which isn't supported is
//     ignored.
type PushInfo = push.PushInfo

// PushTrigger is a trigger of push notifications for a collection
// (draft-bitfire-webdav-push section 3.1):
//
//   - a content update is a change of the contents of the collection or of
//     its members, including the creation and the removal of members, as
//     defined for the DAV:sync-collection report (RFC 6578 section 3.5): it
//     usually changes the DAV:sync-token of the collection;
//   - a property update is a change of the WebDAV properties of the
//     collection or of its members.
//
// Its fields are:
//
//   - Depth Depth: the depth of the resources whose changes trigger push
//     notifications. DepthZero for the collection itself, DepthOne for the
//     collection and its internal members, DepthInfinity for the collection
//     and all of its members.
//   - Props []xml.Name: the properties the client is interested in, for the
//     property update trigger of a subscription. If empty, the backend
//     chooses the properties it considers useful. The backend must ignore
//     the properties it doesn't send notifications for, and may ignore the
//     list altogether. Props is unused in other triggers.
type PushTrigger = push.PushTrigger

// PushSubscription is a push subscription a client registers for a
// collection (draft-bitfire-webdav-push section 3.1).
//
// Its fields are:
//
//   - WebPush WebPushSubscription: the Web Push subscription push messages
//     are sent to.
//   - ContentUpdate, PropertyUpdate *PushTrigger: the triggers the client
//     requested and the collection supports, with the depth downgraded to
//     the maximum the collection supports. At least one is set.
//   - Expires time.Time: the expiration time the client requested, or the
//     zero time if it didn't request one.
type PushSubscription = push.PushSubscription

// WebPushSubscription is a Web Push subscription (RFC 8030), as registered by
// a client (draft-bitfire-webdav-push section 6.1).
//
// Its fields are:
//
//   - PushResource string: the URL of the push resource, an absolute https
//     URL. Push messages are delivered with POST requests to it. It
//     identifies the subscription.
//   - PublicKey []byte: the P-256 ECDH public key of the client in
//     uncompressed form, which push messages are encrypted for (RFC 8291).
//   - AuthSecret []byte: the 16-octet authentication secret of the client
//     (RFC 8291 section 3.2).
type WebPushSubscription = push.WebPushSubscription

// PushRegistration is the registration of a push subscription for a
// collection (draft-bitfire-webdav-push section 3.1.3).
//
// Its fields are:
//
//   - URL string: the registration URL, which identifies the registration.
//     The client removes the registration with a DELETE request for it, and
//     lists it in the Push-Dont-Notify header field of its requests (see
//     ParsePushDontNotify). It must not change when the registration is
//     updated. The server replies with an absolute URL: a URL reference
//     such as a path is resolved against the URL of the request, made of the
//     Host header field and of https if the request was received over TLS,
//     http otherwise. If the request has no host, the reference is only
//     resolved against its path. A server behind a reverse proxy may need to
//     return an absolute URL.
//   - Expires time.Time: the expiration time of the registration, which must
//     be set. It can be earlier than the time requested by the client.
type PushRegistration = push.PushRegistration

// PushPreconditionType is a precondition of a WebDAV-Push registration
// (draft-bitfire-webdav-push section 3.1.3).
type PushPreconditionType = push.PushPreconditionType

const (
	// PushPreconditionInvalidSubscription reports that the subscription is
	// invalid.
	PushPreconditionInvalidSubscription PushPreconditionType = push.PushPreconditionInvalidSubscription
	// PushPreconditionPushNotAvailable reports that WebDAV-Push isn't
	// available for the collection, or that the user isn't allowed to
	// register a subscription for it.
	PushPreconditionPushNotAvailable PushPreconditionType = push.PushPreconditionPushNotAvailable
	// PushPreconditionNoSupportedTrigger reports that none of the requested
	// triggers is supported.
	PushPreconditionNoSupportedTrigger PushPreconditionType = push.PushPreconditionNoSupportedTrigger
)

// NewPushPreconditionError returns an error reporting that a registration
// fails the WebDAV-Push precondition p: it's served as 403 Forbidden, with a
// DAV:error body holding the precondition.
//
// The server checks the preconditions itself. A backend can also refuse a
// registration with PushPreconditionPushNotAvailable, if the user isn't
// allowed to register it, or PushPreconditionInvalidSubscription, if it won't
// send push messages to the push resource, for instance because of its host.
func NewPushPreconditionError(p PushPreconditionType) error {
	return push.NewPushPreconditionError(p)
}

// PushMessage is a push message, which notifies the subscriptions of a
// collection of a change (draft-bitfire-webdav-push section 4.1).
//
// It's encoded by xml.Marshal as a push-message element: SendWebPush sends it
// to a Web Push subscription.
type PushMessage struct {
	// Topic is the push topic of the collection (see PushInfo.Topic). Only
	// VAPID key rotation messages have none.
	Topic string
	// ContentUpdate reports a content update.
	ContentUpdate bool
	// SyncToken is the DAV:sync-token of the collection after the content
	// update, which lets clients ignore the message if they already know this
	// state. It should be set along with ContentUpdate if the collection has
	// one.
	SyncToken string
	// PropertyUpdate reports a property update.
	PropertyUpdate bool
	// Props lists the properties of a property update: it's usually empty,
	// except for VAPID key rotation messages.
	Props []xml.Name
}

// NewVAPIDKeyRotationMessage returns a VAPID key rotation message, which
// tells a client that the VAPID key of the server is about to change, so that
// it creates a new subscription for the new key and registers it
// (draft-bitfire-webdav-push section 6.2.1). Before changing its key, the
// server should send one to every active subscription, signed with the old
// key.
func NewVAPIDKeyRotationMessage() *PushMessage {
	return &PushMessage{
		PropertyUpdate: true,
		Props:          []xml.Name{push.TransportsName},
	}
}

type pushMessageElement struct {
	XMLName        xml.Name               `xml:"https://bitfire.at/webdav-push push-message"`
	Topic          string                 `xml:"topic,omitempty"`
	ContentUpdate  *pushContentUpdateElt  `xml:"content-update"`
	PropertyUpdate *pushPropertyUpdateElt `xml:"property-update"`
}

type pushContentUpdateElt struct {
	SyncToken *internal.SyncToken `xml:"DAV: sync-token,omitempty"`
}

type pushPropertyUpdateElt struct {
	Prop *internal.Prop `xml:"DAV: prop,omitempty"`
}

// MarshalXML implements xml.Marshaler.
func (msg *PushMessage) MarshalXML(e *xml.Encoder, start xml.StartElement) error {
	elt := pushMessageElement{Topic: msg.Topic}
	if msg.ContentUpdate {
		elt.ContentUpdate = &pushContentUpdateElt{}
		if msg.SyncToken != "" {
			elt.ContentUpdate.SyncToken = &internal.SyncToken{Token: msg.SyncToken}
		}
	}
	if msg.PropertyUpdate {
		elt.PropertyUpdate = &pushPropertyUpdateElt{}
		if len(msg.Props) > 0 {
			raw := make([]internal.RawXMLValue, len(msg.Props))
			for i, name := range msg.Props {
				raw[i] = *internal.NewRawXMLElement(name, nil, nil)
			}
			elt.PropertyUpdate.Prop = &internal.Prop{Raw: raw}
		}
	}
	return e.Encode(&elt)
}

// ParsePushDontNotify parses the Push-Dont-Notify header field of a request,
// with which the client asks for no push notifications to be sent for the
// changes the request makes (draft-bitfire-webdav-push section 4.2): all
// reports whether it lists "*", for no notifications at all, and urls are the
// registration URLs listed, in the exact form the server returned them in.
// Invalid values are ignored.
//
// The server mustn't notify the registrations listed, and shouldn't send any
// notification if all is set. Backends can read the header field in an HTTP
// middleware: they should only take into account the registrations of the
// current user.
func ParsePushDontNotify(h http.Header) (all bool, urls []string) {
	for _, v := range h.Values("Push-Dont-Notify") {
		for _, elt := range splitHeaderList(v) {
			if elt == "*" {
				all = true
				continue
			}
			s, ok := unquoteHeaderString(elt)
			if !ok {
				continue
			}
			if u, err := url.Parse(s); err != nil || !u.IsAbs() || u.Host == "" {
				continue
			}
			urls = append(urls, s)
		}
	}
	return all, urls
}

// splitHeaderList splits the value of a header field defined with the #rule
// (RFC 9110 section 5.6.1) into its trimmed, non-empty elements. Commas in
// quoted strings don't separate elements.
func splitHeaderList(v string) []string {
	var l []string
	start, quoted, escaped := 0, false, false
	for i := 0; i < len(v); i++ {
		switch c := v[i]; {
		case escaped:
			escaped = false
		case quoted && c == '\\':
			escaped = true
		case c == '"':
			quoted = !quoted
		case c == ',' && !quoted:
			l = appendHeaderListElement(l, v[start:i])
			start = i + 1
		}
	}
	return appendHeaderListElement(l, v[start:])
}

func appendHeaderListElement(l []string, elt string) []string {
	if elt = strings.Trim(elt, " \t"); elt != "" {
		l = append(l, elt)
	}
	return l
}

// unquoteHeaderString decodes a quoted-string (RFC 9110 section 5.6.4). It
// reports false if s is something else.
func unquoteHeaderString(s string) (string, bool) {
	if len(s) < 2 || s[0] != '"' || s[len(s)-1] != '"' {
		return "", false
	}
	var b strings.Builder
	for i := 1; i < len(s)-1; i++ {
		c := s[i]
		switch {
		case c == '\\':
			i++
			if i == len(s)-1 {
				return "", false
			}
			b.WriteByte(s[i])
		case c == '"':
			return "", false
		default:
			b.WriteByte(c)
		}
	}
	return b.String(), true
}
