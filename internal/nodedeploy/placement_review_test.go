package nodedeploy

import (
	"context"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ish-hcc/ai-agent-proto/internal/model"
	"github.com/ish-hcc/ai-agent-proto/internal/tumblebug"
)

type changeReviewedImage struct {
	Tumblebug
	reads atomic.Int32
}

func (c *changeReviewedImage) GetNode(ctx context.Context, nsID, infraID, nodeID string) (*tumblebug.Node, error) {
	node, err := c.Tumblebug.GetNode(ctx, nsID, infraID, nodeID)
	if err == nil && c.reads.Add(1) == 2 {
		node.ImageID = "replacement-image"
	}
	return node, err
}

func TestReviewedPlacementChecksNodeBeforeAdmissionAndExecution(t *testing.T) {
	ctx := context.Background()
	req := modelReq("vm1")
	review := model.NodePlacementReview{
		NsID: "etri", SpecID: "aws+gpu", ImageID: "ami-rhel9", Status: "Warning",
		SpecStatus: "Available", ImageStatus: "Available (Auto-registered)",
		EstimatedCost: "$0.50/hour", ReviewedAt: time.Now().UTC(),
	}
	req.PlacementReview = &review
	dry := newFixture(t, true)
	review.SpecID = "aws+other-gpu"
	if _, err := dry.svc.Create(ctx, req); err == nil || !strings.Contains(err.Error(), "reviewed CB-Tumblebug spec and image") {
		t.Fatalf("mismatched catalog spec was admitted: %v", err)
	}
	if len(dry.svc.store.List()) != 0 || dry.runner.count() != 0 {
		t.Fatal("a mismatched node produced a deployment record or ran a playbook")
	}
	review.SpecID = "aws+gpu"
	dry.tb.edit("vm1", map[string]any{"status": "Suspended"})
	if _, err := dry.svc.Create(ctx, req); err == nil || !strings.Contains(err.Error(), "node must be running") {
		t.Fatalf("non-running node was admitted: %v", err)
	}
	dry.tb.edit("vm1", nil)
	d, err := dry.svc.Create(ctx, req)
	if err != nil || d.Status != model.NodeDeployDryRun || d.Request.PlacementReview == nil ||
		d.Request.PlacementReview.EstimatedCost != review.EstimatedCost ||
		!d.Request.PlacementReview.ReviewedAt.Equal(review.ReviewedAt) {
		t.Fatalf("dry-run did not retain the accepted review: %v", err)
	}

	live := newFixture(t, false)
	live.svc.tb = &changeReviewedImage{Tumblebug: live.svc.tb}
	d, err = live.svc.Create(ctx, req)
	if err != nil {
		t.Fatal(err)
	}
	d = waitFinished(t, live.svc, d.ID)
	if d.Status != model.NodeDeployFailed || live.runner.count() != 0 {
		t.Fatal("replacement after admission was not rejected before Semaphore")
	}
}

type acceleratorCatalog struct {
	Tumblebug
	device string
}

func (c acceleratorCatalog) GetSpec(ctx context.Context, nsID, specID string) (*tumblebug.SpecInfo, error) {
	spec, err := c.Tumblebug.GetSpec(ctx, nsID, specID)
	if err == nil {
		spec.AcceleratorModel = c.device
	}
	return spec, err
}

func TestApprovedInferenceRequiresCompatibleGPU(t *testing.T) {
	gpu, cpu := true, false
	const amd = "AMD Instinct MI250"
	const nvidia = "NVIDIA RTX PRO 6000"
	for _, tc := range []struct {
		name, device string
		dryRun       bool
		approval     bool
		gpu          *bool
		want         string
		wantGPU      bool
	}{
		{"stage explicit AMD", amd, true, true, &gpu, model.NodeDeployFailed, false},
		{"stage inferred AMD", amd, true, true, nil, model.NodeDeployFailed, false},
		{"live explicit AMD", amd, false, true, &gpu, model.NodeDeployFailed, false},
		{"stage NVIDIA", nvidia, true, true, &gpu, model.NodeDeployDryRun, true},
		{"stage unknown vendor", "", true, true, &gpu, model.NodeDeployDryRun, true},
		{"stage CPU on AMD node", amd, true, true, &cpu, model.NodeDeployDryRun, false},
		{"legacy dry-run", amd, true, false, &gpu, model.NodeDeployDryRun, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fixture := newFixture(t, tc.dryRun)
			fixture.svc.tb = acceleratorCatalog{Tumblebug: fixture.svc.tb, device: tc.device}
			req := modelReq("vm1")
			req.Inference.GPU = tc.gpu
			req.Inference.RuntimeID = model.RuntimeOllama
			req.Inference.Env = nil
			req.RequireDryRun = tc.dryRun && tc.approval
			create := fixture.svc.Create
			if !tc.dryRun {
				req.Submission = &model.NodeDeploymentSubmission{ID: "3f9b2c4e-8a51-4d27-9e0c-5b1f6a7d2e48"}
				create = fixture.svc.CreateApproved
			}
			deployment, err := create(context.Background(), req)
			if err != nil {
				t.Fatal(err)
			}
			if !tc.dryRun {
				deployment = waitFinished(t, fixture.svc, deployment.ID)
			}
			node := deployment.Nodes[0]
			if deployment.Status != tc.want || fixture.runner.count() != 0 {
				t.Fatalf("status = %s, error = %q, playbooks = %d", deployment.Status, node.Error, fixture.runner.count())
			}
			if tc.want == model.NodeDeployFailed {
				if !strings.Contains(node.Error, "NVIDIA") || node.StageDir != "" {
					t.Fatalf("incompatible GPU reached staging: dir=%q, error=%q", node.StageDir, node.Error)
				}
			} else if node.GPU != tc.wantGPU || strings.Contains(node.Compose, "driver: nvidia") != tc.wantGPU {
				t.Fatalf("dry-run GPU = %t, want %t", node.GPU, tc.wantGPU)
			}
		})
	}
}
