// Copyright 2025
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

package sveltos

import (
	"fmt"
	"time"

	"k8s.io/client-go/tools/record"
	"sigs.k8s.io/controller-runtime/pkg/manager"

	pollerutil "github.com/brongineer/ksm-provider-sveltos/internal/util/poller"
)

// pollInterval is kcm's requeueInterval, which also paced its ClusterSummary poller.
const pollInterval = 10 * time.Second

// SetupPoller wires the ClusterSummary poller and the notifier draining it. The poller starts
// only once registered is closed (see StartAfter).
//
// The channel keeps the poller's default capacity: kcm asked for MaxConcurrentReconciles*10, but
// never set that field, so the default was what it ran with.
func SetupPoller(mgr manager.Manager, recorder record.EventRecorder, registered <-chan struct{}, systemNamespace, providerName string) error {
	poller := pollerutil.NewRunner(
		enqueueClusterSummary(mgr.GetClient(), systemNamespace, providerName),
		pollerutil.WithInterval(pollInterval),
		pollerutil.WithName("poller-cluster-summaries"),
	)
	if err := mgr.Add(StartAfter(registered, poller)); err != nil {
		return fmt.Errorf("failed to add poller: %w", err)
	}
	if err := mgr.Add(&Notifier{Events: poller.GetEventChannel(), Recorder: recorder}); err != nil {
		return fmt.Errorf("failed to add notifier: %w", err)
	}
	return nil
}
