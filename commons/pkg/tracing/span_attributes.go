// Copyright (c) 2025, NVIDIA CORPORATION.  All rights reserved.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package tracing

import (
	"fmt"
	"strings"
	"time"
	"unicode"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/trace"

	pb "github.com/nvidia/nvsentinel/data-models/pkg/protos"
)

// AddHealthEventStatusAttributes adds all HealthEventStatus fields to a span as attributes
// The eventId is added separately since it lives on the parent document.
func AddHealthEventStatusAttributes(span trace.Span, healthEventStatus *pb.HealthEventStatus, eventId string) {
	if span == nil || healthEventStatus == nil {
		return
	}

	attrs := []attribute.KeyValue{}

	attrs = append(attrs, attribute.String("health_event.id", eventId))

	nodeQuarantined := ""
	if healthEventStatus.NodeQuarantined != "" {
		nodeQuarantined = healthEventStatus.NodeQuarantined
	}

	attrs = append(attrs, attribute.String("health_event_status.node_quarantined", nodeQuarantined))

	quarantineFinishTs := ""
	if healthEventStatus.QuarantineFinishTimestamp != nil {
		quarantineFinishTs = healthEventStatus.QuarantineFinishTimestamp.AsTime().Format(time.RFC3339Nano)
	}

	attrs = append(attrs, attribute.String("health_event_status.quarantine_finish_timestamp", quarantineFinishTs))

	if healthEventStatus.UserPodsEvictionStatus != nil {
		attrs = append(attrs,
			attribute.String("health_event_status.user_pod_eviction.status",
				healthEventStatus.UserPodsEvictionStatus.GetStatus()),
			attribute.String("health_event_status.user_pod_eviction.message",
				healthEventStatus.UserPodsEvictionStatus.GetMessage()),
		)
	}

	drainFinishTs := ""
	if healthEventStatus.DrainFinishTimestamp != nil {
		drainFinishTs = healthEventStatus.DrainFinishTimestamp.AsTime().Format(time.RFC3339Nano)
	}

	attrs = append(attrs, attribute.String("health_event_status.drain_finish_timestamp", drainFinishTs))

	faultRemediated := false
	if healthEventStatus.FaultRemediated != nil {
		faultRemediated = healthEventStatus.FaultRemediated.GetValue()
	}

	attrs = append(attrs, attribute.Bool("health_event_status.fault_remediated", faultRemediated))

	lastRemediationTs := ""
	if healthEventStatus.LastRemediationTimestamp != nil {
		lastRemediationTs = healthEventStatus.LastRemediationTimestamp.AsTime().Format(time.RFC3339Nano)
	}

	attrs = append(attrs, attribute.String("health_event_status.last_remediation_timestamp", lastRemediationTs))

	span.SetAttributes(attrs...)
}

// AddHealthEventAttributes adds all HealthEvent fields to a span as attributes
// Uses indexed attributes for arrays and flattened prefix for maps
func AddHealthEventAttributes(span trace.Span, event *pb.HealthEvent) {
	if span == nil || event == nil {
		return
	}

	attrs := []attribute.KeyValue{}

	attrs = append(attrs,
		attribute.Int("health_event.version", int(event.Version)),
		attribute.String("health_event.agent", event.Agent),
		attribute.String("health_event.component_class", event.ComponentClass),
		attribute.String("health_event.check_name", event.CheckName),
		attribute.Bool("health_event.is_fatal", event.IsFatal),
		attribute.Bool("health_event.is_healthy", event.IsHealthy),
		attribute.String("health_event.node_name", event.NodeName),
		attribute.String("health_event.id", event.Id),
		attribute.String("health_event.recommended_action", event.RecommendedAction.String()),
		attribute.String("health_event.processing_strategy", event.ProcessingStrategy.String()),
	)

	attrs = append(attrs, attribute.String("health_event.message", event.Message))

	// errorCode array: health_event.error_code.0 = "31", health_event.error_code.1 = "79", ...
	for i, code := range event.ErrorCode {
		attrKey := fmt.Sprintf("health_event.error_code.%d", i)
		attrs = append(attrs, attribute.String(attrKey, code))
	}

	// entitiesImpacted array:
	// health_event.entities_impacted.pci_id = "0000:16:00"
	for _, entity := range event.EntitiesImpacted {
		if entity != nil {
			attrs = append(attrs, attribute.String(fmt.Sprintf("health_event.entities_impacted.%s",
				entity.EntityType), entity.EntityValue))
		}
	}

	// metadata map: health_event.metadata.serial_number="1655123000632", ...
	for key, value := range event.Metadata {
		attrKey := fmt.Sprintf("health_event.metadata.%s", camelToSnakeCase(key))

		attrs = append(attrs, attribute.String(attrKey, value))
	}

	if event.QuarantineOverrides != nil {
		attrs = append(attrs,
			attribute.Bool("health_event.quarantine_overrides.force", event.QuarantineOverrides.Force),
			attribute.Bool("health_event.quarantine_overrides.skip", event.QuarantineOverrides.Skip),
		)
	}

	if event.DrainOverrides != nil {
		attrs = append(attrs,
			attribute.Bool("health_event.drain_overrides.force", event.DrainOverrides.Force),
			attribute.Bool("health_event.drain_overrides.skip", event.DrainOverrides.Skip),
		)
	}

	if event.GeneratedTimestamp != nil {
		attrs = append(attrs,
			attribute.String("health_event.generated_timestamp",
				event.GeneratedTimestamp.AsTime().Format(time.RFC3339Nano)),
		)
	}

	span.SetAttributes(attrs...)
}

func camelToSnakeCase(s string) string {
	var result strings.Builder

	for i, r := range s {
		if unicode.IsUpper(r) {
			if i > 0 {
				result.WriteByte('_')
			}

			result.WriteRune(unicode.ToLower(r))
		} else {
			result.WriteRune(r)
		}
	}

	return result.String()
}
