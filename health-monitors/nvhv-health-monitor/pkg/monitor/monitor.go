// Copyright (c) 2026, NVIDIA CORPORATION.  All rights reserved.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//	http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package monitor

import (
	"context"
	"log/slog"
	"sort"
	"time"

	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/timestamppb"

	pb "github.com/nvidia/nvsentinel/data-models/pkg/protos"
	"github.com/nvidia/nvsentinel/health-monitors/nvhv-health-monitor/pkg/metrics"
	healthv1 "github.com/nvidia/nvsentinel/health-monitors/nvhv-health-monitor/pkg/verdictpb/health/v1"
)

// UnreachableCheckName is the check name of the event sent when NVHV stays
// unreachable.
const UnreachableCheckName = "NVHVUnreachable"

// PublishFunc delivers one batch of events; it returns nil only once the batch
// was delivered.
type PublishFunc func(ctx context.Context, events []*pb.HealthEvent) error

// Config configures a Monitor.
type Config struct {
	Agent    string
	NodeName string
	// UnreachableAfter is how long NVHV may stay unreachable before the
	// NVHVUnreachable event is sent.
	UnreachableAfter time.Duration
	Publish          PublishFunc
	// Now is time.Now unless a test sets it.
	Now func() time.Time
}

// Monitor keeps what has been reported and publishes only what changed.
type Monitor struct {
	cfg Config

	// reported maps a fault's key to its fingerprint. It is updated only after
	// a batch was delivered, so a failed publish is recomputed on the next poll.
	reported map[string]reportedFault

	failingSince    time.Time // zero while NVHV answers
	unreachableSent bool
}

type reportedFault struct {
	checkID     string
	entities    []string
	fingerprint string
}

// New returns a Monitor.
func New(cfg Config) *Monitor {
	if cfg.Now == nil {
		cfg.Now = time.Now
	}

	return &Monitor{cfg: cfg, reported: map[string]reportedFault{}}
}

// Handle processes the result of one poll: exactly one of verdict and err is
// set.
func (m *Monitor) Handle(ctx context.Context, verdict *healthv1.NodeVerdict, err error) {
	if err != nil {
		metrics.Unreachable.WithLabelValues(m.cfg.NodeName).Set(1)
		metrics.EvaluateFailures.WithLabelValues(m.cfg.NodeName, status.Code(err).String()).Inc()
		m.handleUnreachable(ctx)

		return
	}

	metrics.Unreachable.WithLabelValues(m.cfg.NodeName).Set(0)

	m.failingSince, m.unreachableSent = time.Time{}, false

	m.handleVerdict(ctx, verdict)
}

// handleUnreachable sends one NVHVUnreachable event once NVHV has been
// unreachable for longer than the threshold. Nothing else changes: a failed
// call neither raises nor clears a fault.
func (m *Monitor) handleUnreachable(ctx context.Context) {
	now := m.cfg.Now()
	if m.failingSince.IsZero() {
		m.failingSince = now
	}

	if m.unreachableSent || now.Sub(m.failingSince) < m.cfg.UnreachableAfter {
		return
	}

	event := &pb.HealthEvent{
		Version:            1,
		Agent:              m.cfg.Agent,
		NodeName:           m.cfg.NodeName,
		ComponentClass:     "Node",
		CheckName:          UnreachableCheckName,
		IsFatal:            true,
		IsHealthy:          false,
		Message:            "NVHV is unreachable; check its logs and the GPU driver state",
		RecommendedAction:  pb.RecommendedAction_CONTACT_SUPPORT,
		GeneratedTimestamp: timestamppb.New(now),
		ProcessingStrategy: pb.ProcessingStrategy_STORE_ONLY,
	}

	if err := m.cfg.Publish(ctx, []*pb.HealthEvent{event}); err != nil {
		slog.Warn("Publish NVHVUnreachable failed; retrying next poll", "error", err)

		return
	}

	m.unreachableSent = true
}

// handleVerdict publishes the faults that are new or changed since the last
// delivered batch. A PASS result forgets its faults, so they are reported
// again if they come back; nothing is sent for it. Faults missing from the
// verdict are kept: only a PASS ends one.
func (m *Monitor) handleVerdict(ctx context.Context, verdict *healthv1.NodeVerdict) {
	now := m.cfg.Now()
	next := make(map[string]reportedFault, len(m.reported))

	for k, v := range m.reported {
		next[k] = v
	}

	var events []*pb.HealthEvent

	for _, r := range verdict.GetCheckResults() {
		switch {
		case r.GetSeverity() == healthv1.Severity_PASS && !r.GetSkipped():
			forget(next, r)
		case reportable(r):
			f := newFault(r)
			if prev, ok := next[f.key()]; ok && prev.fingerprint == f.fingerprint {
				continue
			}

			next[f.key()] = reportedFault{checkID: f.checkID, entities: f.entities, fingerprint: f.fingerprint}
			e := f.event(m.cfg.NodeName, m.cfg.Agent)
			e.GeneratedTimestamp = timestamppb.New(now)
			events = append(events, e)
		}
	}

	if len(events) > 0 {
		sort.SliceStable(events, func(i, j int) bool { return events[i].GetCheckName() < events[j].GetCheckName() })

		if err := m.cfg.Publish(ctx, events); err != nil {
			slog.Warn("Publish failed; the record is unchanged and events are recomputed next poll",
				"events", len(events), "error", err)

			return
		}

		for _, e := range events {
			slog.Info("Published", "check", e.GetCheckName(), "fatal", e.GetIsFatal(),
				"action", e.GetRecommendedAction().String(), "message", e.GetMessage())
		}
	}

	m.reported = next
}

// forget drops the reported faults a PASS result covers: those of its check
// that share an entity with it, or all of the check's when it names none.
func forget(reported map[string]reportedFault, r *healthv1.CheckVerdict) {
	for k, f := range reported {
		if f.checkID != r.GetCheckId() {
			continue
		}

		if len(r.GetAffectedEntities()) == 0 || overlaps(f.entities, r.GetAffectedEntities()) {
			delete(reported, k)
		}
	}
}

func overlaps(a, b []string) bool {
	for _, x := range a {
		for _, y := range b {
			if x == y {
				return true
			}
		}
	}

	return false
}
