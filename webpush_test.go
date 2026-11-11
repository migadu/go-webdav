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
	"errors"
	"fmt"
	"io"
	"math/big"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"
)

// The example of RFC 8291 section 5, with the intermediate values of
// appendix A.
const (
	rfc8291Plaintext  = "When I grow up, I want to be a watermelon"
	rfc8291UAPublic   = "BCVxsr7N_eNgVRqvHtD0zTZsEc6-VV-JvLexhqUzORcxaOzi6-AYWXvTBHm4bjyPjs7Vd8pZGH6SRpkNtoIAiw4"
	rfc8291UAPrivate  = "q1dXpw3UpT5VOmu_cf_v6ih07Aems3njxI-JWgLcM94"
	rfc8291ASPrivate  = "yfWPiYE-n46HLnH0KqZOF1fJJU3MYrct3AELtAQ-oRw"
	rfc8291Salt       = "DGv6ra1nlYgDCS1FRnbzlw"
	rfc8291AuthSecret = "BTBZMqHH6r4Tts7J_aSIgg"
	rfc8291Body       = "DGv6ra1nlYgDCS1FRnbzlwAAEABBBP4z9KsN6nGRTbVYI_c7VJSPQTBtkgcy27ml" +
		"mlMoZIIgDll6e3vCYLocInmYWAmS6TlzAC8wEqKK6PBru3jl7A_yl95bQpu6cVPT" +
		"pK4Mqgkf1CXztLVBSt2Ks3oZwbuwXPXLWyouBWLVWGNWQexSgSxsj_Qulcy4a-fN"
)

// The example of RFC 8292 section 2.4: a JWT and the key that signed it.
const (
	rfc8292Token = "eyJ0eXAiOiJKV1QiLCJhbGciOiJFUzI1NiJ9." +
		"eyJhdWQiOiJodHRwczovL3B1c2guZXhhbXBsZS5uZXQiLCJleHAiOjE0NTM1MjM3NjgsInN1YiI6Im1haWx0bzpwdXNoQGV4YW1wbGUuY29tIn0." +
		"i3CYb7t4xfxCDquptFOepC9GAu_HLGkMlMuCGSK2rpiUfnK9ojFwDXb1JrErtmysazNjjvW2L9OkSSHzvoD1oA"
	rfc8292Key = "BA1Hxzyi1RUM1b5wjxsn7nGxAszw2u61m164i3MrAIxHF6YK5h4SDYic-dRuU_RCPCfA5aq9ojSwk5Y2EmClBPs"
)

func b64(t *testing.T, s string) []byte {
	t.Helper()
	b, err := base64.RawURLEncoding.DecodeString(s)
	if err != nil {
		t.Fatalf("invalid base64url %q: %v", s, err)
	}
	return b
}

// decryptWebPush decrypts a push message as a user agent does (RFC 8291
// section 3.4), checking the restrictions of section 4.
func decryptWebPush(uaPriv *ecdh.PrivateKey, authSecret, body []byte) ([]byte, error) {
	if len(body) < 21 {
		return nil, errors.New("truncated header")
	}
	salt := body[:16]
	rs := binary.BigEndian.Uint32(body[16:20])
	idlen := int(body[20])
	if idlen != 65 || len(body) < 21+idlen {
		return nil, fmt.Errorf("keyid is %d octets, want 65", idlen)
	}
	asPub, err := ecdh.P256().NewPublicKey(body[21 : 21+idlen])
	if err != nil {
		return nil, fmt.Errorf("invalid keyid: %v", err)
	}
	ciphertext := body[21+idlen:]
	if uint32(len(ciphertext)) > rs {
		return nil, fmt.Errorf("more than a single record of %d octets", rs)
	}

	ecdhSecret, err := uaPriv.ECDH(asPub)
	if err != nil {
		return nil, err
	}
	keyInfo := "WebPush: info\x00" + string(uaPriv.PublicKey().Bytes()) + string(asPub.Bytes())
	ikm, err := hkdf.Key(sha256.New, ecdhSecret, authSecret, keyInfo, 32)
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
	record, err := gcm.Open(nil, nonce, ciphertext, nil)
	if err != nil {
		return nil, err
	}
	record = bytes.TrimRight(record, "\x00")
	if len(record) == 0 || record[len(record)-1] != 0x02 {
		return nil, errors.New("missing padding delimiter 0x02")
	}
	return record[:len(record)-1], nil
}

func newTestWebPushSubscription(t *testing.T, pushResource string) (*WebPushSubscription, *ecdh.PrivateKey) {
	t.Helper()
	uaPriv, err := ecdh.P256().GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	authSecret := make([]byte, 16)
	if _, err := rand.Read(authSecret); err != nil {
		t.Fatal(err)
	}
	return &WebPushSubscription{
		PushResource: pushResource,
		PublicKey:    uaPriv.PublicKey().Bytes(),
		AuthSecret:   authSecret,
	}, uaPriv
}

// The decryption used by these tests decrypts the example of RFC 8291.
func TestDecryptWebPushRFC8291(t *testing.T) {
	uaPriv, err := ecdh.P256().NewPrivateKey(b64(t, rfc8291UAPrivate))
	if err != nil {
		t.Fatal(err)
	}
	got, err := decryptWebPush(uaPriv, b64(t, rfc8291AuthSecret), b64(t, rfc8291Body))
	if err != nil {
		t.Fatalf("decryptWebPush() = %v", err)
	}
	if string(got) != rfc8291Plaintext {
		t.Errorf("decrypted %q, want %q", got, rfc8291Plaintext)
	}
}

// With the keys and salt of the example of RFC 8291, the encryption gives the
// push message body of section 5.
func TestEncryptWebPushRFC8291(t *testing.T) {
	asPriv, err := ecdh.P256().NewPrivateKey(b64(t, rfc8291ASPrivate))
	if err != nil {
		t.Fatal(err)
	}
	sub := &WebPushSubscription{
		PushResource: "https://push.example.net/push/JzLQ3raZJfFBR0aqvOMsLrt54w4rJUsV",
		PublicKey:    b64(t, rfc8291UAPublic),
		AuthSecret:   b64(t, rfc8291AuthSecret),
	}
	got, err := encryptWebPush(sub, []byte(rfc8291Plaintext), asPriv, b64(t, rfc8291Salt))
	if err != nil {
		t.Fatalf("encryptWebPush() = %v", err)
	}
	if want := b64(t, rfc8291Body); !bytes.Equal(got, want) {
		t.Errorf("encryptWebPush() =\n%s\nwant\n%s", base64.RawURLEncoding.EncodeToString(got), rfc8291Body)
	}
}

func TestEncryptWebPushRoundTrip(t *testing.T) {
	sub, uaPriv := newTestWebPushSubscription(t, "https://push.example.net/sub")

	for _, plaintext := range [][]byte{
		[]byte(draftSamplePushMessage),
		{},
		bytes.Repeat([]byte("x"), 3993), // the most a 4096-octet body holds
	} {
		body, err := EncryptWebPush(sub, plaintext)
		if err != nil {
			t.Fatalf("EncryptWebPush() = %v", err)
		}
		if len(body) > 4096 {
			t.Errorf("body is %d octets, push services needn't accept more than 4096", len(body))
		}
		got, err := decryptWebPush(uaPriv, sub.AuthSecret, body)
		if err != nil {
			t.Fatalf("decryptWebPush() = %v", err)
		}
		if !bytes.Equal(got, plaintext) {
			t.Errorf("decrypted %q, want %q", got, plaintext)
		}
	}

	// The salt and the application server key pair are new for every
	// message
	a, _ := EncryptWebPush(sub, []byte("same"))
	b, _ := EncryptWebPush(sub, []byte("same"))
	if bytes.Equal(a[:16], b[:16]) || bytes.Equal(a[21:86], b[21:86]) {
		t.Errorf("two messages share their salt or application server key")
	}
}

func TestEncryptWebPushInvalid(t *testing.T) {
	valid, _ := newTestWebPushSubscription(t, "https://push.example.net/sub")
	notOnCurve := append([]byte(nil), valid.PublicKey...)
	notOnCurve[64] ^= 1

	for _, tc := range []struct {
		name      string
		sub       WebPushSubscription
		plaintext []byte
	}{
		{"public key not on curve", WebPushSubscription{PublicKey: notOnCurve, AuthSecret: valid.AuthSecret}, nil},
		{"truncated public key", WebPushSubscription{PublicKey: valid.PublicKey[:33], AuthSecret: valid.AuthSecret}, nil},
		{"short auth secret", WebPushSubscription{PublicKey: valid.PublicKey, AuthSecret: valid.AuthSecret[:8]}, nil},
		{"plaintext too long", *valid, bytes.Repeat([]byte("x"), 3994)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := EncryptWebPush(&tc.sub, tc.plaintext); err == nil {
				t.Errorf("EncryptWebPush() succeeded, want an error")
			}
		})
	}
}

type vapidClaims struct {
	Aud string `json:"aud"`
	Exp int64  `json:"exp"`
	Sub string `json:"sub"`
}

// verifyVAPID checks the signature of a JWT signed with ES256 (RFC 7518
// section 3.4) with the key k, the uncompressed public key encoded in
// base64url (RFC 8292 section 3.2), and returns its claims.
func verifyVAPID(t *testing.T, token, k string) *vapidClaims {
	t.Helper()
	pub, err := ecdsa.ParseUncompressedPublicKey(elliptic.P256(), b64(t, k))
	if err != nil {
		t.Fatalf("invalid VAPID public key: %v", err)
	}
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		t.Fatalf("JWT has %d parts, want 3", len(parts))
	}
	var header struct {
		Typ string `json:"typ"`
		Alg string `json:"alg"`
	}
	if err := json.Unmarshal(b64(t, parts[0]), &header); err != nil {
		t.Fatalf("invalid JWT header: %v", err)
	}
	if header.Alg != "ES256" || header.Typ != "JWT" {
		t.Errorf("JWT header = %+v, want ES256 JWT", header)
	}
	sig := b64(t, parts[2])
	if len(sig) != 64 {
		t.Fatalf("ES256 signature is %d octets, want 64", len(sig))
	}
	hash := sha256.Sum256([]byte(parts[0] + "." + parts[1]))
	r, s := new(big.Int).SetBytes(sig[:32]), new(big.Int).SetBytes(sig[32:])
	if !ecdsa.Verify(pub, hash[:], r, s) {
		t.Errorf("invalid JWT signature")
	}
	var claims vapidClaims
	if err := json.Unmarshal(b64(t, parts[1]), &claims); err != nil {
		t.Fatalf("invalid JWT claims: %v", err)
	}
	return &claims
}

// parseVAPIDAuthorization parses an Authorization header field with the
// "vapid" scheme (RFC 8292 section 3).
func parseVAPIDAuthorization(t *testing.T, v string) (token, k string) {
	t.Helper()
	scheme, params, ok := strings.Cut(v, " ")
	if !ok || !strings.EqualFold(scheme, "vapid") {
		t.Fatalf("Authorization = %q, want the vapid scheme", v)
	}
	for _, param := range strings.Split(params, ",") {
		name, value, _ := strings.Cut(strings.TrimSpace(param), "=")
		switch name {
		case "t":
			token = value
		case "k":
			k = value
		}
	}
	if token == "" || k == "" {
		t.Fatalf("Authorization = %q, want t and k parameters", v)
	}
	return token, k
}

// The JWT verification used by these tests accepts the example of RFC 8292.
func TestVerifyVAPIDRFC8292(t *testing.T) {
	claims := verifyVAPID(t, rfc8292Token, rfc8292Key)
	want := vapidClaims{Aud: "https://push.example.net", Exp: 1453523768, Sub: "mailto:push@example.com"}
	if *claims != want {
		t.Errorf("claims = %+v, want %+v", *claims, want)
	}
}

type pushServiceRequest struct {
	method string
	path   string
	header http.Header
	body   []byte
}

// newTestPushService starts a Web Push push service replying with code, and
// returns the requests it receives.
func newTestPushService(t *testing.T, code int) (*httptest.Server, func() []pushServiceRequest) {
	t.Helper()
	var mu sync.Mutex
	var reqs []pushServiceRequest
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		mu.Lock()
		reqs = append(reqs, pushServiceRequest{r.Method, r.URL.Path, r.Header.Clone(), body})
		mu.Unlock()
		if code == http.StatusCreated {
			w.Header().Set("Location", "/message/qDIYHNcfAIPP_5ITvURr-d6BGt")
		}
		w.WriteHeader(code)
	}))
	t.Cleanup(srv.Close)
	return srv, func() []pushServiceRequest {
		mu.Lock()
		defer mu.Unlock()
		return append([]pushServiceRequest(nil), reqs...)
	}
}

func TestSendWebPush(t *testing.T) {
	srv, requests := newTestPushService(t, http.StatusCreated)
	sub, uaPriv := newTestWebPushSubscription(t, srv.URL+"/push/JzLQ3raZJfFBR0aqvOMsLrt54w4rJUsV")
	vapidKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	msg := &PushMessage{
		Topic:         "O7M1nQ7cKkKTKsoS_j6Z3w",
		ContentUpdate: true,
		SyncToken:     "http://example.com/sync/10",
	}

	for _, tc := range []struct {
		name    string
		opts    *WebPushOptions
		ttl     string
		urgency string
		topic   string
		vapid   bool
	}{
		{name: "defaults", ttl: "86400"},
		{
			name: "options",
			opts: &WebPushOptions{
				VAPIDKey:     vapidKey,
				VAPIDSubject: "mailto:push@example.com",
				TTL:          90 * time.Second,
				Urgency:      "high",
				Topic:        "cal-work",
			},
			ttl:     "90",
			urgency: "high",
			topic:   "cal-work",
			vapid:   true,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			before := len(requests())
			start := time.Now()
			if err := SendWebPush(context.Background(), srv.Client(), sub, msg, tc.opts); err != nil {
				t.Fatalf("SendWebPush() = %v", err)
			}
			reqs := requests()
			if len(reqs) != before+1 {
				t.Fatalf("push service received %d requests, want 1", len(reqs)-before)
			}
			req := reqs[len(reqs)-1]

			if req.method != http.MethodPost || req.path != "/push/JzLQ3raZJfFBR0aqvOMsLrt54w4rJUsV" {
				t.Errorf("request = %v %v, want a POST to the push resource", req.method, req.path)
			}
			for name, want := range map[string]string{
				"TTL":              tc.ttl,
				"Urgency":          tc.urgency,
				"Topic":            tc.topic,
				"Content-Encoding": "aes128gcm",
				"Content-Type":     `application/xml; charset="UTF-8"`,
			} {
				if got := req.header.Get(name); got != want {
					t.Errorf("%v = %q, want %q", name, got, want)
				}
			}

			plaintext, err := decryptWebPush(uaPriv, sub.AuthSecret, req.body)
			if err != nil {
				t.Fatalf("failed to decrypt the push message: %v", err)
			}
			if !bytes.HasPrefix(plaintext, []byte("<?xml")) {
				t.Errorf("push message isn't an XML document:\n%s", plaintext)
			}
			got := decodeTestPushMessage(t, string(plaintext))
			if len(got.Topics) != 1 || got.Topics[0] != msg.Topic {
				t.Errorf("topics = %q, want %q", got.Topics, msg.Topic)
			}
			if len(got.ContentUpdates) != 1 || len(got.ContentUpdates[0].SyncTokens) != 1 || got.ContentUpdates[0].SyncTokens[0] != msg.SyncToken {
				t.Errorf("content updates = %+v, want one with sync token %q", got.ContentUpdates, msg.SyncToken)
			}
			if len(got.PropUpdates) != 0 {
				t.Errorf("unexpected property update")
			}

			auth := req.header.Get("Authorization")
			if !tc.vapid {
				if auth != "" {
					t.Errorf("Authorization = %q, want none without a VAPID key", auth)
				}
				return
			}
			token, k := parseVAPIDAuthorization(t, auth)
			wantK, _ := vapidKey.PublicKey.Bytes()
			if k != base64.RawURLEncoding.EncodeToString(wantK) {
				t.Errorf("k = %q, want the VAPID public key", k)
			}
			claims := verifyVAPID(t, token, k)
			u, _ := url.Parse(srv.URL)
			if want := "https://" + u.Host; claims.Aud != want {
				t.Errorf("aud = %q, want the origin of the push resource %q", claims.Aud, want)
			}
			exp := time.Unix(claims.Exp, 0)
			if !exp.After(start) || exp.After(start.Add(24*time.Hour)) {
				t.Errorf("exp = %v, want within 24 hours from %v (RFC 8292 section 2)", exp, start)
			}
			if claims.Sub != tc.opts.VAPIDSubject {
				t.Errorf("sub = %q, want %q", claims.Sub, tc.opts.VAPIDSubject)
			}
		})
	}
}

// Push services report invalid subscriptions with 404 Not Found or 410 Gone:
// the backend gets the status to remove them.
func TestSendWebPushError(t *testing.T) {
	for _, code := range []int{http.StatusNotFound, http.StatusGone, http.StatusTooManyRequests} {
		srv, _ := newTestPushService(t, code)
		sub, _ := newTestWebPushSubscription(t, srv.URL+"/push/gone")
		err := SendWebPush(context.Background(), srv.Client(), sub, &PushMessage{Topic: "t", ContentUpdate: true}, nil)
		var httpErr *HTTPError
		if !errors.As(err, &httpErr) || httpErr.Code != code {
			t.Errorf("SendWebPush() = %v, want an HTTPError with code %v", err, code)
		}
	}
}

func TestSendWebPushInvalid(t *testing.T) {
	srv, requests := newTestPushService(t, http.StatusCreated)
	sub, _ := newTestWebPushSubscription(t, srv.URL+"/push/sub")
	p384Key, err := ecdsa.GenerateKey(elliptic.P384(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	vapidKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	msg := &PushMessage{Topic: "t", ContentUpdate: true}
	withPushResource := func(s string) WebPushSubscription {
		return WebPushSubscription{PushResource: s, PublicKey: sub.PublicKey, AuthSecret: sub.AuthSecret}
	}

	for _, tc := range []struct {
		name string
		sub  WebPushSubscription
		opts *WebPushOptions
	}{
		{"push resource over HTTP", withPushResource("http://push.example.net/sub"), nil},
		{"relative push resource", withPushResource("/sub"), nil},
		{"push resource with userinfo", withPushResource(strings.Replace(srv.URL, "https://", "https://user:secret@", 1) + "/push/sub"), nil},
		{"push resource with fragment", withPushResource(srv.URL + "/push/sub#frag"), nil},
		{"VAPID subject without scheme", *sub, &WebPushOptions{VAPIDKey: vapidKey, VAPIDSubject: "push@example.com"}},
		{"VAPID subject over HTTP", *sub, &WebPushOptions{VAPIDKey: vapidKey, VAPIDSubject: "http://example.com/contact"}},
		{"empty mailto VAPID subject", *sub, &WebPushOptions{VAPIDKey: vapidKey, VAPIDSubject: "mailto:"}},
		{"invalid urgency", *sub, &WebPushOptions{Urgency: "urgent"}},
		{"topic too long", *sub, &WebPushOptions{Topic: strings.Repeat("a", 33)}},
		{"topic outside base64url alphabet", *sub, &WebPushOptions{Topic: "a b"}},
		{"negative TTL", *sub, &WebPushOptions{TTL: -time.Second}},
		{"VAPID key not on P-256", *sub, &WebPushOptions{VAPIDKey: p384Key}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if err := SendWebPush(context.Background(), srv.Client(), &tc.sub, msg, tc.opts); err == nil {
				t.Errorf("SendWebPush() succeeded, want an error")
			}
		})
	}
	if n := len(requests()); n != 0 {
		t.Errorf("push service received %d requests, want none", n)
	}
}

// The aud claim of VAPID tokens is the serialization of the origin of the
// push resource (RFC 8292 section 2, RFC 6454 section 6.1): a lowercase
// host, and the port in base ten unless it's the default one.
func TestVAPIDAudience(t *testing.T) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		pushResource string
		want         string
	}{
		{"https://push.example.net/sub", "https://push.example.net"},
		{"https://Push.Example.NET/sub", "https://push.example.net"},
		{"https://push.example.net:443/sub", "https://push.example.net"},
		{"https://push.example.net:0443/sub", "https://push.example.net"},
		{"https://push.example.net:/sub", "https://push.example.net"},
		{"https://push.example.net:8443/sub", "https://push.example.net:8443"},
		{"https://push.example.net:08443/sub", "https://push.example.net:8443"},
		{"https://user@push.example.net/sub", "https://push.example.net"},
		{"https://192.0.2.1/sub", "https://192.0.2.1"},
		{"https://[2001:DB8::1]:443/sub", "https://[2001:db8::1]"},
		{"https://[2001:db8::1]:8443/sub", "https://[2001:db8::1]:8443"},
	} {
		u, err := url.Parse(tc.pushResource)
		if err != nil {
			t.Fatal(err)
		}
		auth, err := vapidAuthorization(key, "", u, time.Now().Add(time.Hour))
		if err != nil {
			t.Errorf("%v: vapidAuthorization() = %v", tc.pushResource, err)
			continue
		}
		token, k := parseVAPIDAuthorization(t, auth)
		if claims := verifyVAPID(t, token, k); claims.Aud != tc.want {
			t.Errorf("%v: aud = %q, want %q", tc.pushResource, claims.Aud, tc.want)
		}
	}

	u, _ := url.Parse("https://push.example.net:65536/sub")
	if auth, err := vapidAuthorization(key, "", u, time.Now().Add(time.Hour)); err == nil {
		t.Errorf("port 65536: vapidAuthorization() = %q, want an error", auth)
	}
}

// The sub claim of VAPID tokens is a mailto: or an https: URI (RFC 8292
// section 2.1).
func TestVAPIDSubject(t *testing.T) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	u, _ := url.Parse("https://push.example.net/sub")
	for _, subject := range []string{"", "mailto:push@example.com", "MAILTO:push@example.com", "https://example.com/contact"} {
		auth, err := vapidAuthorization(key, subject, u, time.Now().Add(time.Hour))
		if err != nil {
			t.Errorf("%q: vapidAuthorization() = %v", subject, err)
			continue
		}
		token, k := parseVAPIDAuthorization(t, auth)
		if claims := verifyVAPID(t, token, k); claims.Sub != subject {
			t.Errorf("sub = %q, want %q", claims.Sub, subject)
		}
	}
	for _, subject := range []string{"push@example.com", "http://example.com/contact", "mailto:", "https:///contact", "urn:example:contact", "%"} {
		if _, err := vapidAuthorization(key, subject, u, time.Now().Add(time.Hour)); err == nil {
			t.Errorf("%q: vapidAuthorization() succeeded, want an error", subject)
		}
	}
}

// newTestRedirectServer starts a server replying to every request with a
// redirect to location, with code.
func newTestRedirectServer(t *testing.T, code int, location string) *httptest.Server {
	t.Helper()
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.Copy(io.Discard, r.Body)
		w.Header().Set("Location", location)
		w.WriteHeader(code)
	}))
	t.Cleanup(srv.Close)
	return srv
}

// Without an HTTP client, SendWebPush doesn't follow redirects: the message
// mustn't go to another origin, or over HTTP (draft-bitfire-webdav-push
// section 7.3). RFC 8030 doesn't use redirects for delivery requests (section
// 5).
func TestSendWebPushRedirect(t *testing.T) {
	target, targetRequests := newTestPushService(t, http.StatusCreated)
	var mu sync.Mutex
	plainRequests := 0
	plain := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		plainRequests++
		mu.Unlock()
		w.WriteHeader(http.StatusCreated)
	}))
	t.Cleanup(plain.Close)

	// SendWebPush uses http.DefaultClient: make it trust the certificate of
	// the test servers
	defaultClient := http.DefaultClient
	http.DefaultClient = target.Client()
	t.Cleanup(func() { http.DefaultClient = defaultClient })

	msg := &PushMessage{Topic: "t", ContentUpdate: true}
	for _, code := range []int{
		http.StatusMovedPermanently,
		http.StatusFound,
		http.StatusSeeOther,
		http.StatusTemporaryRedirect,
		http.StatusPermanentRedirect,
	} {
		for _, tc := range []struct {
			name     string
			location string
		}{
			{"other origin", target.URL + "/push/sub"},
			{"HTTP", plain.URL + "/push/sub"},
			{"same origin", "/push/other"},
		} {
			srv := newTestRedirectServer(t, code, tc.location)
			sub, _ := newTestWebPushSubscription(t, srv.URL+"/push/sub")
			err := SendWebPush(context.Background(), nil, sub, msg, nil)
			var httpErr *HTTPError
			if !errors.As(err, &httpErr) || httpErr.Code != code {
				t.Errorf("%v to %v: SendWebPush() = %v, want an HTTPError with code %v", code, tc.name, err, code)
			}
		}
	}

	if n := len(targetRequests()); n != 0 {
		t.Errorf("other origin received %d requests, want none", n)
	}
	mu.Lock()
	defer mu.Unlock()
	if plainRequests != 0 {
		t.Errorf("HTTP server received %d requests, want none", plainRequests)
	}
}
