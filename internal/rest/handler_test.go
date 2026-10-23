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

func TestRequirementNotDeployedIsConflictWithItsOwnMessage(t *testing.T) {
	err := &deploy.RequirementError{AppID: "open-webui-llama31", RequiredAppID: "vllm-llama31-8b", Namespace: "default"}
	if got := statusCodeFor(err); got != http.StatusConflict {
		t.Errorf("requirement not deployed mapped to %d, want 409", got)
	}
	want := "application open-webui-llama31 needs model service vllm-llama31-8b deployed in namespace default first"
	if got := messageFor(err, "Deployment planning failed"); got != want {
		t.Errorf("message = %q, want %q", got, want)
	}
}

func TestNoRecommenderIsUnprocessableWithItsOwnMessage(t *testing.T) {
	if got := statusCodeFor(deploy.ErrNoRecommender); got != http.StatusUnprocessableEntity {
		t.Errorf("no recommender mapped to %d, want 422", got)
	}
	if got := messageFor(deploy.ErrNoRecommender, "Deployment planning failed"); got != "no recommender configured: pin specId and imageId" {
		t.Errorf("message = %q", got)
	}
}
