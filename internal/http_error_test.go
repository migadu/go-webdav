package internal

import (
	"fmt"
	"net/http"
	"testing"
)

func TestHTTPErrorFromErrorUnwraps(t *testing.T) {
	inner := &HTTPError{Code: http.StatusForbidden, Err: fmt.Errorf("denied")}
	wrapped := fmt.Errorf("outer context: %w", inner)

	got := HTTPErrorFromError(wrapped)
	if got.Code != http.StatusForbidden {
		t.Errorf("HTTPErrorFromError(wrapped).Code = %d, want %d", got.Code, http.StatusForbidden)
	}
}
