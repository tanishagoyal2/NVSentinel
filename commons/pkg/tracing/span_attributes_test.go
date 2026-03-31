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
	"testing"

	"github.com/stretchr/testify/assert"
	"go.opentelemetry.io/otel/attribute"
	"google.golang.org/protobuf/types/known/timestamppb"

	pb "github.com/nvidia/nvsentinel/data-models/pkg/protos"
)

func TestDetectKeyValueMessage_Entity(t *testing.T) {
	entity := &pb.Entity{EntityType: "GPU", EntityValue: "0"}
	md := entity.ProtoReflect().Descriptor()

	keyFd, valFd, ok := detectKeyValueMessage(md)
	assert.True(t, ok)
	assert.Equal(t, "entityType", string(keyFd.Name()))
	assert.Equal(t, "entityValue", string(valFd.Name()))
}

func TestDetectKeyValueMessage_NonEntity(t *testing.T) {
	overrides := &pb.BehaviourOverrides{Force: true, Skip: false}
	md := overrides.ProtoReflect().Descriptor()

	_, _, ok := detectKeyValueMessage(md)
	assert.False(t, ok)
}

func TestAddHealthEventAttributes_PopulatesAttributes(t *testing.T) {
	event := &pb.HealthEvent{
		Version:        1,
		Agent:          "gpu-health-monitor",
		ComponentClass: "GPU",
		CheckName:      "GpuXidError",
		IsFatal:        true,
		IsHealthy:      false,
		NodeName:       "test-node",
		ErrorCode:      []string{"79"},
		EntitiesImpacted: []*pb.Entity{
			{EntityType: "GPU", EntityValue: "0"},
		},
		Metadata: map[string]string{
			"gpu_uuid": "GPU-123",
		},
		GeneratedTimestamp: &timestamppb.Timestamp{Seconds: 1700000000, Nanos: 0},
		QuarantineOverrides: &pb.BehaviourOverrides{
			Force: true,
			Skip:  false,
		},
	}

	var attrs []attribute.KeyValue

	addProtoMessageAttributes(&attrs, "health_event", event.ProtoReflect())

	attrMap := toAttrMap(attrs)
	assert.Equal(t, int64(1), attrMap["health_event.version"])
	assert.Equal(t, "gpu-health-monitor", attrMap["health_event.agent"])
	assert.Equal(t, "GPU", attrMap["health_event.component_class"])
	assert.Equal(t, "GpuXidError", attrMap["health_event.check_name"])
	assert.Equal(t, true, attrMap["health_event.is_fatal"])
	assert.Equal(t, false, attrMap["health_event.is_healthy"])
	assert.Equal(t, "test-node", attrMap["health_event.node_name"])
	assert.Equal(t, "79", attrMap["health_event.error_code.0"])
	assert.Equal(t, "0", attrMap["health_event.entities_impacted.GPU"])
	assert.Equal(t, "GPU-123", attrMap["health_event.metadata.gpu_uuid"])
	assert.Equal(t, true, attrMap["health_event.quarantine_overrides.force"])
	assert.Equal(t, false, attrMap["health_event.quarantine_overrides.skip"])
	assert.Contains(t, attrMap, "health_event.generated_timestamp")
}

// toAttrMap converts a slice of attribute.KeyValue to a map for easy assertion.
func toAttrMap(attrs []attribute.KeyValue) map[string]interface{} {
	m := make(map[string]interface{}, len(attrs))

	for _, a := range attrs {
		m[string(a.Key)] = a.Value.AsInterface()
	}

	return m
}
