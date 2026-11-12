package caldav

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/emersion/go-webdav"
)

// wrappedNotFoundBackend has no calendar objects, and wraps the 404 error it
// returns for them.
type wrappedNotFoundBackend struct {
	testBackend
}

func (wrappedNotFoundBackend) GetCalendarObject(ctx context.Context, path string, req *CalendarCompRequest) (*CalendarObject, error) {
	return nil, fmt.Errorf("store: %w", webdav.NewHTTPError(http.StatusNotFound, fmt.Errorf("no such object")))
}

// OPTIONS for a calendar object that doesn't exist yet allows PUT only, also
// when the backend wraps its 404 error.
func TestOptionsWrappedNotFound(t *testing.T) {
	h := &Handler{Backend: wrappedNotFoundBackend{testBackend{calendars: []Calendar{{Path: "/user/calendars/work/"}}}}}
	req := httptest.NewRequest(http.MethodOptions, "/user/calendars/work/new.ics", nil)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	if w.Code != http.StatusNoContent {
		t.Fatalf("status = %d, want 204; body:\n%s", w.Code, w.Body.String())
	}
	if allow, want := w.Header().Get("Allow"), "OPTIONS, PUT"; allow != want {
		t.Errorf("Allow = %q, want %q", allow, want)
	}
}
