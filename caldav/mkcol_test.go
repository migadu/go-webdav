package caldav

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// createBackend records the calendars it is asked to create.
type createBackend struct {
	testBackend
	created []Calendar
}

func (b *createBackend) CreateCalendar(ctx context.Context, cal *Calendar) error {
	b.created = append(b.created, *cal)
	return nil
}

// A MKCOL which doesn't set supported-calendar-component-set leaves it to the
// server, which defaults to VEVENT.
func TestMkcolWithoutComponentSet(t *testing.T) {
	backend := &createBackend{}
	handler := Handler{Backend: backend}

	req := httptest.NewRequest("MKCOL", "/user/calendars/work/", strings.NewReader(`<?xml version="1.0" encoding="utf-8"?>
<D:mkcol xmlns:D="DAV:" xmlns:C="urn:ietf:params:xml:ns:caldav">
  <D:set><D:prop>
    <D:resourcetype><D:collection/><C:calendar/></D:resourcetype>
    <D:displayname>Work</D:displayname>
  </D:prop></D:set>
</D:mkcol>`))
	req.Header.Set("Content-Type", "application/xml")
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	if w.Code != http.StatusCreated {
		t.Fatalf("MKCOL returned %v: %s", w.Code, w.Body.String())
	}
	if len(backend.created) != 1 {
		t.Fatalf("CreateCalendar called %d times, want 1", len(backend.created))
	}
	if cal := backend.created[0]; cal.Name != "Work" || cal.SupportedComponentSet != nil {
		t.Errorf("created calendar %+v, want name Work and no component set", cal)
	}
}
