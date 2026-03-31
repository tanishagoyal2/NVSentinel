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
	"google.golang.org/protobuf/reflect/protoreflect"

	pb "github.com/nvidia/nvsentinel/data-models/pkg/protos"
)

const timestampFullName protoreflect.FullName = "google.protobuf.Timestamp"

// AddHealthEventStatusAttributes adds all HealthEventStatus fields to a span as attributes
// using protobuf reflection. The eventId is added separately since it lives on the parent document.
func AddHealthEventStatusAttributes(
	span trace.Span, healthEventStatus *pb.HealthEventStatus, eventId string,
) {
	if span == nil || healthEventStatus == nil {
		return
	}

	span.SetAttributes(attribute.String("health_event.id", eventId))

	var attrs []attribute.KeyValue

	addProtoMessageAttributes(&attrs, "health_event_status", healthEventStatus.ProtoReflect())
	span.SetAttributes(attrs...)
}

// AddHealthEventAttributes adds all HealthEvent fields to a span as attributes.
func AddHealthEventAttributes(span trace.Span, event *pb.HealthEvent) {
	if span == nil || event == nil {
		return
	}

	var attrs []attribute.KeyValue

	addProtoMessageAttributes(&attrs, "health_event", event.ProtoReflect())
	span.SetAttributes(attrs...)
}

// addProtoMessageAttributes recursively walks all fields of a protobuf message
// and appends them as OpenTelemetry span attributes under the given key prefix.
func addProtoMessageAttributes(
	attrs *[]attribute.KeyValue, prefix string, msg protoreflect.Message,
) {
	fields := msg.Descriptor().Fields()
	for i := 0; i < fields.Len(); i++ {
		fd := fields.Get(i)
		key := prefix + "." + camelToSnakeCase(string(fd.Name()))

		switch {
		case fd.IsMap():
			addMapAttributes(attrs, key, fd, msg.Get(fd).Map())
		case fd.IsList():
			addListAttributes(attrs, key, fd, msg.Get(fd).List())
		case fd.Kind() == protoreflect.MessageKind ||
			fd.Kind() == protoreflect.GroupKind:
			if !msg.Has(fd) {
				continue
			}

			addMessageAttribute(attrs, key, msg.Get(fd).Message())
		default:
			addScalarAttribute(attrs, key, fd, msg.Get(fd))
		}
	}
}

func addScalarAttribute(
	attrs *[]attribute.KeyValue, key string,
	fd protoreflect.FieldDescriptor, val protoreflect.Value,
) {
	switch fd.Kind() {
	case protoreflect.BoolKind:
		*attrs = append(*attrs, attribute.Bool(key, val.Bool()))
	case protoreflect.StringKind:
		*attrs = append(*attrs, attribute.String(key, val.String()))
	case protoreflect.EnumKind:
		enumVal := fd.Enum().Values().ByNumber(val.Enum())

		name := fmt.Sprintf("%d", val.Enum())
		if enumVal != nil {
			name = string(enumVal.Name())
		}

		*attrs = append(*attrs, attribute.String(key, name))
	case protoreflect.Int32Kind, protoreflect.Int64Kind,
		protoreflect.Sint32Kind, protoreflect.Sint64Kind,
		protoreflect.Sfixed32Kind, protoreflect.Sfixed64Kind:
		*attrs = append(*attrs, attribute.Int64(key, val.Int()))
	case protoreflect.Uint32Kind, protoreflect.Uint64Kind,
		protoreflect.Fixed32Kind, protoreflect.Fixed64Kind:
		*attrs = append(*attrs, attribute.Int64(key, int64(val.Uint()))) //nolint:gosec
	case protoreflect.FloatKind, protoreflect.DoubleKind:
		*attrs = append(*attrs, attribute.Float64(key, val.Float()))
	case protoreflect.BytesKind, protoreflect.MessageKind, protoreflect.GroupKind:
	}
}

func addMessageAttribute(
	attrs *[]attribute.KeyValue, key string, msg protoreflect.Message,
) {
	if msg.Descriptor().FullName() == timestampFullName {
		seconds := msg.Get(msg.Descriptor().Fields().ByName("seconds")).Int()
		nanos := msg.Get(msg.Descriptor().Fields().ByName("nanos")).Int()
		t := time.Unix(seconds, nanos).UTC()
		*attrs = append(*attrs, attribute.String(key, t.Format(time.RFC3339Nano)))

		return
	}

	addProtoMessageAttributes(attrs, key, msg)
}

func addMapAttributes(
	attrs *[]attribute.KeyValue, prefix string,
	fd protoreflect.FieldDescriptor, m protoreflect.Map,
) {
	valDesc := fd.MapValue()

	m.Range(func(k protoreflect.MapKey, v protoreflect.Value) bool {
		mapKey := prefix + "." + k.String()
		addScalarAttribute(attrs, mapKey, valDesc, v)

		return true
	})
}

func addListAttributes(
	attrs *[]attribute.KeyValue, prefix string,
	fd protoreflect.FieldDescriptor, list protoreflect.List,
) {
	if list.Len() == 0 {
		return
	}

	if fd.Kind() == protoreflect.MessageKind {
		if keyFd, valFd, ok := detectKeyValueMessage(fd.Message()); ok {
			for i := 0; i < list.Len(); i++ {
				elemMsg := list.Get(i).Message()
				k := elemMsg.Get(keyFd).String()
				v := elemMsg.Get(valFd).String()
				*attrs = append(*attrs, attribute.String(prefix+"."+k, v))
			}

			return
		}

		for i := 0; i < list.Len(); i++ {
			addProtoMessageAttributes(attrs, fmt.Sprintf("%s.%d", prefix, i), list.Get(i).Message())
		}

		return
	}

	for i := 0; i < list.Len(); i++ {
		addScalarAttribute(attrs, fmt.Sprintf("%s.%d", prefix, i), fd, list.Get(i))
	}
}

// detectKeyValueMessage checks whether a repeated message is the Entity
// key-value pair (entityType / entityValue). When detected, the list is
// flattened using entityType as the attribute key suffix.
func detectKeyValueMessage(
	md protoreflect.MessageDescriptor,
) (keyFd, valFd protoreflect.FieldDescriptor, ok bool) {
	keyField := md.Fields().ByName("entityType")
	valField := md.Fields().ByName("entityValue")

	if keyField == nil || valField == nil {
		return nil, nil, false
	}

	return keyField, valField, true
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
