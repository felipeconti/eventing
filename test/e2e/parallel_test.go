// +build e2e

/*
Copyright 2019 The Knative Authors
Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package e2e

import (
	"encoding/json"
	"fmt"
	"testing"

	"k8s.io/apimachinery/pkg/util/uuid"
	duckv1 "knative.dev/pkg/apis/duck/v1"

	"knative.dev/eventing/test/lib"
	"knative.dev/eventing/test/lib/cloudevents"
	"knative.dev/eventing/test/lib/resources"

	eventingduckv1alpha1 "knative.dev/eventing/pkg/apis/duck/v1alpha1"
	"knative.dev/eventing/pkg/apis/flows/v1alpha1"
	eventingtesting "knative.dev/eventing/pkg/reconciler/testing"
)

type branchConfig struct {
	filter bool
}

func TestFlowsParallel(t *testing.T) {
	const (
		senderPodName = "e2e-parallel"
	)
	table := []struct {
		name           string
		branchesConfig []branchConfig
		expected       string
	}{
		{
			name: "two-branches-pass-first-branch-only",
			branchesConfig: []branchConfig{
				{filter: false},
				{filter: true},
			},
			expected: "parallel-two-branches-pass-first-branch-only-branch-0-sub",
		},
	}
	channelTypeMeta := &lib.DefaultChannel

	client := setup(t, true)
	defer tearDown(client)

	for _, tc := range table {
		parallelBranches := make([]v1alpha1.ParallelBranch, len(tc.branchesConfig))
		for branchNumber, cse := range tc.branchesConfig {
			// construct filter services
			filterPodName := fmt.Sprintf("parallel-%s-branch-%d-filter", tc.name, branchNumber)
			filterPod := resources.EventFilteringPod(filterPodName, cse.filter)
			client.CreatePodOrFail(filterPod, lib.WithService(filterPodName))

			// construct branch subscriber
			subPodName := fmt.Sprintf("parallel-%s-branch-%d-sub", tc.name, branchNumber)
			subPod := resources.SequenceStepperPod(subPodName, subPodName)
			client.CreatePodOrFail(subPod, lib.WithService(subPodName))

			parallelBranches[branchNumber] = v1alpha1.ParallelBranch{
				Filter: &duckv1.Destination{
					Ref: resources.KnativeRefForService(filterPodName, client.Namespace),
				},
				Subscriber: duckv1.Destination{
					Ref: resources.KnativeRefForService(subPodName, client.Namespace),
				},
			}
		}

		channelTemplate := &eventingduckv1alpha1.ChannelTemplateSpec{
			TypeMeta: *(channelTypeMeta),
		}

		// create logger service for global reply
		loggerPodName := fmt.Sprintf("%s-logger", tc.name)
		loggerPod := resources.EventLoggerPod(loggerPodName)
		client.CreatePodOrFail(loggerPod, lib.WithService(loggerPodName))

		// create channel as reply of the Parallel
		// TODO(chizhg): now we'll have to use a channel plus its subscription here, as reply of the Subscription
		//                must be Addressable.
		replyChannelName := fmt.Sprintf("reply-%s", tc.name)
		client.CreateChannelOrFail(replyChannelName, channelTypeMeta)
		replySubscriptionName := fmt.Sprintf("reply-%s", tc.name)
		client.CreateSubscriptionOrFail(
			replySubscriptionName,
			replyChannelName,
			channelTypeMeta,
			resources.WithSubscriberForSubscription(loggerPodName),
		)

		parallel := eventingtesting.NewFlowsParallel(tc.name, client.Namespace,
			eventingtesting.WithFlowsParallelChannelTemplateSpec(channelTemplate),
			eventingtesting.WithFlowsParallelBranches(parallelBranches),
			eventingtesting.WithFlowsParallelReply(&duckv1.Destination{Ref: &duckv1.KReference{Kind: channelTypeMeta.Kind, APIVersion: channelTypeMeta.APIVersion, Name: replyChannelName, Namespace: client.Namespace}}))

		client.CreateFlowsParallelOrFail(parallel)

		client.WaitForAllTestResourcesReadyOrFail()

		// send fake CloudEvent to the Parallel
		msg := fmt.Sprintf("TestFlowParallel %s - ", uuid.NewUUID())
		// NOTE: the eventData format must be BaseData, as it needs to be correctly parsed in the stepper service.
		eventData := cloudevents.BaseData{Message: msg}
		eventDataBytes, err := json.Marshal(eventData)
		if err != nil {
			t.Fatalf("Failed to convert %v to json: %v", eventData, err)
		}
		event := cloudevents.New(
			string(eventDataBytes),
			cloudevents.WithSource(senderPodName),
		)
		client.SendFakeEventToAddressableOrFail(
			senderPodName,
			tc.name,
			lib.FlowsParallelTypeMeta,
			event)

		// verify the logger service receives the correct transformed event
		if err := client.CheckLog(loggerPodName, lib.CheckerContains(tc.expected)); err != nil {
			t.Fatalf("String %q not found in logs of logger pod %q: %v", tc.expected, loggerPodName, err)
		}
	}

}
