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
	"errors"
	"testing"
	"time"

	kcmv1 "github.com/K0rdent/kcm/api/v1beta1"
	addoncontrollerv1beta1 "github.com/projectsveltos/addon-controller/api/v1beta1"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/brongineer/ksm-provider-api/api/provider"
	providerserver "github.com/brongineer/ksm-provider-api/provider/server"
	"github.com/brongineer/ksm-provider-api/providertest"
)

func storedServiceSet(name string, generation int64, services ...kcmv1.ServiceWithValues) *kcmv1.ServiceSet {
	ss := serviceSetFor(services...)
	ss.Name = name
	ss.UID = ""
	ss.Generation = generation
	return ss
}

func deployRequest(ss *kcmv1.ServiceSet) provider.DeployServicesRequest {
	return provider.DeployServicesRequest{Name: ss.Name, Namespace: ss.Namespace, Generation: ss.Generation}
}

func TestContract(t *testing.T) {
	var current client.Client
	create := func(t *testing.T, ss *kcmv1.ServiceSet) {
		t.Helper()
		require.NoError(t, current.Create(context.Background(), ss))
	}
	kustSvc := kcmv1.ServiceWithValues{Name: "kust-svc", Namespace: testNamespace, Template: "kust-tmpl"}

	providertest.RunContract(t, providertest.Scenarios{
		NewProvider: func(t *testing.T) providerserver.Provider {
			a, c := newTestAdapter(t, append(clusterDeploymentObjects(), kustomizeTemplate("kust-tmpl"))...)
			current = c
			return a
		},
		Deployable: func(t *testing.T) provider.DeployServicesRequest {
			ss := storedServiceSet("deployable", 3, kustSvc)
			create(t, ss)
			return deployRequest(ss)
		},
		Failing: func(t *testing.T) provider.DeployServicesRequest {
			ss := storedServiceSet("failing", 1, kcmv1.ServiceWithValues{Name: "nginx", Namespace: "ingress", Template: "missing-tmpl"})
			create(t, ss)
			return deployRequest(ss)
		},
		Missing: func(*testing.T) provider.DeployServicesRequest {
			return provider.DeployServicesRequest{Name: "does-not-exist", Namespace: testNamespace, Generation: 1}
		},
		Stale: func(t *testing.T) provider.DeployServicesRequest {
			ss := storedServiceSet("stale", 2, kustSvc)
			create(t, ss)
			req := deployRequest(ss)
			req.Generation = 3
			return req
		},
		DeleteTimeout: 2 * time.Second,
	})
}

func TestNewAdapter(t *testing.T) {
	_, c := newTestAdapter(t)

	a := NewAdapter(c, testSystemNamespace)

	assert.Same(t, c, a.Client)
	assert.Equal(t, testSystemNamespace, a.SystemNamespace)
	require.NotNil(t, a.timeFunc)
}

func TestFailureReason(t *testing.T) {
	tests := []struct {
		conditionReason string
		want            provider.DeployServicesResultReason
	}{
		{kcmv1.ServiceSetProfileBuildFailedReason, provider.DeployServicesResultReasonInvalidInput},
		{kcmv1.ServiceSetHelmChartsBuildFailedReason, provider.DeployServicesResultReasonSourceUnavailable},
		{kcmv1.ServiceSetKustomizationRefsBuildFailedReason, provider.DeployServicesResultReasonSourceUnavailable},
		{kcmv1.ServiceSetPolicyRefsBuildFailedReason, provider.DeployServicesResultReasonSourceUnavailable},
		{kcmv1.ServiceSetProfileNotReadyReason, provider.DeployServicesResultReasonProviderError},
		{"SomethingElse", provider.DeployServicesResultReasonProviderError},
	}
	for _, tt := range tests {
		t.Run(tt.conditionReason, func(t *testing.T) {
			assert.Equal(t, tt.want, failureReason(tt.conditionReason))
		})
	}
}

func TestToServiceStates(t *testing.T) {
	transition := metav1.NewTime(testNow)
	states := []kcmv1.ServiceState{
		{
			Name: "nginx", Namespace: "ingress", Template: "nginx-tmpl",
			Type: kcmv1.ServiceTypeHelm, State: kcmv1.ServiceStateFailed,
			Version: "4.11.0", FailureMessage: "boom", LastDeployedHash: "old-hash",
			LastStateTransitionTime: &transition,
		},
		{Name: "kust", Namespace: testNamespace, Template: "kust-tmpl", Type: kcmv1.ServiceTypeKustomize, State: kcmv1.ServiceStateNotDeployed},
	}
	hashes := map[client.ObjectKey]string{{Namespace: "ingress", Name: "nginx"}: "new-hash"}

	got := toServiceStates(states, hashes)

	require.Len(t, got, 2)
	nginx := got[0]
	assert.Equal(t, "nginx", nginx.Name)
	assert.Equal(t, "ingress", nginx.Namespace)
	assert.Equal(t, "nginx-tmpl", nginx.Template)
	assert.Equal(t, provider.ServiceStateTypeHelm, nginx.Type)
	assert.Equal(t, provider.ServiceStateStateFailed, nginx.State)
	require.NotNil(t, nginx.Version)
	assert.Equal(t, "4.11.0", *nginx.Version)
	require.NotNil(t, nginx.FailureMessage)
	assert.Equal(t, "boom", *nginx.FailureMessage)
	require.NotNil(t, nginx.LastDeployedHash)
	assert.Equal(t, "old-hash", *nginx.LastDeployedHash)
	require.NotNil(t, nginx.DeployedHash)
	assert.Equal(t, "new-hash", *nginx.DeployedHash)
	require.NotNil(t, nginx.LastStateTransitionTime)
	assert.True(t, testNow.Equal(*nginx.LastStateTransitionTime))

	kust := got[1]
	assert.Equal(t, provider.ServiceStateStatePending, kust.State)
	assert.Nil(t, kust.Version, "empty values are omitted")
	assert.Nil(t, kust.FailureMessage)
	assert.Nil(t, kust.LastDeployedHash)
	assert.Nil(t, kust.DeployedHash)
	assert.Nil(t, kust.LastStateTransitionTime)

	assert.NotNil(t, toServiceStates(nil, nil), "services is a required array, never null")
}

func TestDeployServicesReportsProfileFailure(t *testing.T) {
	ss := storedServiceSet("no-cluster", 1, kcmv1.ServiceWithValues{Name: "kust-svc", Namespace: testNamespace, Template: "kust-tmpl"})
	a, _ := newTestAdapter(t, kustomizeTemplate("kust-tmpl"), ss, &kcmv1.Credential{
		Name: testCredential, Namespace: testNamespace,
	})
	// The regional client resolves through the ClusterDeployment, so make it resolvable while the
	// Profile build still fails: the cluster reference (CAPI Cluster / SveltosCluster) is missing.
	require.NoError(t, a.Create(context.Background(), &kcmv1.ClusterDeployment{
		Name: testCluster, Namespace: testNamespace,
		Spec: kcmv1.ClusterDeploymentSpec{Template: "sample-template", Credential: testCredential},
	}))

	res, err := a.DeployServices(context.Background(), deployRequest(ss))

	require.NoError(t, err, "a deployment failure is a result, not an error")
	assert.False(t, res.Applied)
	require.NotNil(t, res.Reason)
	assert.Equal(t, provider.DeployServicesResultReasonProviderError, *res.Reason)
	require.NotNil(t, res.Message)
	assert.Equal(t, kcmv1.ServiceSetProfileNotReadyMessage, *res.Message)
	assert.False(t, res.Collected)
	assert.Equal(t, int64(1), res.ObservedGeneration)
}

func TestDeployServicesReportsSourceFailureWithMessage(t *testing.T) {
	ss := storedServiceSet("missing-tmpl", 1, kcmv1.ServiceWithValues{Name: "nginx", Namespace: "ingress", Template: "missing-tmpl"})
	a, _ := newTestAdapter(t, append(clusterDeploymentObjects(), ss)...)

	res, err := a.DeployServices(context.Background(), deployRequest(ss))

	require.NoError(t, err)
	assert.False(t, res.Applied)
	require.NotNil(t, res.Reason)
	assert.Equal(t, provider.DeployServicesResultReasonSourceUnavailable, *res.Reason)
	require.NotNil(t, res.Message)
	assert.Contains(t, *res.Message, "missing-tmpl")
}

func TestDeployServicesReportsCollectionFailure(t *testing.T) {
	ss := storedServiceSet("cd-1-abcd", 1, kcmv1.ServiceWithValues{Name: "kust-svc", Namespace: testNamespace, Template: "kust-tmpl"})
	// A Profile matching a cluster whose ClusterSummary does not exist yet.
	profile := profileFor(ss, capiClusterRef())
	a, _ := newTestAdapter(t, append(clusterDeploymentObjects(), kustomizeTemplate("kust-tmpl"), ss, profile)...)

	res, err := a.DeployServices(context.Background(), deployRequest(ss))

	require.NoError(t, err)
	assert.True(t, res.Applied)
	assert.False(t, res.Collected)
	require.NotNil(t, res.CollectMessage)
	assert.Equal(t, kcmv1.ServiceSetStatusesNotCollectedMessage, *res.CollectMessage)
}

func TestDeployServicesFingerprintsDeployedCharts(t *testing.T) {
	isolateClusterConfigCache(t)
	ss := storedServiceSet("cd-1-abcd", 2, kcmv1.ServiceWithValues{Name: "nginx", Namespace: "ingress", Template: "nginx-tmpl"})
	profile := profileFor(ss, capiClusterRef())
	profile.UID = testProfileUID
	objs := append(clusterDeploymentObjects(), helmTemplateObjects("nginx-tmpl")...)
	objs = append(objs, ss, profile, deployedHelmSummary(ss), ownedClusterConfiguration(ss))
	a, _ := newTestAdapter(t, objs...)

	res, err := a.DeployServices(context.Background(), deployRequest(ss))

	require.NoError(t, err)
	assert.True(t, res.Applied)
	assert.True(t, res.Collected)
	assert.Equal(t, int64(2), res.ObservedGeneration)
	require.Len(t, res.Services, 1)
	assert.Equal(t, provider.ServiceStateStateDeployed, res.Services[0].State)
	require.NotNil(t, res.Services[0].DeployedHash)
	assert.NotEmpty(t, *res.Services[0].DeployedHash)
}

func TestDeployServicesLookupErrors(t *testing.T) {
	t.Run("missing ServiceSet", func(t *testing.T) {
		a, _ := newTestAdapter(t)

		_, err := a.DeployServices(context.Background(), provider.DeployServicesRequest{Name: "x", Namespace: testNamespace, Generation: 1})
		assert.True(t, errors.Is(err, providerserver.ErrNotFound), "err = %v", err)
	})

	t.Run("stale read", func(t *testing.T) {
		ss := storedServiceSet("cd-1-abcd", 1)
		a, _ := newTestAdapter(t, ss)

		req := deployRequest(ss)
		req.Generation = 2
		_, err := a.DeployServices(context.Background(), req)
		assert.True(t, errors.Is(err, providerserver.ErrStaleGeneration), "err = %v", err)
	})

	t.Run("newer generation than requested is fine", func(t *testing.T) {
		ss := storedServiceSet("cd-1-abcd", 5, kcmv1.ServiceWithValues{Name: "kust-svc", Namespace: testNamespace, Template: "kust-tmpl"})
		a, _ := newTestAdapter(t, append(clusterDeploymentObjects(), kustomizeTemplate("kust-tmpl"), ss)...)

		req := deployRequest(ss)
		req.Generation = 4
		res, err := a.DeployServices(context.Background(), req)
		require.NoError(t, err)
		assert.Equal(t, int64(5), res.ObservedGeneration, "observedGeneration is what the adapter acted on")
	})

	t.Run("regional client failure is an error", func(t *testing.T) {
		ss := storedServiceSet("cd-1-abcd", 1)
		a, _ := newTestAdapter(t, ss)

		_, err := a.DeployServices(context.Background(), deployRequest(ss))
		assert.Error(t, err, "today this fails before any condition is set")
	})
}

func TestDeleteServicesProgression(t *testing.T) {
	tests := []struct {
		name           string
		selfManagement bool
		profile        func(ss *kcmv1.ServiceSet) client.Object
	}{
		{
			name:    "Profile",
			profile: func(ss *kcmv1.ServiceSet) client.Object { return profileFor(ss) },
		},
		{
			name:           "ClusterProfile",
			selfManagement: true,
			profile: func(ss *kcmv1.ServiceSet) client.Object {
				return &addoncontrollerv1beta1.ClusterProfile{Name: ss.Name}
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ss := storedServiceSet("cd-1-abcd", 1)
			if tt.selfManagement {
				ss.Spec.Cluster = ""
				ss.Spec.Provider.SelfManagement = true
			}
			a, c := newTestAdapter(t, append(clusterDeploymentObjects(), ss, tt.profile(ss))...)
			req := provider.DeleteServicesRequest{Name: ss.Name, Namespace: ss.Namespace}

			res, err := a.DeleteServices(context.Background(), req)
			require.NoError(t, err)
			assert.False(t, res.Done, "the Profile was just deleted")

			err = c.Get(context.Background(), client.ObjectKeyFromObject(tt.profile(ss)), tt.profile(ss))
			assert.True(t, apierrors.IsNotFound(err), "Profile must be deleted, got %v", err)

			for range 2 {
				res, err = a.DeleteServices(context.Background(), req)
				require.NoError(t, err)
				assert.True(t, res.Done)
			}
		})
	}
}

func TestDeleteServicesWaitsForProfileWithFinalizer(t *testing.T) {
	ss := storedServiceSet("cd-1-abcd", 1)
	profile := profileFor(ss)
	profile.Finalizers = []string{"projectsveltos.io/finalizer"}
	a, _ := newTestAdapter(t, append(clusterDeploymentObjects(), ss, profile)...)
	req := provider.DeleteServicesRequest{Name: ss.Name, Namespace: ss.Namespace}

	for range 2 {
		res, err := a.DeleteServices(context.Background(), req)
		require.NoError(t, err)
		assert.False(t, res.Done, "a Profile still being deleted is not done")
	}
}

func TestDeleteServicesMissingServiceSet(t *testing.T) {
	a, _ := newTestAdapter(t)

	_, err := a.DeleteServices(context.Background(), provider.DeleteServicesRequest{Name: "x", Namespace: testNamespace})
	assert.Error(t, err)
}
