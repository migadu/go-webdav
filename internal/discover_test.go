package internal

import (
	"context"
	"errors"
	"net"
	"testing"
)

type fakeResolver struct {
	srv    []*net.SRV
	err    error
	txt    []string
	txtErr error
}

func (r *fakeResolver) LookupSRV(ctx context.Context, service, proto, name string) (string, []*net.SRV, error) {
	return "", r.srv, r.err
}

func (r *fakeResolver) LookupTXT(ctx context.Context, name string) ([]string, error) {
	return r.txt, r.txtErr
}

func TestDiscoverContextURL(t *testing.T) {
	const name = "_caldavs._tcp.example.com"
	srv := []*net.SRV{{Target: "dav.example.com.", Port: 443}}
	notFound := &net.DNSError{Err: "no such host", Name: name, IsNotFound: true}
	refused := &net.DNSError{Err: "server misbehaving", Name: name, Server: "192.0.2.1:53"}
	temporary := &net.DNSError{Err: "server misbehaving", Name: name, Server: "192.0.2.1:53", IsTemporary: true}
	malformed := &net.DNSError{Err: "DNS response contained records which contain invalid names", Name: name}
	for _, tc := range []struct {
		name    string
		r       fakeResolver
		want    string
		wantErr error
	}{
		{"no record", fakeResolver{err: notFound}, "", ErrNoServiceRecord},
		{"empty answer", fakeResolver{}, "", ErrNoServiceRecord},
		// RFC 2782: a target of "." means the service is not available
		{"not available", fakeResolver{srv: []*net.SRV{{Target: ".", Port: 0}}}, "", ErrNoServiceRecord},
		// Any other DNS failure doesn't say that there is no record
		{"refused", fakeResolver{err: refused}, "", refused},
		{"temporary failure", fakeResolver{err: temporary}, "", temporary},
		{"malformed answer", fakeResolver{srv: srv, err: malformed}, "", malformed},
		{"default port", fakeResolver{srv: srv}, "https://dav.example.com/.well-known/caldav", nil},
		{"port and path", fakeResolver{srv: []*net.SRV{{Target: "dav.example.com.", Port: 8443}}, txt: []string{"path=/dav/"}}, "https://dav.example.com:8443/dav/", nil},
		{"TXT with txtvers and path", fakeResolver{srv: srv, txt: []string{"txtvers=1", "path=/caldav/"}}, "https://dav.example.com/caldav/", nil},
		{"TXT with case-insensitive PATH key", fakeResolver{srv: srv, txt: []string{"txtvers=1", "PATH=/caldav/"}}, "https://dav.example.com/caldav/", nil},
		{"TXT with relative path", fakeResolver{srv: srv, txt: []string{"path=caldav/"}}, "https://dav.example.com/caldav/", nil},
		{"TXT without path key falls back to well-known", fakeResolver{srv: srv, txt: []string{"txtvers=1"}}, "https://dav.example.com/.well-known/caldav", nil},
		{"no TXT record", fakeResolver{srv: srv, txtErr: notFound}, "https://dav.example.com/.well-known/caldav", nil},
		{"TXT refused", fakeResolver{srv: srv, txtErr: refused}, "", refused},
		{"TXT temporary failure", fakeResolver{srv: srv, txtErr: temporary}, "", temporary},
	} {
		t.Run(tc.name, func(t *testing.T) {
			defer func(r dnsResolver) { resolver = r }(resolver)
			resolver = &tc.r

			got, err := DiscoverContextURL(context.Background(), "caldav", "example.com")
			if tc.wantErr != nil {
				if !errors.Is(err, tc.wantErr) {
					t.Fatalf("DiscoverContextURL() = %q, %v, want %v", got, err, tc.wantErr)
				}
				return
			}
			if err != nil || got != tc.want {
				t.Fatalf("DiscoverContextURL() = %q, %v, want %q", got, err, tc.want)
			}
		})
	}
}

func TestDiscoverContextURLInvalidTXT(t *testing.T) {
	srv := []*net.SRV{{Target: "dav.example.com.", Port: 443}}
	for _, tc := range []struct {
		name string
		txt  []string
	}{
		{"empty path", []string{"path="}},
		{"empty path with txtvers", []string{"txtvers=1", "path="}},
		{"multiple path entries", []string{"path=/a/", "path=/b/"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			defer func(r dnsResolver) { resolver = r }(resolver)
			resolver = &fakeResolver{srv: srv, txt: tc.txt}

			if got, err := DiscoverContextURL(context.Background(), "caldav", "example.com"); err == nil {
				t.Fatalf("DiscoverContextURL() = %q, nil, want error", got)
			}
		})
	}
}
