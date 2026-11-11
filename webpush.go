package webdav

import (
	"bytes"
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/ecdh"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/hkdf"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"encoding/xml"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

const (
	// webPushRecordSize is the record size of encrypted push messages: push
	// services needn't accept larger bodies (RFC 8030 section 7.2).
	webPushRecordSize = 4096
	// webPushHeaderSize is the size of the aes128gcm content coding header
	// of encrypted push messages: salt, record size, key ID length and key ID,
	// the 65-octet public key of the application server (RFC 8291 section 4).
	webPushHeaderSize = 16 + 4 + 1 + 65
	// maxWebPushPlaintext is the size of the largest push message which fits
	// in webPushRecordSize octets, with the content coding header, the
	// padding delimiter and the authentication tag (RFC 8291 section 4).
	maxWebPushPlaintext = webPushRecordSize - webPushHeaderSize - 1 - 16

	// defaultWebPushTTL is the TTL of push messages sent without one.
	defaultWebPushTTL = 24 * time.Hour
	// vapidTokenLifetime is the lifetime of VAPID tokens, which mustn't
	// exceed 24 hours (RFC 8292 section 2).
	vapidTokenLifetime = 12 * time.Hour
)

// EncryptWebPush encrypts a push message for a Web Push subscription, as
// required by RFC 8291: it returns the body of the request delivering it to
// the push resource, in the aes128gcm content coding (RFC 8188), with a single
// record of 4096 octets. Each call uses a new salt and a new ECDH key pair.
//
// Push services needn't accept bodies larger than 4096 octets (RFC 8030
// section 7.2), so plaintexts longer than 3993 octets are refused.
func EncryptWebPush(sub *WebPushSubscription, plaintext []byte) ([]byte, error) {
	asPriv, err := ecdh.P256().GenerateKey(rand.Reader)
	if err != nil {
		return nil, err
	}
	salt := make([]byte, 16)
	if _, err := rand.Read(salt); err != nil {
		return nil, err
	}
	return encryptWebPush(sub, plaintext, asPriv, salt)
}

// encryptWebPush encrypts a push message with the application server key
// pair asPriv and the given salt (RFC 8291 section 3.4).
func encryptWebPush(sub *WebPushSubscription, plaintext []byte, asPriv *ecdh.PrivateKey, salt []byte) ([]byte, error) {
	if len(plaintext) > maxWebPushPlaintext {
		return nil, fmt.Errorf("webdav: push message of %d octets is larger than %d octets", len(plaintext), maxWebPushPlaintext)
	}
	// Decoding the public key checks that it's on the curve (RFC 8291
	// section 7)
	uaPub, err := ecdh.P256().NewPublicKey(sub.PublicKey)
	if err != nil {
		return nil, fmt.Errorf("webdav: invalid Web Push subscription public key: %v", err)
	}
	if len(sub.AuthSecret) != 16 {
		return nil, fmt.Errorf("webdav: Web Push authentication secret is %d octets, want 16", len(sub.AuthSecret))
	}

	ecdhSecret, err := asPriv.ECDH(uaPub)
	if err != nil {
		return nil, err
	}
	asPublic := asPriv.PublicKey().Bytes()
	keyInfo := "WebPush: info\x00" + string(uaPub.Bytes()) + string(asPublic)
	ikm, err := hkdf.Key(sha256.New, ecdhSecret, sub.AuthSecret, keyInfo, 32)
	if err != nil {
		return nil, err
	}
	cek, err := hkdf.Key(sha256.New, ikm, salt, "Content-Encoding: aes128gcm\x00", 16)
	if err != nil {
		return nil, err
	}
	nonce, err := hkdf.Key(sha256.New, ikm, salt, "Content-Encoding: nonce\x00", 12)
	if err != nil {
		return nil, err
	}
	block, err := aes.NewCipher(cek)
	if err != nil {
		return nil, err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}

	// The content coding header (RFC 8188 section 2.1), with the public key
	// of the application server as the key ID (RFC 8291 section 4)
	body := make([]byte, 0, webPushHeaderSize+len(plaintext)+1+gcm.Overhead())
	body = append(body, salt...)
	body = binary.BigEndian.AppendUint32(body, webPushRecordSize)
	body = append(body, byte(len(asPublic)))
	body = append(body, asPublic...)

	// A single record: the plaintext, the padding delimiter of the last
	// record and no padding (RFC 8188 section 2). The nonce of the first
	// record is the derived nonce.
	record := make([]byte, 0, len(plaintext)+1)
	record = append(record, plaintext...)
	record = append(record, 0x02)
	return gcm.Seal(body, nonce, record, nil), nil
}

// WebPushOptions holds the options of a push message delivery request.
type WebPushOptions struct {
	// VAPIDKey, if set, is the private key of the server's VAPID key pair,
	// an ECDSA key on the P-256 curve: the request carries a token signed
	// with it in its Authorization header field (RFC 8292), which proves the
	// identity of the server to the push service. It must be set if the
	// server serves the public key in PushInfo.VAPIDPublicKey
	// (draft-bitfire-webdav-push section 6.2).
	VAPIDKey *ecdsa.PrivateKey
	// VAPIDSubject is the optional contact URI of the server, a mailto: or an
	// https: URI, sent in the token (RFC 8292 section 2.1). Other URIs are
	// refused.
	VAPIDSubject string
	// TTL is how long the push service should keep the message while the
	// client can't be reached (RFC 8030 section 5.2). It's rounded down to
	// seconds; zero means one day.
	TTL time.Duration
	// Urgency is the optional urgency of the message: "very-low", "low",
	// "normal" or "high" (RFC 8030 section 5.3). Without one, push services
	// use "normal".
	Urgency string
	// Topic, if set, lets the message replace an undelivered message with the
	// same topic (RFC 8030 section 5.4): at most 32 characters from the
	// base64url alphabet. It's visible to the push service, so it mustn't
	// hold sensitive information, and it must be chosen so that the meaning
	// of the messages doesn't change when one replaces another
	// (draft-bitfire-webdav-push section 6.4). It's unrelated to the push
	// topic of a collection.
	Topic string
}

// SendWebPush sends a push message to a Web Push subscription: it encrypts
// the XML document of msg for the subscription (RFC 8291), and requests its
// delivery with a POST request to the push resource (RFC 8030 section 5),
// over TLS (draft-bitfire-webdav-push section 7.3). The push resource must be
// an https URL without userinfo or a fragment.
//
// A status other than 2xx from the push service is returned as an
// *HTTPError. Push services reply 404 Not Found or 410 Gone for subscriptions
// which are no longer valid: the backend should remove those, possibly after
// a while (draft-bitfire-webdav-push section 4.4).
//
// If c is nil, http.DefaultClient is used, but redirects aren't followed:
// they're returned as an *HTTPError too. RFC 8030 doesn't use redirects for
// delivery requests, and following one could send the message to another
// origin, or over plain HTTP. SendWebPush can't change the redirect policy of
// c: an *http.Client should have a CheckRedirect function returning
// http.ErrUseLastResponse.
//
// Push resources are provided by clients: backends may want c to refuse to
// connect to the addresses of their internal network.
func SendWebPush(ctx context.Context, c HTTPClient, sub *WebPushSubscription, msg *PushMessage, opts *WebPushOptions) error {
	if opts == nil {
		opts = &WebPushOptions{}
	}
	pushResource, err := url.Parse(sub.PushResource)
	if err != nil {
		return fmt.Errorf("webdav: invalid push resource: %v", err)
	}
	if pushResource.Scheme != "https" || pushResource.Host == "" {
		return fmt.Errorf("webdav: push resource %q isn't an https URL", sub.PushResource)
	}
	// The push resource is the target URI of the request: it can't have
	// userinfo (RFC 9110 section 4.2.4), which the HTTP client would send as
	// credentials, and a fragment isn't part of the resource
	if pushResource.User != nil {
		return fmt.Errorf("webdav: push resource %q has userinfo", pushResource.Redacted())
	}
	if pushResource.Fragment != "" {
		return fmt.Errorf("webdav: push resource %q has a fragment", sub.PushResource)
	}

	header := make(http.Header)
	ttl := opts.TTL
	if ttl < 0 {
		return fmt.Errorf("webdav: negative Web Push TTL")
	} else if ttl == 0 {
		ttl = defaultWebPushTTL
	}
	header.Set("TTL", strconv.FormatInt(int64(ttl/time.Second), 10))
	switch opts.Urgency {
	case "":
	case "very-low", "low", "normal", "high":
		header.Set("Urgency", opts.Urgency)
	default:
		return fmt.Errorf("webdav: invalid Web Push urgency %q", opts.Urgency)
	}
	if opts.Topic != "" {
		if !isWebPushTopic(opts.Topic) {
			return fmt.Errorf("webdav: invalid Web Push topic %q", opts.Topic)
		}
		header.Set("Topic", opts.Topic)
	}
	if opts.VAPIDKey != nil {
		auth, err := vapidAuthorization(opts.VAPIDKey, opts.VAPIDSubject, pushResource, time.Now().Add(vapidTokenLifetime))
		if err != nil {
			return err
		}
		header.Set("Authorization", auth)
	}

	plaintext, err := xml.Marshal(msg)
	if err != nil {
		return err
	}
	plaintext = append([]byte(xml.Header), plaintext...)
	body, err := EncryptWebPush(sub, plaintext)
	if err != nil {
		return err
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, sub.PushResource, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header = header
	req.Header.Set("Content-Type", `application/xml; charset="UTF-8"`)
	req.Header.Set("Content-Encoding", "aes128gcm")

	if c == nil {
		noRedirect := *http.DefaultClient
		noRedirect.CheckRedirect = func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		}
		c = &noRedirect
	}
	resp, err := c.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	io.Copy(io.Discard, io.LimitReader(resp.Body, 64*1024))
	if resp.StatusCode/100 != 2 {
		return NewHTTPError(resp.StatusCode, fmt.Errorf("webdav: push service replied %v", resp.Status))
	}
	return nil
}

// isWebPushTopic reports whether s is a valid Topic header field value: at
// most 32 characters from the base64url alphabet (RFC 8030 section 5.4).
func isWebPushTopic(s string) bool {
	if len(s) > 32 {
		return false
	}
	for _, c := range s {
		switch {
		case 'A' <= c && c <= 'Z', 'a' <= c && c <= 'z', '0' <= c && c <= '9', c == '-', c == '_':
		default:
			return false
		}
	}
	return true
}

// vapidAuthorization returns the value of the Authorization header field of
// a request to pushResource, with a token signed with key which expires at
// exp (RFC 8292 sections 2 and 3).
func vapidAuthorization(key *ecdsa.PrivateKey, subject string, pushResource *url.URL, exp time.Time) (string, error) {
	if key.Curve != elliptic.P256() {
		return "", fmt.Errorf("webdav: VAPID key isn't on the P-256 curve")
	}
	pub, err := key.PublicKey.Bytes()
	if err != nil {
		return "", err
	}
	aud, err := vapidAudience(pushResource)
	if err != nil {
		return "", err
	}
	if subject != "" && !isVAPIDSubject(subject) {
		return "", fmt.Errorf("webdav: VAPID subject %q isn't a mailto: or an https: URI", subject)
	}

	claims, err := json.Marshal(struct {
		Aud string `json:"aud"`
		Exp int64  `json:"exp"`
		Sub string `json:"sub,omitempty"`
	}{
		Aud: aud,
		Exp: exp.Unix(),
		Sub: subject,
	})
	if err != nil {
		return "", err
	}

	// A JWS with ES256 (RFC 7518 section 3.4): the signature is the
	// concatenation of R and S, of 32 octets each
	enc := base64.RawURLEncoding
	input := enc.EncodeToString([]byte(`{"typ":"JWT","alg":"ES256"}`)) + "." + enc.EncodeToString(claims)
	hash := sha256.Sum256([]byte(input))
	r, s, err := ecdsa.Sign(rand.Reader, key, hash[:])
	if err != nil {
		return "", err
	}
	sig := make([]byte, 64)
	r.FillBytes(sig[:32])
	s.FillBytes(sig[32:])

	return "vapid t=" + input + "." + enc.EncodeToString(sig) + ", k=" + enc.EncodeToString(pub), nil
}

// vapidAudience returns the aud claim of the VAPID tokens of requests to
// pushResource, an https URL: the serialization of its origin (RFC 8292
// section 2, RFC 6454 section 6.1), with the host in lowercase, and the port
// in base ten if it isn't the default one.
func vapidAudience(pushResource *url.URL) (string, error) {
	host := strings.ToLower(pushResource.Hostname())
	if strings.Contains(host, ":") {
		host = "[" + host + "]"
	}
	if s := pushResource.Port(); s != "" {
		port, err := strconv.ParseUint(s, 10, 16)
		if err != nil {
			return "", fmt.Errorf("webdav: invalid push resource port %q", s)
		}
		if port != 443 {
			host += ":" + strconv.FormatUint(port, 10)
		}
	}
	return "https://" + host, nil
}

// isVAPIDSubject reports whether s is a mailto: or an https: URI, the contact
// URIs of the sub claim of VAPID tokens (RFC 8292 section 2.1).
func isVAPIDSubject(s string) bool {
	u, err := url.Parse(s)
	if err != nil {
		return false
	}
	switch u.Scheme {
	case "mailto":
		return u.Opaque != ""
	case "https":
		return u.Host != ""
	}
	return false
}
