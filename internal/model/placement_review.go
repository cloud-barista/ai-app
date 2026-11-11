package model

import (
	"regexp"
	"strings"
	"time"
	"unicode"
)

var (
	reviewResourceID = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._+:/-]{0,255}$`)
	reviewStatusID   = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9 ()_-]{0,63}$`)
)

// NodePlacementReview records a selected node group's CB-Tumblebug review.
type NodePlacementReview struct {
	NsID          string    `json:"nsId" example:"etri"`
	SpecID        string    `json:"specId" example:"aws+ap-northeast-2+g4dn.xlarge"`
	ImageID       string    `json:"imageId" example:"aws+ap-northeast-2+ami-example"`
	Status        string    `json:"status" enums:"Ready,Warning"`
	SpecStatus    string    `json:"specStatus" example:"Available"`
	ImageStatus   string    `json:"imageStatus" example:"Available"`
	EstimatedCost string    `json:"estimatedCost,omitempty" example:"$0.50/hour"`
	ReviewedAt    time.Time `json:"reviewedAt"`
}

func (r *NodePlacementReview) validate(targets []NodeDeployTarget) error {
	if len(targets) != 1 || r.NsID != targets[0].NsID {
		return errInvalid("placementReview.nsId", "must match the single deployment target")
	}
	if !reviewResourceID.MatchString(r.SpecID) || !reviewResourceID.MatchString(r.ImageID) {
		return errInvalid("placementReview", "requires CB-Tumblebug spec and image IDs")
	}
	if (r.Status != "Ready" && r.Status != "Warning") || !reviewStatusID.MatchString(r.SpecStatus) ||
		!reviewStatusID.MatchString(r.ImageStatus) {
		return errInvalid("placementReview", "requires a viable review and the spec and image statuses")
	}
	if r.ReviewedAt.IsZero() || r.ReviewedAt.After(time.Now().Add(time.Minute)) {
		return errInvalid("placementReview.reviewedAt", "requires the review timestamp")
	}
	if len(r.EstimatedCost) > 128 || strings.IndexFunc(r.EstimatedCost, unicode.IsControl) >= 0 {
		return errInvalid("placementReview.estimatedCost", "must be a short cost estimate")
	}
	return nil
}
