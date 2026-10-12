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
	"strings"
	"sync/atomic"
	"testing"
	"time"

	kcmv1 "github.com/K0rdent/kcm/api/v1beta1"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
	"k8s.io/client-go/tools/record"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/event"

	"github.com/brongineer/ksm-provider-api/notify"
)

const waitTimeout = 5 * time.Second

func isolateEnqueueStates(t *testing.T) {
	t.Helper()
	enqueueStates.Lock()
	original := enqueueStates.entries
	enqueueStates.entries = make(map[client.ObjectKey]*enqueueState)
	enqueueStates.Unlock()
	t.Cleanup(func() {
		enqueueStates.Lock()
		enqueueStates.entries = original
		enqueueStates.Unlock()
	})
}

func TestEnqueueClusterSummaryPollsOnlyOwnProvider(t *testing.T) {
	isolateEnqueueStates(t)
	own := helmServiceSet()
	other := helmServiceSet()
	other.Name = "other-provider"
	other.Spec.Provider.Name = "ksm-argocd"
	a, c := newTestAdapter(t, append(clusterDeploymentObjects(),
		own, profileFor(own, capiClusterRef()), deployedHelmSummary(own),
		other, profileFor(other, capiClusterRef()), deployedHelmSummary(other),
	)...)

	got, err := enqueueClusterSummary(c, a.SystemNamespace, "ksm-projectsveltos")(context.Background())

	require.NoError(t, err)
	require.Len(t, got, 1)
	assert.Equal(t, own.Name, got[0].Name)
}

func TestEnqueueClusterSummarySkipsServiceSetsWithoutArtifacts(t *testing.T) {
	isolateEnqueueStates(t)
	withoutProfile := helmServiceSet()
	withoutProfile.Name = "no-profile"
	withoutSummary := helmServiceSet()
	withoutSummary.Name = "no-summary"
	a, c := newTestAdapter(t, append(clusterDeploymentObjects(),
		withoutProfile,
		withoutSummary, profileFor(withoutSummary, capiClusterRef()),
	)...)

	got, err := enqueueClusterSummary(c, a.SystemNamespace, "ksm-projectsveltos")(context.Background())

	require.NoError(t, err)
	assert.Empty(t, got)
}

func TestEnqueueClusterSummaryEnqueuesOnceUntilSummaryChanges(t *testing.T) {
	isolateEnqueueStates(t)
	ss := helmServiceSet()
	ss.Status.Deployed = true
	ss.Status.Services[0].State = kcmv1.ServiceStateDeployed
	a, c := newTestAdapter(t, append(clusterDeploymentObjects(), ss, profileFor(ss, capiClusterRef()), deployedHelmSummary(ss))...)
	poll := enqueueClusterSummary(c, a.SystemNamespace, "ksm-projectsveltos")

	first, err := poll(context.Background())
	require.NoError(t, err)
	assert.Len(t, first, 1, "a newly seen ClusterSummary is enqueued")

	second, err := poll(context.Background())
	require.NoError(t, err)
	assert.Empty(t, second, "a fully deployed ServiceSet with an unchanged summary is quiescent")
}

func TestNotifierEmitsStateChangedPerEvent(t *testing.T) {
	events := make(chan event.TypedGenericEvent[*kcmv1.ServiceSet], 2)
	rec := record.NewFakeRecorder(4)
	n := &Notifier{Events: events, Recorder: rec}
	done := make(chan error, 1)
	go func() { done <- n.Start(context.Background()) }()

	events <- event.TypedGenericEvent[*kcmv1.ServiceSet]{Object: helmServiceSet()}
	events <- event.TypedGenericEvent[*kcmv1.ServiceSet]{Object: helmServiceSet()}
	for range 2 {
		select {
		case got := <-rec.Events:
			assert.True(t, strings.Contains(got, notify.Reason), "event %q, want reason %s", got, notify.Reason)
		case <-time.After(waitTimeout):
			t.Fatal("no notification emitted")
		}
	}

	close(events)
	select {
	case err := <-done:
		assert.NoError(t, err, "a closed channel means the poller stopped")
	case <-time.After(waitTimeout):
		t.Fatal("Start did not return after the channel closed")
	}
}

func TestNotifierStopsOnContextCancel(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	n := &Notifier{Events: make(chan event.TypedGenericEvent[*kcmv1.ServiceSet]), Recorder: record.NewFakeRecorder(1)}
	done := make(chan error, 1)
	go func() { done <- n.Start(ctx) }()

	cancel()
	select {
	case err := <-done:
		assert.NoError(t, err)
	case <-time.After(waitTimeout):
		t.Fatal("Start did not return after cancel")
	}
	assert.True(t, n.NeedLeaderElection())
}

type countingRunnable struct {
	started atomic.Int32
}

func (r *countingRunnable) Start(context.Context) error {
	r.started.Add(1)
	return nil
}

func TestStartAfterWaitsForGate(t *testing.T) {
	gate := make(chan struct{})
	inner := &countingRunnable{}
	r := StartAfter(gate, inner)
	done := make(chan error, 1)
	go func() { done <- r.Start(context.Background()) }()

	time.Sleep(50 * time.Millisecond)
	assert.Zero(t, inner.started.Load(), "started before the gate opened")

	close(gate)
	select {
	case err := <-done:
		require.NoError(t, err)
	case <-time.After(waitTimeout):
		t.Fatal("inner runnable did not start after the gate opened")
	}
	assert.Equal(t, int32(1), inner.started.Load())
}

func TestStartAfterReturnsOnCancelWithoutStarting(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	inner := &countingRunnable{}
	r := StartAfter(make(chan struct{}), inner)
	cancel()

	require.NoError(t, r.Start(ctx))
	assert.Zero(t, inner.started.Load())
}

func TestClusterReconcilerKubeconfigFromToken(t *testing.T) {
	r := &ClusterReconciler{config: &rest.Config{Host: "https://kcm.example:6443"}}

	raw := r.getKubeconfigFromToken("projectsveltos", "sveltos-sa", "token-123", []byte("ca-pem"))

	cfg, err := clientcmd.Load([]byte(raw))
	require.NoError(t, err)
	ctxCfg := cfg.Contexts[cfg.CurrentContext]
	require.NotNil(t, ctxCfg)
	assert.Equal(t, "projectsveltos", ctxCfg.Namespace)
	assert.Equal(t, "https://kcm.example:6443", cfg.Clusters[ctxCfg.Cluster].Server)
	assert.Equal(t, []byte("ca-pem"), cfg.Clusters[ctxCfg.Cluster].CertificateAuthorityData)
	assert.Equal(t, "token-123", cfg.AuthInfos[ctxCfg.AuthInfo].Token)
}

func TestClusterReconcilerIgnoresMissingSveltosCluster(t *testing.T) {
	_, c := newTestAdapter(t)
	r := &ClusterReconciler{Client: c}

	res, err := r.Reconcile(context.Background(), ctrl.Request{Namespace: "mgmt", Name: "mgmt"})

	require.NoError(t, err)
	assert.Equal(t, ctrl.Result{}, res)
}
