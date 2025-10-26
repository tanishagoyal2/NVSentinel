// Copyright (c) 2025, NVIDIA CORPORATION.  All rights reserved.
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

package reconciler

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"reflect"
	"strconv"
	"strings"

	platform_connectors "github.com/nvidia/nvsentinel/data-models/pkg/protos"
	storeconnector "github.com/nvidia/nvsentinel/platform-connectors/pkg/connectors/store"
	"go.mongodb.org/mongo-driver/bson"
)

type Parser struct {
	event storeconnector.HealthEventWithStatus
}

// parseSequenceString converts a criteria map into a BSON document for MongoDB queries
func (p *Parser) parseSequenceString(criteria map[string]any) (bson.D, error) {
	doc := bson.D{}

	for key, value := range criteria {
		// Non-string values are appended as-is
		strVal, isString := value.(string)

		if !isString {
			doc = append(doc, bson.E{Key: key, Value: value})
			continue
		}

		// "this." reference → resolve from current event
		if strings.HasPrefix(strVal, "this.") {
			fieldPath := strings.TrimPrefix(strVal, "this.")
			resolvedValue, err := p.getValueFromPath(fieldPath)

			if err != nil {
				return doc, err
			}

			doc = append(doc, bson.E{Key: key, Value: resolvedValue})

			continue
		}

		// JSON object string representing MongoDB operator (e.g. '{"$ne":"x"}')
		if strings.HasPrefix(strVal, "{") && strings.HasSuffix(strVal, "}") {
			var operatorMap map[string]any
			if err := json.Unmarshal([]byte(strVal), &operatorMap); err != nil {
				slog.Warn("Failed to parse MongoDB operator string, treating as literal", "string", strVal, "error", err)
				doc = append(doc, bson.E{Key: key, Value: strVal})
			} else {
				doc = append(doc, bson.E{Key: key, Value: operatorMap})
			}

			continue
		}

		// Plain string literal
		doc = append(doc, bson.E{Key: key, Value: strVal})
	}

	return doc, nil
}

// getValueFromPath extracts a value from the event using a dot-notation path
func (p *Parser) getValueFromPath(path string) (any, error) {
	parts := strings.Split(path, ".")

	if len(parts) > 0 && (parts[0] == "healthevent") {
		return getValueFromHealthEvent(p.event.HealthEvent, parts[1:]), nil
	} else if len(parts) > 0 && (parts[0] == "healtheventstatus") {
		return getValueFromHealthEventStatus(p.event.HealthEventStatus, parts[1:]), nil
	}

	return nil, fmt.Errorf("invalid path: %s", path)
}

// getFieldByName is a common helper function to find a field by name using reflection (case-insensitive)
func getFieldByName(val reflect.Value, fieldName string) any {
	for i := 0; i < val.NumField(); i++ {
		field := val.Type().Field(i)
		if strings.EqualFold(field.Name, fieldName) {
			return val.Field(i).Interface()
		}
	}

	return nil
}

func getValueFromHealthEvent(event *platform_connectors.HealthEvent, parts []string) any {
	if len(parts) == 0 {
		return nil
	}

	rootField := strings.ToLower(parts[0])

	// Simple one-level field lookup
	if len(parts) == 1 {
		return getFieldByName(reflect.ValueOf(event).Elem(), rootField)
	}

	switch rootField {
	case "errorcode":
		return getValueFromErrorCode(event, parts[1:])
	case "entitiesimpacted":
		return getValueFromEntitiesImpacted(event, parts[1:])
	case "metadata":
		return getValueFromMetadata(event, parts[1:])
	case "generatedtimestamp":
		return getValueFromGeneratedTimestamp(event, parts[1:])
	default:
		return nil
	}
}

// getValueFromErrorCode safely returns event.ErrorCode[index] if present.
func getValueFromErrorCode(event *platform_connectors.HealthEvent, parts []string) any {
	if len(parts) == 0 {
		return nil
	}

	idx, err := strconv.Atoi(parts[0])
	if err != nil || idx >= len(event.ErrorCode) {
		return nil
	}

	return event.ErrorCode[idx]
}

// getValueFromMetadata returns the value for a metadata key if it exists.
func getValueFromMetadata(event *platform_connectors.HealthEvent, parts []string) any {
	if len(parts) == 0 {
		return nil
	}

	key := parts[0]
	if val, ok := event.Metadata[key]; ok {
		return val
	}

	return nil
}

func getValueFromEntitiesImpacted(event *platform_connectors.HealthEvent, parts []string) any {
	if len(parts) < 2 {
		return nil
	}

	if idx, err := strconv.Atoi(parts[0]); err == nil && idx < len(event.EntitiesImpacted) {
		entity := event.EntitiesImpacted[idx]
		subField := strings.ToLower(parts[1])

		entityVal := reflect.ValueOf(entity).Elem()

		return getFieldByName(entityVal, subField)
	}

	return nil
}

func getValueFromGeneratedTimestamp(event *platform_connectors.HealthEvent, parts []string) any {
	if len(parts) < 2 {
		return nil
	}

	subField := strings.ToLower(parts[1])

	timestampVal := reflect.ValueOf(event.GeneratedTimestamp).Elem()
	for i := 0; i < timestampVal.NumField(); i++ {
		field := timestampVal.Type().Field(i)
		if strings.EqualFold(field.Name, subField) {
			return timestampVal.Field(i).Interface()
		}
	}

	return nil
}

func getValueFromHealthEventStatus(event storeconnector.HealthEventStatus, parts []string) any {
	rootField := strings.ToLower(parts[0])

	if len(parts) == 1 {
		val := reflect.ValueOf(event)

		return getFieldByName(val, rootField)
	}

	// Handle nested fields in HealthEventStatus (e.g., userpodsevictionstatus.status)
	if strings.EqualFold(rootField, "userpodsevictionstatus") && len(parts) > 1 {
		subField := strings.ToLower(parts[1])
		val := reflect.ValueOf(event.UserPodsEvictionStatus)

		return getFieldByName(val, subField)
	}

	return nil
}
