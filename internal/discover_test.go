package internal

import (
	"context"
	"errors"
	"net"
	"slices"
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
		// A record with several strings, which LookupTXT joins without a
		// separator: TXT "txtvers=1" "path=/caldav/" (RFC 6763 section 6.1)
		{"TXT strings joined, txtvers first", fakeResolver{srv: srv, txt: []string{"txtvers=1path=/caldav/"}}, "https://dav.example.com/caldav/", nil},
		{"TXT strings joined, path first", fakeResolver{srv: srv, txt: []string{"path=/caldav/txtvers=1"}}, "https://dav.example.com/caldav/", nil},
		{"TXT strings joined, upper case keys", fakeResolver{srv: srv, txt: []string{"TXTVERS=1PATH=/caldav/"}}, "https://dav.example.com/caldav/", nil},
		{"TXT strings joined, with another key", fakeResolver{srv: srv, txt: []string{"txtvers=1foo=barpath=/caldav/"}}, "https://dav.example.com/caldav/", nil},
		{"TXT strings joined, empty txtvers", fakeResolver{srv: srv, txt: []string{"txtvers=path=/caldav/"}}, "https://dav.example.com/caldav/", nil},
		{"TXT strings joined, and another record", fakeResolver{srv: srv, txt: []string{"foo=bar", "txtvers=1path=/caldav/"}}, "https://dav.example.com/caldav/", nil},
		// Keys can't hold "=", so this is the key "xpath", not "x" and "path"
		{"TXT key ending in path", fakeResolver{srv: srv, txt: []string{"xpath=/other/"}}, "https://dav.example.com/.well-known/caldav", nil},
		{"TXT path with an equals sign", fakeResolver{srv: srv, txt: []string{"path=/dav/a=b/"}}, "https://dav.example.com/dav/a=b/", nil},
		// The path is used as is in HTTP requests (RFC 6764 section 4), so
		// it's percent-encoded: "%7E" is "~", not "%" followed by "7E"
		{"TXT path percent-encoded", fakeResolver{srv: srv, txt: []string{"path=/dav/%7Euser/"}}, "https://dav.example.com/dav/%7Euser/", nil},
		{"TXT path with an encoded space", fakeResolver{srv: srv, txt: []string{"path=/dav/my%20calendars/"}}, "https://dav.example.com/dav/my%20calendars/", nil},
		{"TXT path with an encoded slash", fakeResolver{srv: srv, txt: []string{"path=/dav/a%2Fb/"}}, "https://dav.example.com/dav/a%2Fb/", nil},
		{"TXT relative path percent-encoded", fakeResolver{srv: srv, txt: []string{"path=%7Euser/"}}, "https://dav.example.com/%7Euser/", nil},
		// Characters which can't be in a request path are still escaped
		{"TXT path with a space", fakeResolver{srv: srv, txt: []string{"path=/dav/my calendars/"}}, "https://dav.example.com/dav/my%20calendars/", nil},
		{"TXT path with non-ASCII characters", fakeResolver{srv: srv, txt: []string{"path=/dav/café/"}}, "https://dav.example.com/dav/caf%C3%A9/", nil},
		{"TXT path with a space and an encoded character", fakeResolver{srv: srv, txt: []string{"path=/dav/my %7Euser/"}}, "https://dav.example.com/dav/my%20~user/", nil},
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
		{"empty path, strings joined", []string{"txtvers=1path="}},
		{"multiple path entries, strings joined", []string{"path=/a/path=/b/"}},
		// Not a valid percent-encoded path (RFC 3986 section 2.1)
		{"bare percent sign", []string{"path=/dav/100%/"}},
		{"invalid percent-encoding", []string{"path=/dav/%zzuser/"}},
		{"truncated percent-encoding", []string{"path=/dav/%7"}},
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

// TestSplitTXT checks how the strings of a TXT record joined by LookupTXT are
// split, including the cases it can't tell apart.
func TestSplitTXT(t *testing.T) {
	for _, tc := range []struct {
		record string
		want   []string
	}{
		{"", []string{""}},
		{"path=/dav/", []string{"path=/dav/"}},
		{"txtvers=1path=/dav/", []string{"txtvers=1", "path=/dav/"}},
		{"path=/dav/txtvers=1", []string{"path=/dav/", "txtvers=1"}},
		{"Path=/a/TxtVers=1", []string{"Path=/a/", "TxtVers=1"}},
		{"a=bpath=/dav/", []string{"a=b", "path=/dav/"}},
		{"xpath=/a/", []string{"xpath=/a/"}},
		// Can't be told apart from "path=/dav/" "c=d"
		{"a=bpath=/dav/c=d", []string{"a=b", "path=/dav/c=d"}},
		// Can't be told apart from "flag" "path=/a/"
		{"flagpath=/a/", []string{"flagpath=/a/"}},
		{"path=/a/path=/b/", []string{"path=/a/", "path=/b/"}},
	} {
		if got := splitTXT(tc.record); !slices.Equal(got, tc.want) {
			t.Errorf("splitTXT(%q) = %q, want %q", tc.record, got, tc.want)
		}
	}
}
