package caldav

import (
	"context"
	"encoding/xml"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"

	"github.com/emersion/go-webdav"
	"github.com/emersion/go-webdav/internal"
)

// objectBackend holds calendar objects at the paths in objects.
type objectBackend struct {
	testBackend
	objects map[string]bool
}

func (b *objectBackend) GetCalendarObject(ctx context.Context, path string, req *CalendarCompRequest) (*CalendarObject, error) {
	if !b.objects[path] {
		return nil, webdav.NewHTTPError(http.StatusNotFound, nil)
	}
	return &CalendarObject{Path: path, Data: minimalCalendar()}, nil
}

// copyMoveCall is a call to CopyCalendarObject or MoveCalendarObject.
type copyMoveCall struct {
	method      string
	src, dst    string
	noOverwrite bool
}

// copyMoveBackend records the calendar objects it is asked to copy and move.
type copyMoveBackend struct {
	objectBackend
	calls []copyMoveCall
}

func (b *copyMoveBackend) CopyCalendarObject(ctx context.Context, src, dst string, opts *CopyCalendarObjectOptions) (created bool, err error) {
	return b.copyMove("COPY", src, dst, opts.NoOverwrite)
}

func (b *copyMoveBackend) MoveCalendarObject(ctx context.Context, src, dst string, opts *MoveCalendarObjectOptions) (created bool, err error) {
	return b.copyMove("MOVE", src, dst, opts.NoOverwrite)
}

func (b *copyMoveBackend) copyMove(method, src, dst string, noOverwrite bool) (created bool, err error) {
	b.calls = append(b.calls, copyMoveCall{method, src, dst, noOverwrite})
	if !b.objects[src] {
		return false, webdav.NewHTTPError(http.StatusNotFound, nil)
	}
	if b.objects[dst] {
		if noOverwrite {
			return false, webdav.NewHTTPError(http.StatusPreconditionFailed, nil)
		}
		return false, nil
	}
	return true, nil
}

const (
	copyMoveEvent = "/user/calendars/personal/event.ics"
	copyMoveTask  = "/user/calendars/work/task.ics"
)

func newCopyMoveBackend() *copyMoveBackend {
	return &copyMoveBackend{objectBackend: objectBackend{objects: map[string]bool{
		copyMoveEvent:          true,
		copyMoveTask:           true,
		"/dav" + copyMoveEvent: true,
		"/dav" + copyMoveTask:  true,
	}}}
}

func serveCopyMove(h *Handler, method, src, dest, overwrite string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, src, nil)
	req.Header.Set("Destination", dest)
	if overwrite != "" {
		req.Header.Set("Overwrite", overwrite)
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	return w
}

// hasCalDAVPrecondition reports whether the body of w is a DAV:error element
// with the CalDAV precondition.
func hasCalDAVPrecondition(t *testing.T, w *httptest.ResponseRecorder, precondition PreconditionType) bool {
	t.Helper()
	var davErr internal.Error
	if err := xml.NewDecoder(strings.NewReader(w.Body.String())).Decode(&davErr); err != nil {
		return false
	}
	want := xml.Name{Space: "urn:ietf:params:xml:ns:caldav", Local: string(precondition)}
	for _, raw := range davErr.Raw {
		if name, ok := raw.XMLName(); ok && name == want {
			return true
		}
	}
	return false
}

// RFC 4791 section 5.3.2.1: calendar objects can be copied or moved into a
// calendar, for instance to move an event from a calendar to another.
func TestCopyMoveCalendarObject(t *testing.T) {
	for _, tc := range []struct {
		name              string
		prefix            string
		method, src, dest string
		overwrite         string
		wantCode          int
		wantCall          *copyMoveCall
		wantPrecondition  PreconditionType
	}{
		{
			name:     "move to another calendar",
			method:   "MOVE",
			src:      copyMoveEvent,
			dest:     "http://example.com/user/calendars/work/event.ics",
			wantCode: http.StatusCreated,
			wantCall: &copyMoveCall{"MOVE", copyMoveEvent, "/user/calendars/work/event.ics", false},
		},
		{
			name:     "copy to another calendar",
			method:   "COPY",
			src:      copyMoveEvent,
			dest:     "/user/calendars/work/event.ics",
			wantCode: http.StatusCreated,
			wantCall: &copyMoveCall{"COPY", copyMoveEvent, "/user/calendars/work/event.ics", false},
		},
		{
			name:     "move over an existing object",
			method:   "MOVE",
			src:      copyMoveEvent,
			dest:     copyMoveTask,
			wantCode: http.StatusNoContent,
			wantCall: &copyMoveCall{"MOVE", copyMoveEvent, copyMoveTask, false},
		},
		{
			name:      "copy over an existing object",
			method:    "COPY",
			src:       copyMoveEvent,
			dest:      copyMoveTask,
			overwrite: "T",
			wantCode:  http.StatusNoContent,
			wantCall:  &copyMoveCall{"COPY", copyMoveEvent, copyMoveTask, false},
		},
		{
			name:      "move without overwriting an existing object",
			method:    "MOVE",
			src:       copyMoveEvent,
			dest:      copyMoveTask,
			overwrite: "F",
			wantCode:  http.StatusPreconditionFailed,
			wantCall:  &copyMoveCall{"MOVE", copyMoveEvent, copyMoveTask, true},
		},
		{
			name:      "copy without overwriting to a new object",
			method:    "COPY",
			src:       copyMoveEvent,
			dest:      "/user/calendars/work/event.ics",
			overwrite: "F",
			wantCode:  http.StatusCreated,
			wantCall:  &copyMoveCall{"COPY", copyMoveEvent, "/user/calendars/work/event.ics", true},
		},
		{
			name:     "move with a prefix",
			prefix:   "/dav/",
			method:   "MOVE",
			src:      "/dav" + copyMoveEvent,
			dest:     "/dav/user/calendars/work/event.ics",
			wantCode: http.StatusCreated,
			wantCall: &copyMoveCall{"MOVE", "/dav" + copyMoveEvent, "/dav/user/calendars/work/event.ics", false},
		},
		{
			name:             "destination is a calendar",
			method:           "MOVE",
			src:              copyMoveEvent,
			dest:             "/user/calendars/work/",
			wantCode:         http.StatusForbidden,
			wantPrecondition: PreconditionCalendarCollectionLocationOk,
		},
		{
			name:             "destination is outside of a calendar",
			method:           "MOVE",
			src:              copyMoveEvent,
			dest:             "/user/calendars/event.ics",
			wantCode:         http.StatusForbidden,
			wantPrecondition: PreconditionCalendarCollectionLocationOk,
		},
		{
			name:             "destination is in a collection in a calendar",
			method:           "COPY",
			src:              copyMoveEvent,
			dest:             "/user/calendars/work/sub/event.ics",
			wantCode:         http.StatusForbidden,
			wantPrecondition: PreconditionCalendarCollectionLocationOk,
		},
		{
			name:     "destination is outside of the prefix",
			prefix:   "/dav/",
			method:   "MOVE",
			src:      "/dav" + copyMoveEvent,
			dest:     "/user/calendars/work/event.ics",
			wantCode: http.StatusBadGateway,
		},
		{
			name:     "source is a calendar",
			method:   "MOVE",
			src:      "/user/calendars/personal/",
			dest:     "/user/calendars/other/",
			wantCode: http.StatusForbidden,
		},
		{
			name:     "source is a calendar, destination an object",
			method:   "COPY",
			src:      "/user/calendars/personal/",
			dest:     "/user/calendars/work/personal.ics",
			wantCode: http.StatusForbidden,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			backend := newCopyMoveBackend()
			h := &Handler{Backend: backend, Prefix: tc.prefix}

			w := serveCopyMove(h, tc.method, tc.src, tc.dest, tc.overwrite)
			if w.Code != tc.wantCode {
				t.Errorf("%s returned %v, want %v: %s", tc.method, w.Code, tc.wantCode, w.Body.String())
			}
			if tc.wantPrecondition != "" && !hasCalDAVPrecondition(t, w, tc.wantPrecondition) {
				t.Errorf("%s error is %q, want the %s precondition", tc.method, w.Body.String(), tc.wantPrecondition)
			}

			var wantCalls []copyMoveCall
			if tc.wantCall != nil {
				wantCalls = []copyMoveCall{*tc.wantCall}
			}
			if !reflect.DeepEqual(backend.calls, wantCalls) {
				t.Errorf("backend calls = %+v, want %+v", backend.calls, wantCalls)
			}
		})
	}
}

// Backends which don't implement CalendarObjectCopyMover keep answering COPY
// and MOVE with 501 Not Implemented.
func TestCopyMoveCalendarObjectNotImplemented(t *testing.T) {
	h := &Handler{Backend: &objectBackend{objects: map[string]bool{copyMoveEvent: true}}}
	for _, method := range []string{"COPY", "MOVE"} {
		w := serveCopyMove(h, method, copyMoveEvent, "/user/calendars/work/event.ics", "")
		if w.Code != http.StatusNotImplemented {
			t.Errorf("%s returned %v, want 501: %s", method, w.Code, w.Body.String())
		}
	}
}

// COPY and MOVE are listed in the Allow header of calendar objects only when
// the backend implements CalendarObjectCopyMover.
func TestOptionsAllowsCopyMoveCalendarObject(t *testing.T) {
	for _, tc := range []struct {
		name    string
		backend Backend
		path    string
		want    bool
	}{
		{"calendar object", newCopyMoveBackend(), copyMoveEvent, true},
		{"calendar", newCopyMoveBackend(), "/user/calendars/personal/", false},
		{"missing calendar object", newCopyMoveBackend(), "/user/calendars/personal/missing.ics", false},
		{"backend without copy and move", &objectBackend{objects: map[string]bool{copyMoveEvent: true}}, copyMoveEvent, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodOptions, tc.path, nil)
			w := httptest.NewRecorder()
			(&Handler{Backend: tc.backend}).ServeHTTP(w, req)
			if w.Code != http.StatusNoContent {
				t.Fatalf("OPTIONS %s returned %v: %s", tc.path, w.Code, w.Body.String())
			}

			allowed := make(map[string]bool)
			for _, method := range strings.Split(w.Header().Get("Allow"), ",") {
				allowed[strings.TrimSpace(method)] = true
			}
			for _, method := range []string{"COPY", "MOVE"} {
				if allowed[method] != tc.want {
					t.Errorf("Allow lists %s: %v, want %v (Allow: %v)", method, allowed[method], tc.want, allowed)
				}
			}
		})
	}
}
