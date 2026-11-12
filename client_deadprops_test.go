package webdav_test

import (
	"context"
	"encoding/xml"
	"errors"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"

	"github.com/migadu/go-webdav"
)

func TestClientDeadProperties(t *testing.T) {
	fs := newDeadPropFileSystem()
	ts := httptest.NewServer(&webdav.Handler{FileSystem: fs})
	defer ts.Close()
	c, err := webdav.NewClient(nil, ts.URL)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()

	var authors webdav.DeadProperty
	if err := xml.Unmarshal([]byte(authorsProp), &authors); err != nil {
		t.Fatal(err)
	}
	color := webdav.DeadProperty{XMLName: colorName, InnerXML: []byte("red")}

	// The instructions are sent in order
	err = c.PatchDeadProperties(ctx, "/dir/a.txt", []webdav.DeadPropertyPatch{
		{Property: color},
		{Remove: true, Property: webdav.DeadProperty{XMLName: colorName}},
		{Property: authors},
	})
	if err != nil {
		t.Fatalf("PatchDeadProperties() = %v", err)
	}
	if got, want := fs.patchCalls(), [][]string{{"set color", "remove color", "set Authors"}}; !reflect.DeepEqual(got, want) {
		t.Errorf("PatchDeadProperties calls = %q, want %q", got, want)
	}

	props, err := c.DeadProperties(ctx, "/dir/a.txt", authorsName, colorName)
	if err != nil {
		t.Fatalf("DeadProperties() = %v", err)
	}
	if len(props) != 1 || props[0].XMLName != authorsName {
		t.Fatalf("DeadProperties() = %v, want only %v", props, authorsName)
	}
	b, err := xml.Marshal(props[0])
	if err != nil {
		t.Fatal(err)
	}
	if got, want := canonicalXML(t, string(b)), canonicalXML(t, authorsProp); got != want {
		t.Errorf("DeadProperties() = %v, want %v", got, want)
	}

	// A protected property fails the whole request, and the error reports
	// its status before the 424 Failed Dependency of the others
	err = c.PatchDeadProperties(ctx, "/dir/a.txt", []webdav.DeadPropertyPatch{
		{Property: color},
		{Property: webdav.DeadProperty{XMLName: getETagName, InnerXML: []byte(`"forged"`)}},
	})
	var httpErr *webdav.HTTPError
	if !errors.As(err, &httpErr) || httpErr.Code != http.StatusForbidden {
		t.Errorf("PatchDeadProperties() = %v, want a 403 HTTPError", err)
	}
	if err == nil || !strings.Contains(err.Error(), "getetag") || !strings.Contains(err.Error(), "color") {
		t.Errorf("PatchDeadProperties() = %v, want an error naming both properties", err)
	}
	if got := len(fs.patches); got != 1 {
		t.Errorf("PatchDeadProperties called %v times after the failed request", got-1)
	}

	if _, err := c.DeadProperties(ctx, "/dir/a.txt"); err == nil {
		t.Errorf("DeadProperties() without names succeeded")
	}
}
