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
	"testing"

	kcmv1 "github.com/K0rdent/kcm/api/v1beta1"
	addoncontrollerv1beta1 "github.com/projectsveltos/addon-controller/api/v1beta1"
	"github.com/projectsveltos/addon-controller/lib/clusterops"
	libsveltosv1beta1 "github.com/projectsveltos/libsveltos/api/v1beta1"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	apimeta "k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	clusterapiv1 "sigs.k8s.io/cluster-api/api/core/v1beta2"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

func helmServiceSet() *kcmv1.ServiceSet {
	ss := serviceSetFor(kcmv1.ServiceWithValues{Name: "nginx", Namespace: "ingress", Template: "nginx-tmpl"})
	ss.Status.Services = []kcmv1.ServiceState{{
		Name: "nginx", Namespace: "ingress", Template: "nginx-tmpl",
		Type: kcmv1.ServiceTypeHelm, State: kcmv1.ServiceStateProvisioning,
	}}
	return ss
}

func profileFor(ss *kcmv1.ServiceSet, matching ...corev1.ObjectReference) *addoncontrollerv1beta1.Profile {
	return &addoncontrollerv1beta1.Profile{
		Name: ss.Name, Namespace: ss.Namespace,
		Status: addoncontrollerv1beta1.Status{MatchingClusterRefs: matching},
	}
}

func capiClusterRef() corev1.ObjectReference {
	return corev1.ObjectReference{
		Kind:       clusterapiv1.ClusterKind,
		Namespace:  testNamespace,
		Name:       testCluster,
		APIVersion: clusterapiv1.GroupVersion.String(),
	}
}

func deployedHelmSummary(ss *kcmv1.ServiceSet) *addoncontrollerv1beta1.ClusterSummary {
	name := clusterops.GetClusterSummaryName(addoncontrollerv1beta1.ProfileKind, ss.Name, testCluster, false)
	return &addoncontrollerv1beta1.ClusterSummary{
		Name: name, Namespace: testNamespace,
		Status: addoncontrollerv1beta1.ClusterSummaryStatus{
			FeatureSummaries: []addoncontrollerv1beta1.FeatureSummary{{
				FeatureID: libsveltosv1beta1.FeatureHelm,
				Status:    libsveltosv1beta1.FeatureStatusProvisioned,
			}},
			HelmReleaseSummaries: []addoncontrollerv1beta1.HelmChartSummary{{
				ReleaseName:      "nginx",
				ReleaseNamespace: "ingress",
				Status:           addoncontrollerv1beta1.HelmChartStatusManaging,
				ValuesHash:       []byte("values"),
			}},
		},
	}
}

func TestCollectServiceStatusesFromClusterSummary(t *testing.T) {
	ss := helmServiceSet()
	a, c := newTestAdapter(t, profileFor(ss, capiClusterRef()), deployedHelmSummary(ss))

	require.NoError(t, a.collectServiceStatuses(context.Background(), c, ss))

	require.Len(t, ss.Status.Services, 1)
	assert.Equal(t, kcmv1.ServiceStateDeployed, ss.Status.Services[0].State)
	assert.True(t, ss.Status.Deployed, "all services Deployed")
	assertCondition(t, ss, kcmv1.ServiceSetStatusesCollectedCondition, metav1.ConditionTrue, kcmv1.ServiceSetStatusesCollectedReason)
	assertCondition(t, ss, kcmv1.ServicesInReadyStateCondition, metav1.ConditionTrue, kcmv1.ServicesInReadyStateCondition)
}

func TestCollectServiceStatusesWithoutMatchingClusters(t *testing.T) {
	ss := helmServiceSet()
	ss.Status.Services[0].State = kcmv1.ServiceStateDeployed
	ss.Status.Deployed = true
	a, c := newTestAdapter(t, profileFor(ss))

	require.NoError(t, a.collectServiceStatuses(context.Background(), c, ss))

	assert.False(t, ss.Status.Deployed, "no matching cluster means not deployed")
	assert.Equal(t, kcmv1.ServiceStateDeployed, ss.Status.Services[0].State, "services are left as they were")
	assertCondition(t, ss, kcmv1.ServiceSetStatusesCollectedCondition, metav1.ConditionTrue, kcmv1.ServiceSetStatusesCollectedReason)
}

func TestCollectServiceStatusesFailures(t *testing.T) {
	tests := []struct {
		name string
		objs func(ss *kcmv1.ServiceSet) []client.Object
	}{
		{
			name: "missing Profile",
			objs: func(*kcmv1.ServiceSet) []client.Object { return nil },
		},
		{
			name: "missing ClusterSummary",
			objs: func(ss *kcmv1.ServiceSet) []client.Object {
				return []client.Object{profileFor(ss, capiClusterRef())}
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ss := helmServiceSet()
			a, c := newTestAdapter(t, tt.objs(ss)...)

			require.Error(t, a.collectServiceStatuses(context.Background(), c, ss))

			assertCondition(t, ss, kcmv1.ServiceSetStatusesCollectedCondition, metav1.ConditionFalse, kcmv1.ServiceSetStatusesNotCollectedReason)
		})
	}
}

func TestCollectServiceStatusesSelfManagementReadsClusterProfile(t *testing.T) {
	ss := helmServiceSet()
	ss.Spec.Cluster = ""
	ss.Spec.Provider.SelfManagement = true
	mgmt := corev1.ObjectReference{
		Kind:       libsveltosv1beta1.SveltosClusterKind,
		Namespace:  managementSveltosCluster,
		Name:       managementSveltosCluster,
		APIVersion: libsveltosv1beta1.GroupVersion.String(),
	}
	clusterProfile := &addoncontrollerv1beta1.ClusterProfile{
		Name:   ss.Name,
		Status: addoncontrollerv1beta1.Status{MatchingClusterRefs: []corev1.ObjectReference{mgmt}},
	}
	summary := deployedHelmSummary(ss)
	summary.Name = clusterops.GetClusterSummaryName(addoncontrollerv1beta1.ClusterProfileKind, ss.Name, managementSveltosCluster, true)
	summary.Namespace = managementSveltosCluster
	a, c := newTestAdapter(t, clusterProfile, summary)

	require.NoError(t, a.collectServiceStatuses(context.Background(), c, ss))

	assert.Equal(t, kcmv1.ServiceStateDeployed, ss.Status.Services[0].State)
}

func TestUpdateServicesInReadyStateCondition(t *testing.T) {
	tests := []struct {
		name        string
		states      []string
		specCount   int
		wantStatus  metav1.ConditionStatus
		wantMessage string
		wantSet     bool
	}{
		{name: "all deployed", states: []string{kcmv1.ServiceStateDeployed, kcmv1.ServiceStateDeployed}, specCount: 2, wantStatus: metav1.ConditionTrue, wantMessage: "2/2", wantSet: true},
		{name: "partially deployed", states: []string{kcmv1.ServiceStateDeployed, kcmv1.ServiceStateProvisioning}, specCount: 2, wantStatus: metav1.ConditionFalse, wantMessage: "1/2", wantSet: true},
		{name: "more deployed than in spec is ignored", states: []string{kcmv1.ServiceStateDeployed, kcmv1.ServiceStateDeployed}, specCount: 1, wantSet: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			a, _ := newTestAdapter(t)
			ss := serviceSetFor()
			for i := range tt.specCount {
				ss.Spec.Services = append(ss.Spec.Services, kcmv1.ServiceWithValues{Name: string(rune('a' + i))})
			}
			for _, state := range tt.states {
				ss.Status.Services = append(ss.Status.Services, kcmv1.ServiceState{State: state})
			}

			a.updateServicesInReadyStateCondition(ss)

			cond := apimeta.FindStatusCondition(ss.Status.Conditions, kcmv1.ServicesInReadyStateCondition)
			if !tt.wantSet {
				assert.Nil(t, cond, "condition must not be set")
				return
			}
			require.NotNil(t, cond)
			assert.Equal(t, tt.wantStatus, cond.Status)
			assert.Equal(t, tt.wantMessage, cond.Message)
		})
	}
}
