package internal

import (
	"context"
	"encoding/xml"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"
)

// PropFindFunc sends a PROPFIND request, and calls the function with each
// response, its hrefs normalized, in order.
func TestClientPropFindFunc(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var propfind PropFind
		if r.Method != "PROPFIND" {
			t.Errorf("method = %v, want PROPFIND", r.Method)
		} else if depth := r.Header.Get("Depth"); depth != "1" {
			t.Errorf("Depth = %q, want 1", depth)
		} else if err := xml.NewDecoder(r.Body).Decode(&propfind); err != nil {
			t.Errorf("failed to decode request body: %v", err)
		} else if propfind.Prop == nil || len(propfind.Prop.Raw) != 1 {
			t.Errorf("request asks for %+v, want DAV:getetag", propfind)
		}
		w.WriteHeader(http.StatusMultiStatus)
		io.WriteString(w, xml.Header+`<multistatus xmlns="DAV:">`+
			`<response><href>/dir/a</href><status>HTTP/1.1 200 OK</status></response>`+
			`<response><href>http://`+r.Host+`/dir/b</href><status>HTTP/1.1 404 Not Found</status></response>`+
			`<responsedescription>done</responsedescription></multistatus>`)
	}))
	defer srv.Close()

	c, err := NewClient(srv.Client(), srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	var paths []string
	var codes []int
	ms, err := c.PropFindFunc(context.Background(), "/dir/", DepthOne, NewPropNamePropFind(GetETagName), func(resp *Response) error {
		paths = append(paths, resp.Hrefs[0].Path)
		code := http.StatusOK
		if err := resp.Err(); err != nil {
			code = HTTPErrorFromError(err).Code
		}
		codes = append(codes, code)
		return nil
	})
	if err != nil {
		t.Fatalf("PropFindFunc() = %v", err)
	}
	if want := []string{"/dir/a", "/dir/b"}; !reflect.DeepEqual(paths, want) {
		t.Errorf("got responses for %v, want %v", paths, want)
	}
	if want := []int{http.StatusOK, http.StatusNotFound}; !reflect.DeepEqual(codes, want) {
		t.Errorf("got statuses %v, want %v", codes, want)
	}
	if ms.ResponseDescription != "done" || ms.Responses != nil {
		t.Errorf("PropFindFunc() = %+v, want only the response description", ms)
	}

	errStop := errors.New("stop")
	calls := 0
	_, err = c.PropFindFunc(context.Background(), "/dir/", DepthOne, NewPropNamePropFind(GetETagName), func(resp *Response) error {
		calls++
		return errStop
	})
	if !errors.Is(err, errStop) || calls != 1 {
		t.Errorf("PropFindFunc() = %v after %d calls, want %v after one", err, calls, errStop)
	}
}
