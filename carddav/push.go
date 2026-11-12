package carddav

import (
	"context"
	"encoding/xml"
	"net/http"

	"github.com/migadu/go-webdav"
	"github.com/migadu/go-webdav/internal"
	"github.com/migadu/go-webdav/internal/push"
)

// PushBackend is an optional interface a Backend can implement to support
// WebDAV-Push (draft-bitfire-webdav-push-00): clients such as DAVx⁵ register
// push subscriptions for collections, and the backend notifies them of
// changes with push messages, so that they don't need to poll the collections
// as often.
//
// The Handler serves the WebDAV-Push side of the protocol for the user
// principal, the address book home set and the address books, for those
// PushInfo returns information for: it advertises the webdav-push capability
// and serves their transports, topic and supported-triggers properties; it
// handles the registration of push subscriptions, POST requests with a
// push-register body, with the help of RegisterPush, and their removal,
// DELETE requests for the registration URLs, with the help of UnregisterPush.
//
// The backend sends the push messages, with webdav.SendWebPush. When a
// collection changes, it sends a message (see webdav.PushMessage) to each
// subscription of the collection which isn't expired and whose trigger, with
// its depth, matches the change (draft-bitfire-webdav-push sections 4 and 5),
// for instance:
//
//   - content updates: for an address book, address objects created,
//     modified or removed, by PutAddressObject, DeleteAddressObject,
//     CopyAddressObject and MoveAddressObject, or by other means (depth 1);
//     for the address book home set, address books created or removed (depth
//     1), or a change of their address objects (depth infinity);
//   - property updates: for an address book, properties modified by
//     UpdateAddressBook (depth 0); for the address book home set, the
//     properties of its address books (depth 1); for the principal, its
//     properties (depth 0).
//
// The backend mustn't notify the registrations listed in the Push-Dont-Notify
// header field of the request making the change, and shouldn't send any push
// message if it's "*": see webdav.ParsePushDontNotify. It should avoid
// sending many messages for a burst of changes, for instance by sending them
// asynchronously and merging those for the same collection (section 4.3), and
// should remove the subscriptions push services report as invalid (section
// 4.4).
//
// Registrations should be bound to the current user: only that user may
// update or remove them, or list them in Push-Dont-Notify (section 7.1).
//
// A server serving the principal with webdav.ServePrincipal, shared with a
// CalDAV server, can pass the same backend in
// webdav.ServePrincipalOptions.Push: webdav.PushBackend has the same methods.
type PushBackend interface {
	// PushInfo returns the WebDAV-Push information of the collection at
	// path, the user principal, the address book home set or an address
	// book, or nil if push isn't available for it, for instance because the
	// current user may not subscribe to it. It may be called for a path
	// where there's no collection: it then returns nil, or a 404 error.
	//
	// The draft recommends supporting content updates of depth 1 for the
	// address book home set and address books, and property updates of depth
	// 0 for address books and the principal (section 5).
	PushInfo(ctx context.Context, path string) (*webdav.PushInfo, error)
	// RegisterPush registers the push subscription sub for the collection at
	// path, for which PushInfo returned information, and returns the
	// registration.
	//
	// If the collection already has a subscription with the same push
	// resource (sub.WebPush.PushResource), RegisterPush updates it rather than
	// registering another one, and keeps its registration URL (section 3.2).
	//
	// The expiration time of the registration should take into account the
	// one requested in sub.Expires, if any, but it can be earlier: the draft
	// recommends allowing at least three days (section 3.4).
	//
	// RegisterPush can refuse a subscription with
	// webdav.NewPushPreconditionError.
	RegisterPush(ctx context.Context, path string, sub *webdav.PushSubscription) (*webdav.PushRegistration, error)
	// UnregisterPush removes the registration whose registration URL has
	// path for its path, in reply to a DELETE request (section 3.3).
	//
	// The Handler calls it for every DELETE request, before deleting an
	// address book or an address object: it returns false, and no error, if
	// path isn't the path of a registration URL, for the request to be
	// handled as usual. If there's no such registration, or it has expired,
	// it returns true and a 404 error.
	UnregisterPush(ctx context.Context, path string) (ok bool, err error)
}

// pushInfo returns the push information of the collection at p, or nil if
// the backend doesn't support push for it, or there's no collection at p.
func (b *backend) pushInfo(ctx context.Context, p string) (*webdav.PushInfo, error) {
	pb, ok := b.Backend.(PushBackend)
	if !ok {
		return nil, nil
	}
	switch b.resourceTypeAtPath(p) {
	case resourceTypeUserPrincipal, resourceTypeAddressBookHomeSet, resourceTypeAddressBook:
		return push.Info(ctx, pb, p)
	}
	return nil, nil
}

// addPushProps adds the push properties of the collection at p to props, if
// the backend supports push.
func (b *backend) addPushProps(ctx context.Context, propfind *internal.PropFind, p string, props map[xml.Name]internal.PropFindFunc) {
	if pb, ok := b.Backend.(PushBackend); ok {
		push.AddProps(ctx, pb, propfind, p, props)
	}
}

// servePush serves the WebDAV-Push requests: POST requests, which register
// push subscriptions, and DELETE requests for registration URLs. It reports
// false for other requests.
func (h *Handler) servePush(w http.ResponseWriter, r *http.Request, pb PushBackend) (bool, error) {
	switch r.Method {
	case http.MethodPost:
		b := h.newBackend()
		info, err := b.pushInfo(r.Context(), r.URL.Path)
		if err != nil {
			return true, err
		}
		return true, push.ServeRegister(w, r, pb, info)
	case http.MethodDelete:
		return push.ServeUnregister(w, r, pb)
	}
	return false, nil
}
