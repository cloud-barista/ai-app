package nodedeploy

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/rs/zerolog/log"

	"github.com/ish-hcc/ai-agent-proto/internal/model"
	"github.com/ish-hcc/ai-agent-proto/internal/semaphore"
	"github.com/ish-hcc/ai-agent-proto/internal/tumblebug"
)

// PlaybookComposeDown takes the compose project of a work key down on a node.
const PlaybookComposeDown = "compose-down.yml"

// archiveKindRemove is the archive record kind of taking a deployment down.
const archiveKindRemove = "node-removal"

// ErrBusy is returned when a deployment cannot be taken down because it is
// still being deployed or already being taken down.
var ErrBusy = errors.New("deployment is busy")

// Notes left on a node taken down without running anything.
const (
	noteRemoveDryRunDeployment = "the deployment was a dry run: nothing ran on the node, so nothing was taken down"
	noteRemoveNeverStarted     = "the deployment never started its service on the node, so nothing was taken down"
	noteRemoveNodeGone         = "the node no longer exists in CB-Tumblebug, so nothing was taken down"
	noteRemoveDryRunService    = "dry run is on: compose-down was not sent to Semaphore and the containers keep running"
)

// removal is what taking one node down needs.
type removal int

const (
	// removalKeep leaves a node that is removed already.
	removalKeep removal = iota
	// removalDone marks a node removed without running anything.
	removalDone
	// removalRun runs compose-down on the node.
	removalRun
)

// Remove takes a deployment down from its nodes: on every node where its
// service is still the one of its work key, compose-down stops and removes the
// containers of the compose project. The node folder and the named volumes are
// kept, so that model weights a runtime downloaded are there for the next
// deployment.
//
// Only the latest deployment of a work key on a node is live there. A node
// where a later deployment of the same work key replaced this one is marked
// removed without running anything, and its note names the later deployment:
// stopping the project would stop that deployment's containers.
//
// A node is also marked removed without running anything when the deployment
// was a dry run or never started its service there. When no node needs
// compose-down, or the service runs with dry run on, Remove returns the
// finished record. Otherwise the record is returned removing and the nodes are
// taken down in the background. A deployment still pending, running or
// removing is refused with ErrBusy; one already removed is returned as it is.
//
// The ports the deployment opened in the security groups of its nodes are
// left open: other workloads may share the groups.
func (s *Service) Remove(ctx context.Context, id string) (*model.NodeDeployment, error) {
	d, ok := s.store.Get(id)
	if !ok {
		return nil, fmt.Errorf("%w: %s", ErrNotFound, id)
	}
	switch d.Status {
	case model.NodeDeployPending, model.NodeDeployRunning, model.NodeDeployRemoving:
		return nil, fmt.Errorf("%w: deployment %s is %s; take it down once it ended", ErrBusy, id, d.Status)
	case model.NodeDeployRemoved:
		return s.Get(ctx, id)
	}
	place, err := placementFor(&d.Request)
	if err != nil {
		return nil, err
	}

	actions := make([]removal, len(d.Nodes))
	notes := make([]string, len(d.Nodes))
	runs := 0
	for i := range d.Nodes {
		actions[i], notes[i] = s.removalOf(d, i, place)
		if actions[i] == removalRun {
			if s.opts.DryRun {
				notes[i] = noteRemoveDryRunService
			} else {
				runs++
			}
		}
	}

	now := time.Now().UTC()
	status := d.Status
	var busy error
	err = s.store.Update(id, func(cur *model.NodeDeployment) {
		if cur.Status != status {
			busy = fmt.Errorf("%w: deployment %s changed to %s meanwhile", ErrBusy, id, cur.Status)
			return
		}
		removed := 0
		for i := range cur.Nodes {
			n := &cur.Nodes[i]
			switch {
			case actions[i] == removalDone:
				markNodeRemoved(n, notes[i], now)
			case actions[i] == removalRun && s.opts.DryRun:
				addNote(n, notes[i])
			case actions[i] == removalRun:
				n.Status = model.NodeDeployRemoving
			}
			if n.Status == model.NodeDeployRemoved {
				removed++
			}
		}
		switch {
		case runs > 0:
			cur.Status = model.NodeDeployRemoving
			cur.RemovedAt = nil
		case removed == len(cur.Nodes):
			cur.Status = model.NodeDeployRemoved
			cur.RemovedAt = &now
		}
	})
	if busy != nil {
		return nil, busy
	}
	if err != nil {
		log.Error().Err(err).Str("deploymentId", id).Msg("Failed to write the deployment record")
	}
	log.Info().Str("deploymentId", id).Int("nodes", len(d.Nodes)).Int("composeDown", runs).
		Bool("dryRun", s.opts.DryRun).Msg("Accepted deployment removal")

	if runs == 0 {
		if final, ok := s.store.Get(id); ok {
			s.archiveRemoval(final, now, false)
		}
		return s.Get(ctx, id)
	}

	s.wg.Add(1)
	go func() {
		defer s.wg.Done()
		s.executeRemoval(s.ctx, id, actions, place, now)
	}()
	return s.Get(ctx, id)
}

// removalOf decides what taking node idx of d down needs, and the note of a
// node marked removed without running anything.
func (s *Service) removalOf(d *model.NodeDeployment, idx int, place placement) (removal, string) {
	n := &d.Nodes[idx]
	switch {
	case n.Status == model.NodeDeployRemoved:
		return removalKeep, ""
	case d.DryRun || n.Status == model.NodeDeployDryRun:
		return removalDone, noteRemoveDryRunDeployment
	case !startedService(n):
		return removalDone, noteRemoveNeverStarted
	}
	if later := s.laterOn(d, n.Target, place.WorkKey); later != "" {
		return removalDone, replacedNote(later)
	}
	return removalRun, ""
}

func replacedNote(later string) string {
	return "deployment " + later + " of the same work key replaced this one on the node later; " +
		"its containers were left running"
}

// startedService reports whether a node got as far as starting its service:
// compose-up or copy-and-run was run there, whatever it ended with, since a
// playbook that failed may have left containers.
func startedService(n *model.NodeDeployResult) bool {
	for _, st := range n.Steps {
		if st.Playbook == PlaybookComposeUp || st.Playbook == PlaybookCopyAndRun {
			return true
		}
	}
	return false
}

// laterOn returns a deployment created after d that holds work key on target,
// or "" when there is none. A later deployment holds the work key while it is
// pending or running there, and once it started its service there, unless it
// was a dry run or has been taken down since.
func (s *Service) laterOn(d *model.NodeDeployment, target model.NodeDeployTarget, workKey string) string {
	key := target.Key()
	for _, e := range s.store.List() {
		if e.ID == d.ID || e.DryRun || !e.CreatedAt.After(d.CreatedAt) {
			continue
		}
		place, err := placementFor(&e.Request)
		if err != nil || place.WorkKey != workKey {
			continue
		}
		for i := range e.Nodes {
			n := &e.Nodes[i]
			if n.Target.Key() == key && holdsWorkKey(n) {
				return e.ID
			}
		}
	}
	return ""
}

// holdsWorkKey reports whether node result n of a deployment that is not a
// dry run holds its work key on its node: it is pending or running there, or
// it started its service there and has not been taken down since.
func holdsWorkKey(n *model.NodeDeployResult) bool {
	switch n.Status {
	case model.NodeDeployPending, model.NodeDeployRunning:
		return true
	case model.NodeDeployRemoving, model.NodeDeployRemoved, model.NodeDeployDryRun:
		return false
	}
	return startedService(n)
}

// executeRemoval takes down every node marked removalRun and records the
// outcome.
func (s *Service) executeRemoval(ctx context.Context, id string, actions []removal, place placement, started time.Time) {
	limit := make(chan struct{}, s.opts.MaxParallel)
	var wg sync.WaitGroup
	for i, action := range actions {
		if action != removalRun {
			continue
		}
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			select {
			case limit <- struct{}{}:
			case <-ctx.Done():
				s.finishRemoval(id, i, model.NodeDeployRemoveFailed, "not started: "+ctx.Err().Error(), "")
				return
			}
			defer func() { <-limit }()
			s.removeNode(ctx, id, i, place)
		}(i)
	}
	wg.Wait()

	final, ok := s.store.Get(id)
	if !ok {
		return
	}
	now := time.Now().UTC()
	final.Status = removalStatus(final)
	final.RemovedAt = &now
	s.archiveRemoval(final, started, true)
	s.update(id, func(d *model.NodeDeployment) {
		d.Status = final.Status
		d.RemovedAt = &now
	})
	log.Info().Str("deploymentId", id).Str("status", final.Status).Msg("Deployment removal ended")
}

// removalStatus derives the status of a deployment taken down from its nodes.
func removalStatus(d *model.NodeDeployment) string {
	for _, n := range d.Nodes {
		if n.Status != model.NodeDeployRemoved {
			return model.NodeDeployRemoveFailed
		}
	}
	return model.NodeDeployRemoved
}

// removeNode runs compose-down for the work key of a deployment on one node.
func (s *Service) removeNode(ctx context.Context, id string, idx int, place placement) {
	d, ok := s.store.Get(id)
	if !ok || idx >= len(d.Nodes) {
		return
	}
	target := d.Nodes[idx].Target
	logger := log.With().Str("deploymentId", id).Str("nsId", target.NsID).Str("infraId", target.InfraID).
		Str("nodeId", target.NodeID).Logger()
	fail := func(err error) {
		logger.Warn().Str("error", err.Error()).Msg("Node removal failed")
		s.finishRemoval(id, idx, model.NodeDeployRemoveFailed, err.Error(), "")
	}

	// The check for a later deployment is made again under the lock that
	// deployments of the work key take before they run, so that one started
	// meanwhile is not taken down with this one.
	unlock, err := s.locks.lock(ctx, target.Key()+" "+place.WorkKey)
	if err != nil {
		fail(fmt.Errorf("waiting for another deployment of %s on the node: %w", place.WorkKey, err))
		return
	}
	defer unlock()
	if later := s.laterOn(d, target, place.WorkKey); later != "" {
		s.finishRemoval(id, idx, model.NodeDeployRemoved, "", replacedNote(later))
		logger.Info().Str("laterDeploymentId", later).Msg("Left the node to a later deployment")
		return
	}

	node, err := s.tb.GetNode(ctx, target.NsID, target.InfraID, target.NodeID)
	if errors.Is(err, tumblebug.ErrNodeNotFound) {
		s.finishRemoval(id, idx, model.NodeDeployRemoved, "", noteRemoveNodeGone)
		return
	}
	if err != nil {
		fail(err)
		return
	}
	if node.SSHKeyID == "" {
		fail(fmt.Errorf("node %q of infra %q has no ssh key", target.NodeID, target.InfraID))
		return
	}
	key, err := s.tb.GetSSHKey(ctx, target.NsID, node.SSHKeyID)
	if err != nil {
		fail(err)
		return
	}
	access, err := tumblebug.ResolveNodeAccess(node, key)
	if err != nil {
		fail(err)
		return
	}
	if err := semaphore.CheckPrivateKey(access.PrivateKey); err != nil {
		fail(fmt.Errorf("ssh key %q of node %q: %w", node.SSHKeyID, target.NodeID, err))
		return
	}

	spec := withPlaybook(semaphore.RunSpec{
		Name: shortID(id) + "-down-" + access.CSP + "-" + target.NodeID,
		Host: access.Host, Port: access.Port, User: access.User, PrivateKey: access.PrivateKey,
	}, PlaybookComposeDown, map[string]any{
		"deployment_id": id, "work_key": place.WorkKey, "node_dir": place.NodeDir, "compose_project": place.Project,
	})
	if err := s.runStep(ctx, id, idx, spec); err != nil {
		fail(err)
		return
	}
	s.finishRemoval(id, idx, model.NodeDeployRemoved, "", "")
	logger.Info().Str("workKey", place.WorkKey).Msg("Node removal succeeded")
}

func (s *Service) finishRemoval(id string, idx int, status, errText, note string) {
	now := time.Now().UTC()
	s.updateNode(id, idx, func(n *model.NodeDeployResult) {
		if status == model.NodeDeployRemoved {
			markNodeRemoved(n, note, now)
			return
		}
		n.Status = status
		n.Error = errText
		addNote(n, note)
	})
}

func markNodeRemoved(n *model.NodeDeployResult, note string, now time.Time) {
	n.Status = model.NodeDeployRemoved
	n.Error = ""
	n.RemovedAt = &now
	addNote(n, note)
}

// addNote appends note unless it is empty or the last note already.
func addNote(n *model.NodeDeployResult, note string) {
	if note == "" || (len(n.Notes) > 0 && n.Notes[len(n.Notes)-1] == note) {
		return
	}
	n.Notes = append(n.Notes, note)
}

// archiveRemoval leaves one archive line for taking a deployment down.
func (s *Service) archiveRemoval(d *model.NodeDeployment, started time.Time, executed bool) {
	if s.archive == nil {
		return
	}
	type nodeOutcome struct {
		Target model.NodeDeployTarget `json:"target"`
		Status string                 `json:"status"`
		Error  string                 `json:"error,omitempty"`
		Notes  []string               `json:"notes,omitempty"`
	}
	outcomes := make([]nodeOutcome, len(d.Nodes))
	var failures []string
	for i, n := range d.Nodes {
		outcomes[i] = nodeOutcome{Target: n.Target, Status: n.Status, Error: n.Error, Notes: n.Notes}
		if n.Status == model.NodeDeployRemoveFailed {
			failures = append(failures, n.Target.Key()+": "+n.Error)
		}
	}
	ended := time.Now().UTC()
	record := model.ArchiveRecord{
		RunID: "remove-" + d.ID + "-" + fmt.Sprint(started.UnixNano()), StartedAt: started, EndedAt: ended,
		Namespace: firstNamespace(d), Kind: archiveKindRemove,
		Steps: []model.ToolStep{{
			Index: 1, Tool: "delete_deployment", Grade: "destructive",
			Input: map[string]any{"id": d.ID}, Executed: executed,
			Output:   map[string]any{"id": d.ID, "status": d.Status, "nodes": outcomes},
			CalledAt: started, Duration: ended.Sub(started).Round(time.Millisecond).String(),
		}},
	}
	if len(failures) > 0 {
		record.Error = strings.Join(failures, "; ")
	}
	if err := s.archive.Append(context.Background(), record); err != nil {
		log.Error().Err(err).Str("deploymentId", d.ID).Msg("Failed to archive deployment removal")
	}
}
