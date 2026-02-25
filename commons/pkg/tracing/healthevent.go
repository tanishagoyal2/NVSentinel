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

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/trace"

	pb "github.com/nvidia/nvsentinel/data-models/pkg/protos"
)

// AddHealthEventAttributes adds all HealthEvent fields to a span as attributes
// Uses indexed attributes for arrays and flattened prefix for maps
// This should be called on ROOT spans (Platform Connector) and MODULE processing spans
func AddHealthEventAttributes(span trace.Span, event *pb.HealthEvent) {
	if span == nil || event == nil {
		return
	}

	attrs := []attribute.KeyValue{}

	// ===== Scalar Fields =====
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

	// Message (truncate if needed - OpenTelemetry limit is 250 bytes per value)
	message := event.Message
	if len(message) > 250 {
		message = message[:247] + "..."
	}
	if message != "" {
		attrs = append(attrs, attribute.String("health_event.message", message))
	}

	// ===== Arrays: Indexed Attributes =====

	// errorCode array: health_event.error_code.0, health_event.error_code.1, ...
	for i, code := range event.ErrorCode {
		attrKey := fmt.Sprintf("health_event.error_code.%d", i)
		attrs = append(attrs, attribute.String(attrKey, code))
	}

	// entitiesImpacted array: health_event.entities_impacted.0.entity_type, ...
	for i, entity := range event.EntitiesImpacted {
		if entity != nil {
			attrs = append(attrs,
				attribute.String(fmt.Sprintf("health_event.entities_impacted.%d.entity_type", i), entity.EntityType),
				attribute.String(fmt.Sprintf("health_event.entities_impacted.%d.entity_value", i), entity.EntityValue),
			)
		}
	}

	// ===== Maps: Flatten with Prefix =====

	// metadata map: health_event.metadata.gpu_uuid, health_event.metadata.gpu_index, ...
	for key, value := range event.Metadata {
		sanitizedKey := sanitizeAttributeKey(key)
		attrKey := fmt.Sprintf("health_event.metadata.%s", sanitizedKey)

		// Handle value length limit (250 bytes)
		finalValue := value
		if len(value) > 250 {
			finalValue = value[:247] + "..."
		}

		attrs = append(attrs, attribute.String(attrKey, finalValue))
	}

	// ===== Nested Objects =====

	// quarantineOverrides
	if event.QuarantineOverrides != nil {
		attrs = append(attrs,
			attribute.Bool("health_event.quarantine_overrides.force", event.QuarantineOverrides.Force),
			attribute.Bool("health_event.quarantine_overrides.skip", event.QuarantineOverrides.Skip),
		)
	}

	// drainOverrides
	if event.DrainOverrides != nil {
		attrs = append(attrs,
			attribute.Bool("health_event.drain_overrides.force", event.DrainOverrides.Force),
			attribute.Bool("health_event.drain_overrides.skip", event.DrainOverrides.Skip),
		)
	}

	// ===== Timestamp =====
	if event.GeneratedTimestamp != nil {
		attrs = append(attrs,
			attribute.String("health_event.generated_timestamp",
				event.GeneratedTimestamp.AsTime().Format(time.RFC3339Nano)),
		)
	}

	// Set all attributes at once (more efficient)
	span.SetAttributes(attrs...)
}

// sanitizeAttributeKey ensures the key is valid for OpenTelemetry attributes
// OpenTelemetry attribute keys must match: [a-zA-Z][a-zA-Z0-9_.-]*
// Replaces invalid characters with underscores
func sanitizeAttributeKey(key string) string {
	if key == "" {
		return "_"
	}

	sanitized := strings.Builder{}
	for i, r := range key {
		if i == 0 {
			// First char must be letter
			if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') {
				sanitized.WriteRune(r)
			} else {
				sanitized.WriteRune('_')
			}
		} else {
			// Subsequent chars: letter, digit, underscore, period, hyphen
			if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') ||
				(r >= '0' && r <= '9') || r == '_' || r == '.' || r == '-' {
				sanitized.WriteRune(r)
			} else {
				sanitized.WriteRune('_')
			}
		}
	}

	result := sanitized.String()
	if result == "" {
		return "_"
	}

	return result
}
