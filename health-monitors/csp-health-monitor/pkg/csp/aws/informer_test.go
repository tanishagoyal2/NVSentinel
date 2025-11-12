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

package aws

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	v1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"
)

func TestNodeInformer(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	k8sClient := fake.NewSimpleClientset()

	node := &v1.Node{
		ObjectMeta: metav1.ObjectMeta{
			Name: testNodeName,
		},
		Spec: v1.NodeSpec{
			ProviderID: "aws:///us-east-1a/" + testInstanceID,
		},
	}

	_, err := k8sClient.CoreV1().Nodes().Create(ctx, node, metav1.CreateOptions{})
	assert.NoError(t, err)

	informer := NewNodeInformer(k8sClient)

	informer.Start(ctx)
	defer informer.Stop()

	node2 := &v1.Node{
		ObjectMeta: metav1.ObjectMeta{
			Name: testNodeName1,
		},
		Spec: v1.NodeSpec{
			ProviderID: "aws:///us-east-1b/" + testInstanceID1,
		},
	}

	_, err = k8sClient.CoreV1().Nodes().Create(ctx, node2, metav1.CreateOptions{})
	assert.NoError(t, err)

	time.Sleep(500 * time.Millisecond)

	nodeName, ok := informer.GetNodeName(testInstanceID)
	assert.True(t, ok, "Instance ID should be found in the map")
	assert.Equal(t, testNodeName, nodeName)

	instanceIDs := informer.GetInstanceIDs()
	assert.Len(t, instanceIDs, 2)
	assert.Equal(t, testNodeName, instanceIDs[testInstanceID])
	assert.Equal(t, testNodeName1, instanceIDs[testInstanceID1])

	err = k8sClient.CoreV1().Nodes().Delete(ctx, node.Name, metav1.DeleteOptions{})
	assert.NoError(t, err)

	time.Sleep(500 * time.Millisecond)

	_, ok = informer.GetNodeName(testInstanceID)
	assert.False(t, ok, "Instance ID should be removed from the map")

	instanceIDs = informer.GetInstanceIDs()
	assert.Len(t, instanceIDs, 1)
	assert.Equal(t, testNodeName1, instanceIDs[testInstanceID1])
}

func TestExtractInstanceID(t *testing.T) {
	tests := []struct {
		name       string
		providerID string
		expected   string
	}{
		{
			name:       "Valid AWS provider ID",
			providerID: "aws:///us-east-1a/i-0123456789abcdef0",
			expected:   "i-0123456789abcdef0",
		},
		{
			name:       "Valid AWS provider ID different region",
			providerID: "aws:///eu-west-1c/i-9876543210fedcba0",
			expected:   "i-9876543210fedcba0",
		},
		{
			name:       "Empty provider ID",
			providerID: "",
			expected:   "",
		},
		{
			name:       "Non-AWS provider ID",
			providerID: "gce://my-project/us-central1-a/instance-1",
			expected:   "",
		},
		{
			name:       "AWS provider ID without proper format",
			providerID: "aws:///us-east-1a/invalid-format",
			expected:   "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			node := &v1.Node{
				ObjectMeta: metav1.ObjectMeta{
					Name: "test-node",
				},
				Spec: v1.NodeSpec{
					ProviderID: tt.providerID,
				},
			}

			result := extractInstanceID(node)
			assert.Equal(t, tt.expected, result)
		})
	}
}
