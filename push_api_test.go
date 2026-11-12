package webdav_test

import (
	"context"
	"crypto/ecdsa"
	"encoding/xml"
	"errors"
	"net/http"
	"reflect"
	"testing"
	"time"

	"github.com/migadu/go-webdav"
)

// The push types are aliases of internal types. This test only needs to
// compile: it uses each of their fields, constants and functions as a user
// of the package would, with the types they had before they became aliases.
func TestPushAPI(t *testing.T) {
	var (
		key       *ecdsa.PublicKey
		depth     webdav.Depth = webdav.DepthOne
		props     []xml.Name
		data      []byte
		date      time.Time
		s         string
		trigger   *webdav.PushTrigger
		webPush   webdav.WebPushSubscription
		condition webdav.PushPreconditionType
	)

	info := webdav.PushInfo{
		Topic:          s,
		VAPIDPublicKey: key,
		ContentUpdate:  &webdav.PushTrigger{Depth: depth, Props: props},
		PropertyUpdate: trigger,
	}
	s, key, trigger, trigger = info.Topic, info.VAPIDPublicKey, info.ContentUpdate, info.PropertyUpdate
	depth, props = info.ContentUpdate.Depth, info.ContentUpdate.Props

	sub := webdav.PushSubscription{
		WebPush:        webdav.WebPushSubscription{PushResource: s, PublicKey: data, AuthSecret: data},
		ContentUpdate:  trigger,
		PropertyUpdate: trigger,
		Expires:        date,
	}
	webPush, trigger, trigger, date = sub.WebPush, sub.ContentUpdate, sub.PropertyUpdate, sub.Expires
	s, data, data = webPush.PushResource, webPush.PublicKey, webPush.AuthSecret

	reg := webdav.PushRegistration{URL: s, Expires: date}
	s, date = reg.URL, reg.Expires

	const (
		invalidSubscription webdav.PushPreconditionType = webdav.PushPreconditionInvalidSubscription
		pushNotAvailable    webdav.PushPreconditionType = webdav.PushPreconditionPushNotAvailable
		noSupportedTrigger  webdav.PushPreconditionType = webdav.PushPreconditionNoSupportedTrigger
	)
	for p, want := range map[webdav.PushPreconditionType]string{
		invalidSubscription: "invalid-subscription",
		pushNotAvailable:    "push-not-available",
		noSupportedTrigger:  "no-supported-trigger",
	} {
		if string(p) != want {
			t.Errorf("precondition %q, want %q", p, want)
		}
	}
	condition = webdav.PushPreconditionType("other")
	var newError func(webdav.PushPreconditionType) error = webdav.NewPushPreconditionError
	var httpErr *webdav.HTTPError
	if err := newError(condition); !errors.As(err, &httpErr) || httpErr.Code != http.StatusForbidden {
		t.Errorf("NewPushPreconditionError() = %v, want a 403 error", err)
	}

	var _ func(*webdav.WebPushSubscription, []byte) ([]byte, error) = webdav.EncryptWebPush
	var _ func(context.Context, webdav.HTTPClient, *webdav.WebPushSubscription, *webdav.PushMessage, *webdav.WebPushOptions) error = webdav.SendWebPush

	// An embedded field is named after the alias
	var embedded struct {
		webdav.PushInfo
		webdav.PushTrigger
		webdav.PushSubscription
		webdav.WebPushSubscription
		webdav.PushRegistration
	}
	info, sub, webPush, reg = embedded.PushInfo, embedded.PushSubscription, embedded.WebPushSubscription, embedded.PushRegistration
	trigger = &embedded.PushTrigger

	// The types keep their names, which %T and reflection report along
	// with the internal package
	for want, v := range map[string]interface{}{
		"PushInfo":             info,
		"PushTrigger":          *trigger,
		"PushSubscription":     sub,
		"WebPushSubscription":  webPush,
		"PushRegistration":     reg,
		"PushPreconditionType": condition,
	} {
		if got := reflect.TypeOf(v).Name(); got != want {
			t.Errorf("type name = %v, want %v", got, want)
		}
	}
}
