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

package publisher

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"go.opentelemetry.io/otel/attribute"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
	"k8s.io/apimachinery/pkg/util/wait"

	"github.com/nvidia/nvsentinel/commons/pkg/tracing"
	protos "github.com/nvidia/nvsentinel/data-models/pkg/protos"
	"github.com/nvidia/nvsentinel/health-events-analyzer/pkg/config"
)

const (
	maxRetries int           = 5
	delay      time.Duration = 5 * time.Second
)

type PublisherConfig struct {
	platformConnectorClient protos.PlatformConnectorClient
	processingStrategy      protos.ProcessingStrategy
}

func isRetryableError(err error) bool {
	if err == nil {
		return false
	}

	if s, ok := status.FromError(err); ok {
		if s.Code() == codes.Unavailable {
			return true
		}
	}

	return false
}

func (p *PublisherConfig) sendHealthEventWithRetry(ctx context.Context, healthEvents *protos.HealthEvents) error {
	ctx, span := tracing.StartSpan(ctx, "analyzer.grpc.publish")
	defer span.End()

	grpcStart := time.Now()
	attempt := 0

	backoff := wait.Backoff{
		Steps:    maxRetries,
		Duration: delay,
		Factor:   2,
		Jitter:   0.1,
	}

	err := wait.ExponentialBackoff(backoff, func() (bool, error) {
		attempt++

		_, err := p.platformConnectorClient.HealthEventOccurredV1(ctx, healthEvents)
		if err == nil {
			slog.Debug("Successfully sent health events", "events", healthEvents)

			return true, nil
		}

		if isRetryableError(err) {
			slog.Error("Retryable error occurred", "error", err)
			fatalEventPublishingError.WithLabelValues("retryable_error").Inc()

			return false, nil
		}

		slog.Error("Non-retryable error occurred", "error", err)
		fatalEventPublishingError.WithLabelValues("non_retryable_error").Inc()

		return false, fmt.Errorf("non retryable error occurred while sending health event: %w", err)
	})

	durationMs := float64(time.Since(grpcStart).Milliseconds())
	span.SetAttributes(
		attribute.Int("analyzer.grpc.retry_count", attempt),
		attribute.Float64("analyzer.grpc.duration_ms", durationMs),
	)

	if err != nil {
		slog.Error("All retry attempts to send health event failed", "error", err)
		fatalEventPublishingError.WithLabelValues("event_publishing_to_UDS_error").Inc()

		span.SetAttributes(
			attribute.String("analyzer.grpc.status", "failure"),
			attribute.String("analyzer.error.type", "grpc_publish_error"),
			attribute.String("analyzer.error.message", err.Error()),
		)
		tracing.RecordError(span, err)
		tracing.SetOperationStatus(span, tracing.OperationStatusError, "analyzer")

		return fmt.Errorf("all retry attempts to send health event failed: %w", err)
	}

	span.SetAttributes(attribute.String("analyzer.grpc.status", "success"))
	tracing.SetOperationStatus(span, tracing.OperationStatusSuccess, "analyzer")

	return nil
}

func NewPublisher(platformConnectorClient protos.PlatformConnectorClient,
	processingStrategy protos.ProcessingStrategy) *PublisherConfig {
	return &PublisherConfig{
		platformConnectorClient: platformConnectorClient,
		processingStrategy:      processingStrategy,
	}
}

func (p *PublisherConfig) Publish(ctx context.Context, event *protos.HealthEvent,
	recommendedAction protos.RecommendedAction, ruleName string, message string,
	rule *config.HealthEventsAnalyzerRule) error {
	ctx, span := tracing.StartSpan(ctx, "analyzer.publish")
	defer span.End()

	span.SetAttributes(
		attribute.String("analyzer.publish.rule_name", ruleName),
		attribute.String("analyzer.publish.recommended_action", recommendedAction.String()),
	)

	newEvent := proto.Clone(event).(*protos.HealthEvent)

	newEvent.Agent = "health-events-analyzer"
	newEvent.CheckName = ruleName
	newEvent.RecommendedAction = recommendedAction
	newEvent.IsHealthy = false
	newEvent.Message = message

	// Default from module configuration, with an optional rule-level override.
	newEvent.ProcessingStrategy = p.processingStrategy

	if rule != nil && rule.ProcessingStrategy != "" {
		value, ok := protos.ProcessingStrategy_value[rule.ProcessingStrategy]
		if !ok {
			span.SetAttributes(
				attribute.String("analyzer.error.type", "invalid_processing_strategy"),
				attribute.String("analyzer.error.message", fmt.Sprintf("unexpected processingStrategy: %q", rule.ProcessingStrategy)),
			)
			tracing.RecordError(span, fmt.Errorf("unexpected processingStrategy value: %q", rule.ProcessingStrategy))
			tracing.SetOperationStatus(span, tracing.OperationStatusError, "analyzer")

			return fmt.Errorf("unexpected processingStrategy value: %q", rule.ProcessingStrategy)
		}

		newEvent.ProcessingStrategy = protos.ProcessingStrategy(value)
	}

	if recommendedAction == protos.RecommendedAction_NONE {
		newEvent.IsFatal = false
	} else {
		newEvent.IsFatal = true
	}

	span.SetAttributes(
		attribute.Bool("analyzer.publish.is_fatal", newEvent.IsFatal),
		attribute.String("analyzer.publish.processing_strategy", newEvent.ProcessingStrategy.String()),
	)

	req := &protos.HealthEvents{
		Version: 1,
		Events:  []*protos.HealthEvent{newEvent},
	}

	if err := p.sendHealthEventWithRetry(ctx, req); err != nil {
		span.SetAttributes(
			attribute.String("analyzer.error.type", "publish_error"),
			attribute.String("analyzer.error.message", err.Error()),
		)
		tracing.RecordError(span, err)
		tracing.SetOperationStatus(span, tracing.OperationStatusError, "analyzer")

		return err
	}

	span.SetAttributes(attribute.Bool("analyzer.publish.success", true))
	tracing.SetOperationStatus(span, tracing.OperationStatusSuccess, "analyzer")

	return nil
}
