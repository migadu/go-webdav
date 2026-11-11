package caldav

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestPropFindCalendarObjectPrivileges(t *testing.T) {
	const calPath = "/user/calendars/a/"
	for _, tc := range []struct {
		readOnly bool
		want     string
	}{
		{false, `<current-user-privilege-set xmlns="DAV:"><privilege xmlns="DAV:"><read xmlns="DAV:"></read><write xmlns="DAV:"></write></privilege></current-user-privilege-set>`},
		{true, `<current-user-privilege-set xmlns="DAV:"><privilege xmlns="DAV:"><read xmlns="DAV:"></read></privilege></current-user-privilege-set>`},
	} {
		co := newDataRequestTestObject(calPath + "1.ics")
		co.ReadOnly = tc.readOnly
		handler := Handler{Backend: testBackend{
			calendars: []Calendar{{Path: calPath}},
			objectMap: map[string][]CalendarObject{calPath: {co}},
		}}
		req := httptest.NewRequest("PROPFIND", co.Path, strings.NewReader(`<?xml version="1.0" encoding="utf-8"?>
<D:propfind xmlns:D="DAV:"><D:prop><D:current-user-privilege-set/></D:prop></D:propfind>`))
		req.Header.Set("Content-Type", "application/xml")
		req.Header.Set("Depth", "0")
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, req)

		if w.Code != http.StatusMultiStatus {
			t.Fatalf("PROPFIND returned %v", w.Code)
		}
		b, _ := io.ReadAll(w.Body)
		if !strings.Contains(string(b), tc.want) {
			t.Errorf("readOnly=%v: want %s in:\n%s", tc.readOnly, tc.want, b)
		}
	}
}
