package push

import (
	"crypto/ecdsa"
	"encoding/xml"
	"net/http"
	"time"

	"github.com/migadu/go-webdav/internal"
)

// The types of this file are exported by the webdav package as aliases, with
// the same names: they're defined here so that webdav.ServePrincipal can call
// this package without an import cycle. Users read the documentation of the
// aliases, which must be kept in sync with the field comments below.

// PushInfo is webdav.PushInfo: the WebDAV-Push support of a collection.
type PushInfo struct {
	// Topic is the push topic of the collection, which identifies it in push
	// messages: it must be unique on the server, and it must not change while
	// the collection exists, whichever URL the collection is reached by. A
	// random UUID would do.
	Topic string
	// VAPIDPublicKey, if set, is the public key of the server's VAPID key
	// pair, an ECDSA key on the P-256 curve (RFC 8292): clients restrict the
	// subscriptions they create to it, and push messages must then be sent
	// with a token signed with the private key (see
	// webdav.WebPushOptions.VAPIDKey). It's usually the same for all
	// collections. Setting it is recommended: some clients, such as DAVx⁵,
	// only register subscriptions when it is.
	VAPIDPublicKey *ecdsa.PublicKey
	// ContentUpdate and PropertyUpdate are the triggers of push notifications
	// the collection supports, with the maximum depth of each: at least one
	// must be set. Clients requesting a trigger with a larger depth get it
	// with this depth, and a trigger which isn't supported is ignored.
	ContentUpdate  *PushTrigger
	PropertyUpdate *PushTrigger
}

// PushTrigger is webdav.PushTrigger: a trigger of push notifications for a
// collection.
type PushTrigger struct {
	// Depth is the depth of the resources whose changes trigger push
	// notifications: DepthZero for the collection itself, DepthOne for the
	// collection and its internal members, DepthInfinity for the collection
	// and all of its members.
	Depth internal.Depth
	// Props lists the properties the client is interested in, for the
	// property update trigger of a subscription: if empty, the backend
	// chooses the properties it considers useful. The backend must ignore
	// the properties it doesn't send notifications for, and may ignore the
	// list altogether. Props is unused in other triggers.
	Props []xml.Name
}

// PushSubscription is webdav.PushSubscription: a push subscription a client
// registers for a collection.
type PushSubscription struct {
	// WebPush is the Web Push subscription push messages are sent to.
	WebPush WebPushSubscription
	// ContentUpdate and PropertyUpdate are the triggers the client requested
	// and the collection supports, with the depth downgraded to the maximum
	// the collection supports: at least one is set.
	ContentUpdate  *PushTrigger
	PropertyUpdate *PushTrigger
	// Expires is the expiration time the client requested, or the zero time
	// if it didn't request one.
	Expires time.Time
}

// WebPushSubscription is webdav.WebPushSubscription: a Web Push subscription
// (RFC 8030), as registered by a client.
type WebPushSubscription struct {
	// PushResource is the URL of the push resource, an absolute https URL:
	// push messages are delivered with POST requests to it. It identifies the
	// subscription.
	PushResource string
	// PublicKey is the P-256 ECDH public key of the client in uncompressed
	// form, which push messages are encrypted for (RFC 8291).
	PublicKey []byte
	// AuthSecret is the 16-octet authentication secret of the client (RFC
	// 8291 section 3.2).
	AuthSecret []byte
}

// PushRegistration is webdav.PushRegistration: the registration of a push
// subscription for a collection.
type PushRegistration struct {
	// URL is the registration URL, which identifies the registration: the
	// client removes the registration with a DELETE request for it, and lists
	// it in the Push-Dont-Notify header field of its requests (see
	// webdav.ParsePushDontNotify). It must not change when the registration
	// is updated.
	//
	// The server replies with an absolute URL: a URL reference such as a
	// path is resolved against the URL of the request, made of the Host
	// header field and of https if the request was received over TLS, http
	// otherwise. The registration fails if that gives a URL without a host,
	// for instance for a path when the request has no host. A server behind
	// a reverse proxy may need to return an absolute URL.
	URL string
	// Expires is the expiration time of the registration, which must be set.
	// It can be earlier than the time requested by the client.
	Expires time.Time
}

// PushPreconditionType is webdav.PushPreconditionType: a precondition of a
// WebDAV-Push registration.
type PushPreconditionType string

// The preconditions of a WebDAV-Push registration, see the constants of the
// webdav package.
const (
	PushPreconditionInvalidSubscription PushPreconditionType = "invalid-subscription"
	PushPreconditionPushNotAvailable    PushPreconditionType = "push-not-available"
	PushPreconditionNoSupportedTrigger  PushPreconditionType = "no-supported-trigger"
)

// NewPushPreconditionError is webdav.NewPushPreconditionError: it returns an
// error reporting that a registration fails the WebDAV-Push precondition p,
// served as 403 Forbidden with a DAV:error body holding the precondition.
func NewPushPreconditionError(p PushPreconditionType) error {
	return internal.NewConditionError(http.StatusForbidden, xml.Name{Space: Namespace, Local: string(p)})
}
