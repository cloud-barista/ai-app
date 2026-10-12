package rest

import (
	"fmt"
	"net/http"
	"testing"

	"github.com/ish-hcc/ai-agent-proto/internal/deploy"
)

func TestStatusCodeForRemoteErrorIsBadGateway(t *testing.T) {
	for _, status := range []int{http.StatusBadRequest, http.StatusNotFound, http.StatusInternalServerError, http.StatusServiceUnavailable} {
		err := fmt.Errorf("failed to recommend specs: %w",
			&deploy.RemoteError{Method: http.MethodPost, URL: "http://placement/recommendations/specs", StatusCode: status})
		if got := statusCodeFor(err); got != http.StatusBadGateway {
			t.Errorf("remote %d mapped to %d, want 502", status, got)
		}
	}
}

func TestStatusCodeForAppMismatchIsConflict(t *testing.T) {
	err := fmt.Errorf("%w: vllm-lab-01 serves a, not b", deploy.ErrAppMismatch)
	if got := statusCodeFor(err); got != http.StatusConflict {
		t.Errorf("app mismatch mapped to %d, want 409", got)
	}
}
