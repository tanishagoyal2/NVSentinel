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

// Package monitor turns NVHV verdicts into NVSentinel health events (ADR-076).
//
// Only unhealthy events are produced, always with the STORE_ONLY processing
// strategy: the monitor records faults for observability and never asks
// NVSentinel to act on them, so it sends no recovery events either.
package monitor

import (
	"fmt"
	"regexp"
	"sort"
	"strings"

	"google.golang.org/protobuf/types/known/structpb"

	pb "github.com/nvidia/nvsentinel/data-models/pkg/protos"
	healthv1 "github.com/nvidia/nvsentinel/health-monitors/nvhv-health-monitor/pkg/verdictpb/health/v1"
)

const (
	// CheckNamePrefix keeps NVHV check names from colliding with the check
	// names of the monitors NVHV's checks replace.
	CheckNamePrefix = "NVHV"

	entityGPUUUID = "GPU_UUID"
	entityPCI     = "PCI"

	gpuUUIDPrefix = "GPU-"
)

var pciAddress = regexp.MustCompile(`^(?:[0-9a-fA-F]{4}:)?[0-9a-fA-F]{2}:[0-9a-fA-F]{2}\.[0-7]$`)

// fault is one non-PASS check result that is to be reported.
type fault struct {
	checkID     string
	entities    []string // sorted affected entities, used as part of the fault's identity
	fingerprint string   // changes when the result changes in a way worth re-reporting
	event       func(nodeName, agent string) *pb.HealthEvent
}

// key identifies a fault across polls.
func (f fault) key() string { return faultKey(f.checkID, f.entities) }

func faultKey(checkID string, entities []string) string {
	return checkID + "|" + strings.Join(entities, ",")
}

// reportable reports whether a result is a fault. UNKNOWN and skipped results
// say nothing about the node's health, and PASS is not a fault.
func reportable(r *healthv1.CheckVerdict) bool {
	if r.GetSkipped() {
		return false
	}

	switch r.GetSeverity() {
	case healthv1.Severity_CRITICAL, healthv1.Severity_WARNING, healthv1.Severity_INFO:
		return true
	default:
		return false
	}
}

func newFault(r *healthv1.CheckVerdict) fault {
	entities := append([]string(nil), r.GetAffectedEntities()...)
	sort.Strings(entities)

	action := recommendedAction(r.GetRecommendedAction())
	message := strings.Join(r.GetMessages(), "; ")

	return fault{
		checkID:     r.GetCheckId(),
		entities:    entities,
		fingerprint: fmt.Sprintf("%s|%s|%s", r.GetSeverity(), action, message),
		event: func(nodeName, agent string) *pb.HealthEvent {
			return &pb.HealthEvent{
				Version:            1,
				Agent:              agent,
				NodeName:           nodeName,
				ComponentClass:     strings.ToUpper(r.GetCategory().String()),
				CheckName:          CheckNamePrefix + r.GetCheckId(),
				IsFatal:            r.GetSeverity() == healthv1.Severity_CRITICAL,
				IsHealthy:          false,
				Message:            message,
				RecommendedAction:  action,
				ErrorCode:          []string{r.GetCheckId()},
				EntitiesImpacted:   entitiesOf(r),
				ProcessingStrategy: pb.ProcessingStrategy_STORE_ONLY,
			}
		},
	}
}

// recommendedAction follows the ADR: a "monitor" operational state needs no
// action; otherwise the first remediation action decides.
func recommendedAction(a *healthv1.RecommendedAction) pb.RecommendedAction {
	if a.GetOperationalState() == healthv1.OperationalState_monitor {
		return pb.RecommendedAction_NONE
	}

	actions := a.GetRemediationActions()
	if len(actions) == 0 {
		return pb.RecommendedAction_NONE
	}

	switch actions[0] {
	case "reset_gpu":
		return pb.RecommendedAction_COMPONENT_RESET
	case "restart_os":
		return pb.RecommendedAction_RESTART_VM
	}

	// service_required and every other action need a person.
	return pb.RecommendedAction_CONTACT_SUPPORT
}

// entitiesOf returns the GPU UUIDs from affected_entities and the PCI
// addresses found in observed_values.
func entitiesOf(r *healthv1.CheckVerdict) []*pb.Entity {
	var out []*pb.Entity

	for _, e := range r.GetAffectedEntities() {
		if strings.HasPrefix(e, gpuUUIDPrefix) {
			out = append(out, &pb.Entity{EntityType: entityGPUUUID, EntityValue: e})
		}
	}

	keys := make([]string, 0, len(r.GetObservedValues()))
	for k := range r.GetObservedValues() {
		keys = append(keys, k)
	}

	sort.Strings(keys)

	for _, k := range keys {
		if s, ok := r.GetObservedValues()[k].GetKind().(*structpb.Value_StringValue); ok && pciAddress.MatchString(s.StringValue) {
			out = append(out, &pb.Entity{EntityType: entityPCI, EntityValue: s.StringValue})
		}
	}

	return out
}
