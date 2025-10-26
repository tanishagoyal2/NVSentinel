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

package helpers

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"

	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/mongo"
	"go.mongodb.org/mongo-driver/mongo/options"
	"k8s.io/klog/v2"
	"sigs.k8s.io/e2e-framework/pkg/envconf"
)

// SendHealthEventsToNodes sends health events from the specified `eventFilePath` to all nodes listed in `nodeNames` concurrently.
func SendHealthEventsToNodes(nodeNames []string, errorCode string, eventFilePath string) error {
	eventData, err := os.ReadFile(eventFilePath)
	if err != nil {
		return fmt.Errorf("failed to read health event file %s: %w", eventFilePath, err)
	}

	client := &http.Client{
		Timeout: 10 * time.Second,
	}

	var wg sync.WaitGroup
	var mu sync.Mutex
	var errs []error

	for _, nodeName := range nodeNames {
		wg.Add(1)
		go func(nodeName string) {
			defer wg.Done()
			klog.Infof("Sending health event to node %s with error code %s", nodeName, errorCode)

			eventJSON := strings.ReplaceAll(string(eventData), "NODE_NAME", nodeName)
			eventJSON = strings.ReplaceAll(eventJSON, "ERROR_CODE", errorCode)

			resp, err := client.Post("http://localhost:8080/health-event", "application/json", strings.NewReader(eventJSON))
			if err != nil {
				mu.Lock()
				defer mu.Unlock()
				errs = append(errs, fmt.Errorf("failed to send health event to node %s: %w", nodeName, err))
				return
			}
			defer resp.Body.Close()

			if resp.StatusCode != http.StatusOK {
				body, _ := io.ReadAll(resp.Body)
				mu.Lock()
				defer mu.Unlock()
				errs = append(errs, fmt.Errorf("health event to node %s failed: expected status 200, got %d. Response: %s", nodeName, resp.StatusCode, string(body)))
			}
		}(nodeName)
	}

	wg.Wait()

	return errors.Join(errs...)
}

const (
	MONGODB_DATABASE_NAME   = "HealthEventsDatabase"
	MONGODB_COLLECTION_NAME = "HealthEvents"
)

func getMongoCollection(ctx context.Context) (*mongo.Client, *mongo.Collection, error) {
	// Use environment variable if set, otherwise default to localhost for port-forward
	mongoURI := os.Getenv("MONGODB_URI")
	if mongoURI == "" {
		mongoURI = "mongodb://localhost:27017/HealthEventsDatabase?authMechanism=MONGODB-X509&authSource=$external&tls=true&directConnection=true"
	}

	// Build TLS config
	tlsConfig := &tls.Config{
		MinVersion: tls.VersionTLS12,
		ServerName: "nvsentinel-mongodb-0.nvsentinel-mongodb-headless.nvsentinel.svc.cluster.local",
	}

	// Allow skipping verification for test environments
	if strings.EqualFold(os.Getenv("MONGODB_TLS_SKIP_VERIFY"), "true") {
		tlsConfig.InsecureSkipVerify = true // nolint: gosec – intentional in test helpers
		tlsConfig.ServerName = ""           // Don't validate server name when skipping verification
	}

	// Load client certificates from filesystem if available
	certFile := os.Getenv("MONGODB_TLS_CERT_FILE")
	keyFile := os.Getenv("MONGODB_TLS_KEY_FILE")
	caFile := os.Getenv("MONGODB_TLS_CA_FILE")

	if certFile == "" {
		certFile = "/tmp/mongo-certs/tls.crt"
	}
	if keyFile == "" {
		keyFile = "/tmp/mongo-certs/tls.key"
	}
	if caFile == "" {
		caFile = "/tmp/mongo-certs/ca.crt"
	}

	// Load CA certificate
	if _, err := os.Stat(caFile); err == nil {
		caCert, err := os.ReadFile(caFile)
		if err != nil {
			if !tlsConfig.InsecureSkipVerify {
				return nil, nil, fmt.Errorf("failed to read CA certificate: %w", err)
			}
		} else {
			caCertPool := x509.NewCertPool()
			if !caCertPool.AppendCertsFromPEM(caCert) {
				if !tlsConfig.InsecureSkipVerify {
					return nil, nil, fmt.Errorf("failed to append CA certificate to pool")
				}
			}
			tlsConfig.RootCAs = caCertPool
		}
	}

	// Load client certificate and key
	if _, err := os.Stat(certFile); err == nil {
		if _, err := os.Stat(keyFile); err == nil {
			cert, err := tls.LoadX509KeyPair(certFile, keyFile)
			if err != nil {
				return nil, nil, fmt.Errorf("load client cert: %w", err)
			}
			tlsConfig.Certificates = []tls.Certificate{cert}
		}
	}

	clientOpts := options.Client().ApplyURI(mongoURI).SetTLSConfig(tlsConfig)
	// Use X509 auth by default
	clientOpts.SetAuth(options.Credential{AuthMechanism: "MONGODB-X509", AuthSource: "$external"})

	client, err := mongo.Connect(ctx, clientOpts)
	if err != nil {
		return nil, nil, fmt.Errorf("connect mongo: %w", err)
	}

	// Ping to ensure connectivity
	if err := client.Ping(ctx, nil); err != nil {
		_ = client.Disconnect(ctx)
		return nil, nil, fmt.Errorf("ping mongo: %w", err)
	}

	collection := client.Database(MONGODB_DATABASE_NAME).Collection(MONGODB_COLLECTION_NAME)
	return client, collection, nil
}

// QueryHealthEventByFilter queries MongoDB for the most recent health-event document matching the filter.
func QueryHealthEventByFilter(ctx context.Context, filter bson.M) (map[string]interface{}, error) {
	client, collection, err := getMongoCollection(ctx)
	if err != nil {
		return nil, err
	}
	defer client.Disconnect(ctx) // nolint:errcheck

	opts := options.FindOne().SetSort(bson.D{{Key: "_id", Value: -1}})
	var result map[string]interface{}
	if err := collection.FindOne(ctx, filter, opts).Decode(&result); err != nil {
		return nil, fmt.Errorf("find event: %w", err)
	}
	return result, nil
}

// NOTE: this functionality is added specifically to remove node conditions added by health event analyzer
// because currently health event analyzer doesn't send healthy event for the corresponding fatal event.
func TestCleanUp(ctx context.Context, GpuNodeName string, nodeCondition string, errorCode string, c *envconf.Config) error {
	// Send healthy event to clear the error
	err := SendHealthEventsToNodes([]string{GpuNodeName}, errorCode, "data/healthy-event.json")
	if err != nil {
		klog.Errorf("failed to send healthy events during cleanup: %v", err)
	}
	time.Sleep(5 * time.Second)

	// Clean up node condition and uncordon
	client, err := c.NewClient()
	if err != nil {
		klog.Errorf("failed to create client during cleanup: %v", err)
		return fmt.Errorf("failed to create client during cleanup: %w", err)
	}

	err = CleanupNodeConditionAndUncordon(ctx, client, GpuNodeName, nodeCondition)
	if err != nil {
		klog.Errorf("failed to cleanup node condition and uncordon node %s: %v", GpuNodeName, err)
		return fmt.Errorf("failed to cleanup node condition and uncordon node %s: %w", GpuNodeName, err)
	}

	klog.Infof("Successfully cleaned up node condition and uncordoned node %s", GpuNodeName)

	return nil
}
