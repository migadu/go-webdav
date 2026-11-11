package internal

import (
	"context"
	"errors"
	"net"
	"testing"
)

type fakeResolver struct {
	srv []*net.SRV
	err error
	txt []string
}

func (r *fakeResolver) LookupSRV(ctx context.Context, service, proto, name string) (string, []*net.SRV, error) {
	return "", r.srv, r.err
}

func (r *fakeResolver) LookupTXT(ctx context.Context, name string) ([]string, error) {
	return r.txt, nil
}

func TestDiscoverContextURL(t *testing.T) {
	notFound := &net.DNSError{Err: "no such host", Name: "_caldavs._tcp.example.com", IsNotFound: true}
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
		{"default port", fakeResolver{srv: []*net.SRV{{Target: "dav.example.com.", Port: 443}}}, "https://dav.example.com/.well-known/caldav", nil},
		{"port and path", fakeResolver{srv: []*net.SRV{{Target: "dav.example.com.", Port: 8443}}, txt: []string{"path=/dav/"}}, "https://dav.example.com:8443/dav/", nil},
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
