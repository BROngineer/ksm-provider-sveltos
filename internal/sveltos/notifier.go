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
	"context"

	kcmv1 "github.com/K0rdent/kcm/api/v1beta1"
	"k8s.io/client-go/tools/record"
	"sigs.k8s.io/controller-runtime/pkg/event"
	"sigs.k8s.io/controller-runtime/pkg/manager"

	"github.com/brongineer/ksm-provider-api/notify"
)

// Notifier turns the poller's events into state-change notifications for kcm. In kcm the same
// events fed the ServiceSet controller through a channel source.
type Notifier struct {
	Events   <-chan event.TypedGenericEvent[*kcmv1.ServiceSet]
	Recorder record.EventRecorder
}

// Start drains Events until ctx is done or the channel is closed (the poller stopped).
func (n *Notifier) Start(ctx context.Context) error {
	for {
		select {
		case <-ctx.Done():
			return nil
		case ev, ok := <-n.Events:
			if !ok {
				return nil
			}
			notify.StateChanged(n.Recorder, ev.Object)
		}
	}
}

// NeedLeaderElection keeps notifications on the leader, which also runs the poller.
func (*Notifier) NeedLeaderElection() bool {
	return true
}

// StartAfter delays r until gate is closed. The poller must not tick before the provider is
// registered: its first tick marks every ServiceSet as changed, and notifications emitted then
// reach a registry that does not know the provider yet.
func StartAfter(gate <-chan struct{}, r manager.Runnable) manager.Runnable {
	return &gatedRunnable{gate: gate, inner: r}
}

type gatedRunnable struct {
	inner manager.Runnable
	gate  <-chan struct{}
}

func (g *gatedRunnable) Start(ctx context.Context) error {
	select {
	case <-ctx.Done():
		return nil
	case <-g.gate:
	}
	return g.inner.Start(ctx)
}

// NeedLeaderElection is true: the gated runnables (poller, notifier) are leader-only.
func (*gatedRunnable) NeedLeaderElection() bool {
	return true
}
