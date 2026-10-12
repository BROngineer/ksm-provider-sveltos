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
	"time"

	kcmv1 "github.com/K0rdent/kcm/api/v1beta1"
	helmcontrollerv2 "github.com/fluxcd/helm-controller/api/v2"
	sourcev1 "github.com/fluxcd/source-controller/api/v1"
	addoncontrollerv1beta1 "github.com/projectsveltos/addon-controller/api/v1beta1"
	libsveltosv1beta1 "github.com/projectsveltos/libsveltos/api/v1beta1"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	apiextv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	apimeta "k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	clusterapiv1 "sigs.k8s.io/cluster-api/api/core/v1beta2"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	"github.com/brongineer/ksm-provider-sveltos/internal/scheme"
)

const (
	testNamespace       = "team-a"
	testSystemNamespace = "kcm-system"
	testCluster         = "cd-1"
	testCredential      = "cred-1"
)

var testNow = time.Date(2026, 10, 12, 12, 0, 0, 0, time.UTC)

func newTestAdapter(t *testing.T, objs ...client.Object) (*Adapter, client.Client) {
	t.Helper()
	s, err := scheme.Management()
	require.NoError(t, err)
	c := fake.NewClientBuilder().WithScheme(s).WithObjects(objs...).Build()
	return &Adapter{Client: c, SystemNamespace: testSystemNamespace, timeFunc: func() time.Time { return testNow }}, c
}

func clusterDeploymentObjects() []client.Object {
	return []client.Object{
		&kcmv1.Credential{
			Name: testCredential, Namespace: testNamespace,
		},
		&kcmv1.ClusterDeployment{
			Name: testCluster, Namespace: testNamespace,
			Spec: kcmv1.ClusterDeploymentSpec{
				Template:   "sample-template",
				Credential: testCredential,
				Config:     &apiextv1.JSON{Raw: []byte(`{"foo":"bar"}`)},
			},
		},
		&clusterapiv1.Cluster{Name: testCluster, Namespace: testNamespace},
	}
}

func kustomizeTemplate(name string) *kcmv1.ServiceTemplate {
	return &kcmv1.ServiceTemplate{
		Name: name, Namespace: testNamespace,
		Spec: kcmv1.ServiceTemplateSpec{
			Kustomize: &kcmv1.SourceSpec{
				DeploymentType: "Local",
				LocalSourceRef: &kcmv1.LocalSourceRef{Kind: "ConfigMap", Name: name + "-src"},
			},
		},
		Status: kcmv1.ServiceTemplateStatus{
			TemplateStatusCommon: kcmv1.TemplateStatusCommon{Valid: true},
			SourceStatus:         &kcmv1.SourceStatus{Kind: "ConfigMap", Name: name + "-src", Namespace: testNamespace},
		},
	}
}

func helmTemplateObjects(name string) []client.Object {
	return []client.Object{
		&kcmv1.ServiceTemplate{
			Name: name, Namespace: testNamespace,
			Spec: kcmv1.ServiceTemplateSpec{
				Helm: &kcmv1.HelmSpec{
					ChartRef: &helmcontrollerv2.CrossNamespaceSourceReference{Kind: sourcev1.HelmChartKind, Name: name},
				},
			},
			Status: kcmv1.ServiceTemplateStatus{
				TemplateStatusCommon: kcmv1.TemplateStatusCommon{
					Valid: true,
					ChartRef: &helmcontrollerv2.CrossNamespaceSourceReference{
						Kind: sourcev1.HelmChartKind, Name: name, Namespace: testNamespace,
					},
				},
			},
		},
		&sourcev1.HelmChart{
			Name: name, Namespace: testNamespace,
			Spec: sourcev1.HelmChartSpec{
				Chart:     "ingress-nginx",
				Version:   "4.11.0",
				SourceRef: sourcev1.LocalHelmChartSourceReference{Kind: sourcev1.HelmRepositoryKind, Name: "repo"},
			},
		},
		&sourcev1.HelmRepository{
			Name: "repo", Namespace: testNamespace,
			Spec: sourcev1.HelmRepositorySpec{URL: "https://kubernetes.github.io/ingress-nginx"},
		},
	}
}

func serviceSetFor(services ...kcmv1.ServiceWithValues) *kcmv1.ServiceSet {
	return &kcmv1.ServiceSet{
		Name: "cd-1-abcd", Namespace: testNamespace, UID: "uid-1",
		Spec: kcmv1.ServiceSetSpec{
			Cluster:  testCluster,
			Provider: kcmv1.StateManagementProviderConfig{Name: "ksm-projectsveltos"},
			Services: services,
		},
	}
}

func assertCondition(t *testing.T, ss *kcmv1.ServiceSet, condType string, status metav1.ConditionStatus, reason string) {
	t.Helper()
	cond := apimeta.FindStatusCondition(ss.Status.Conditions, condType)
	require.NotNil(t, cond, "condition %s not set", condType)
	assert.Equal(t, status, cond.Status, "condition %s status", condType)
	assert.Equal(t, reason, cond.Reason, "condition %s reason", condType)
}

func TestEnsureProfileCreatesProfileForClusterDeployment(t *testing.T) {
	objs := append(clusterDeploymentObjects(), kustomizeTemplate("kust-tmpl"))
	a, c := newTestAdapter(t, objs...)
	ss := serviceSetFor(kcmv1.ServiceWithValues{Name: "kust-svc", Namespace: testNamespace, Template: "kust-tmpl"})

	require.NoError(t, a.ensureProfile(context.Background(), c, ss))

	profile := new(addoncontrollerv1beta1.Profile)
	require.NoError(t, c.Get(context.Background(), client.ObjectKeyFromObject(ss), profile))
	assert.Equal(t, kcmv1.KCMManagedLabelValue, profile.Labels[kcmv1.KCMManagedLabelKey])
	require.Len(t, profile.OwnerReferences, 1, "a Profile next to its ServiceSet is owned by it")
	assert.Equal(t, ss.Name, profile.OwnerReferences[0].Name)
	require.Len(t, profile.Spec.ClusterRefs, 1)
	assert.Equal(t, clusterapiv1.ClusterKind, profile.Spec.ClusterRefs[0].Kind)
	assert.Equal(t, testCluster, profile.Spec.ClusterRefs[0].Name)
	require.Len(t, profile.Spec.KustomizationRefs, 1)
	assert.Equal(t, "ConfigMap", profile.Spec.KustomizationRefs[0].Kind)

	assertCondition(t, ss, kcmv1.ServiceSetProfileCondition, metav1.ConditionTrue, kcmv1.ServiceSetProfileReadyReason)
}

func TestEnsureProfileBuildsHelmChartFromHelmRepository(t *testing.T) {
	objs := append(clusterDeploymentObjects(), helmTemplateObjects("nginx-tmpl")...)
	a, c := newTestAdapter(t, objs...)
	ss := serviceSetFor(kcmv1.ServiceWithValues{Name: "nginx", Namespace: "ingress", Template: "nginx-tmpl", Values: "replicas: 2"})

	require.NoError(t, a.ensureProfile(context.Background(), c, ss))

	profile := new(addoncontrollerv1beta1.Profile)
	require.NoError(t, c.Get(context.Background(), client.ObjectKeyFromObject(ss), profile))
	require.Len(t, profile.Spec.HelmCharts, 1)
	chart := profile.Spec.HelmCharts[0]
	assert.Equal(t, "https://kubernetes.github.io/ingress-nginx", chart.RepositoryURL)
	assert.Equal(t, "ingress-nginx/ingress-nginx", chart.ChartName, "non-OCI charts use <chart>/<chart>")
	assert.Equal(t, "4.11.0", chart.ChartVersion)
	assert.Equal(t, "nginx", chart.ReleaseName)
	assert.Equal(t, "ingress", chart.ReleaseNamespace)
	assert.Equal(t, "replicas: 2", chart.Values)

	require.Len(t, ss.Status.Services, 1, "a new Helm service is added to status")
	assert.Equal(t, kcmv1.ServiceTypeHelm, ss.Status.Services[0].Type)
	assert.Equal(t, kcmv1.ServiceStateProvisioning, ss.Status.Services[0].State)
	assert.Empty(t, ss.Status.Services[0].Version, "the verifier owns Version for Helm services")
}

func TestEnsureProfileSelfManagementCreatesClusterProfile(t *testing.T) {
	a, c := newTestAdapter(t, kustomizeTemplate("kust-tmpl"))
	ss := serviceSetFor(kcmv1.ServiceWithValues{Name: "kust-svc", Namespace: testNamespace, Template: "kust-tmpl"})
	ss.Spec.Cluster = ""
	ss.Spec.Provider.SelfManagement = true

	require.NoError(t, a.ensureProfile(context.Background(), c, ss))

	profile := new(addoncontrollerv1beta1.ClusterProfile)
	require.NoError(t, c.Get(context.Background(), client.ObjectKey{Name: ss.Name}, profile))
	require.Len(t, profile.Spec.ClusterRefs, 1)
	assert.Equal(t, libsveltosv1beta1.SveltosClusterKind, profile.Spec.ClusterRefs[0].Kind)
	assert.Equal(t, managementSveltosCluster, profile.Spec.ClusterRefs[0].Name)
	assert.Equal(t, managementSveltosCluster, profile.Spec.ClusterRefs[0].Namespace)
}

func TestEnsureProfileFailureReasons(t *testing.T) {
	tests := []struct {
		name       string
		objs       []client.Object
		mutate     func(*kcmv1.ServiceSet)
		wantReason string
	}{
		{
			name:       "missing ClusterDeployment",
			objs:       []client.Object{kustomizeTemplate("kust-tmpl")},
			wantReason: kcmv1.ServiceSetProfileNotReadyReason,
		},
		{
			name: "invalid provider config",
			objs: append(clusterDeploymentObjects(), kustomizeTemplate("kust-tmpl")),
			mutate: func(ss *kcmv1.ServiceSet) {
				ss.Spec.Provider.Config = &apiextv1.JSON{Raw: []byte(`{"priority":"not-a-number"}`)}
			},
			wantReason: kcmv1.ServiceSetProfileBuildFailedReason,
		},
		{
			name: "missing Helm ServiceTemplate",
			objs: clusterDeploymentObjects(),
			mutate: func(ss *kcmv1.ServiceSet) {
				ss.Spec.Services = []kcmv1.ServiceWithValues{{Name: "nginx", Namespace: "ingress", Template: "missing"}}
			},
			wantReason: kcmv1.ServiceSetHelmChartsBuildFailedReason,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			a, c := newTestAdapter(t, tt.objs...)
			ss := serviceSetFor(kcmv1.ServiceWithValues{Name: "kust-svc", Namespace: testNamespace, Template: "kust-tmpl"})
			if tt.mutate != nil {
				tt.mutate(ss)
			}

			require.Error(t, a.ensureProfile(context.Background(), c, ss))

			assertCondition(t, ss, kcmv1.ServiceSetProfileCondition, metav1.ConditionFalse, tt.wantReason)
		})
	}
}

func TestEnsureProfileUpdatesOnlyOnChange(t *testing.T) {
	objs := append(clusterDeploymentObjects(), kustomizeTemplate("kust-tmpl"), kustomizeTemplate("other-tmpl"))
	a, c := newTestAdapter(t, objs...)
	ss := serviceSetFor(kcmv1.ServiceWithValues{Name: "kust-svc", Namespace: testNamespace, Template: "kust-tmpl"})
	require.NoError(t, a.ensureProfile(context.Background(), c, ss))

	first := new(addoncontrollerv1beta1.Profile)
	require.NoError(t, c.Get(context.Background(), client.ObjectKeyFromObject(ss), first))

	require.NoError(t, a.ensureProfile(context.Background(), c, ss))
	unchanged := new(addoncontrollerv1beta1.Profile)
	require.NoError(t, c.Get(context.Background(), client.ObjectKeyFromObject(ss), unchanged))
	assert.Equal(t, first.ResourceVersion, unchanged.ResourceVersion, "an unchanged spec must not be written")

	ss.Spec.Services = append(ss.Spec.Services, kcmv1.ServiceWithValues{Name: "other", Namespace: testNamespace, Template: "other-tmpl"})
	require.NoError(t, a.ensureProfile(context.Background(), c, ss))
	changed := new(addoncontrollerv1beta1.Profile)
	require.NoError(t, c.Get(context.Background(), client.ObjectKeyFromObject(ss), changed))
	assert.NotEqual(t, first.ResourceVersion, changed.ResourceVersion)
	assert.Len(t, changed.Spec.KustomizationRefs, 2)
}

func TestHandlePauseAnnotations(t *testing.T) {
	tests := []struct {
		name            string
		serviceSetPause bool
		profilePause    bool
		wantUpdated     bool
		wantPaused      bool
	}{
		{name: "neither paused", wantUpdated: false, wantPaused: false},
		{name: "ServiceSet paused, Profile not", serviceSetPause: true, wantUpdated: true, wantPaused: true},
		{name: "both paused", serviceSetPause: true, profilePause: true, wantUpdated: false, wantPaused: true},
		{name: "Profile paused, ServiceSet resumed", profilePause: true, wantUpdated: true, wantPaused: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ss := serviceSetFor()
			if tt.serviceSetPause {
				ss.Annotations = map[string]string{kcmv1.ServiceSetPausedAnnotation: "true"}
			}
			profile := metav1.ObjectMeta{}
			if tt.profilePause {
				profile.Annotations = map[string]string{addoncontrollerv1beta1.ProfilePausedAnnotation: "true"}
			}

			assert.Equal(t, tt.wantUpdated, handlePauseAnnotations(&profile, ss))
			_, paused := profile.Annotations[addoncontrollerv1beta1.ProfilePausedAnnotation]
			assert.Equal(t, tt.wantPaused, paused)
		})
	}
}

func TestResolveRegionalClient(t *testing.T) {
	t.Run("self-managed ServiceSet uses the management client", func(t *testing.T) {
		a, c := newTestAdapter(t)
		ss := serviceSetFor()
		ss.Spec.Cluster = ""

		got, err := getRegionalClient(context.Background(), c, ss, a.SystemNamespace)
		require.NoError(t, err)
		assert.Same(t, c, got)
	})

	t.Run("cluster without region uses the management client", func(t *testing.T) {
		a, c := newTestAdapter(t, clusterDeploymentObjects()...)

		got, err := getRegionalClient(context.Background(), c, serviceSetFor(), a.SystemNamespace)
		require.NoError(t, err)
		assert.Same(t, c, got)
	})

	t.Run("cache answers without reading the ClusterDeployment", func(t *testing.T) {
		a, c := newTestAdapter(t)
		cached, _ := newTestAdapter(t)
		cache := map[client.ObjectKey]client.Client{{Namespace: testNamespace, Name: testCluster}: cached.Client}

		got, err := resolveRegionalClient(context.Background(), c, serviceSetFor(), a.SystemNamespace, cache)
		require.NoError(t, err)
		assert.Same(t, cached.Client, got)
	})

	t.Run("resolved client is stored in the cache", func(t *testing.T) {
		a, c := newTestAdapter(t, clusterDeploymentObjects()...)
		cache := map[client.ObjectKey]client.Client{}

		_, err := resolveRegionalClient(context.Background(), c, serviceSetFor(), a.SystemNamespace, cache)
		require.NoError(t, err)
		assert.Same(t, c, cache[client.ObjectKey{Namespace: testNamespace, Name: testCluster}])
	})

	t.Run("missing ClusterDeployment is an error", func(t *testing.T) {
		a, c := newTestAdapter(t)

		_, err := getRegionalClient(context.Background(), c, serviceSetFor(), a.SystemNamespace)
		assert.Error(t, err)
	})

	t.Run("unknown region is an error", func(t *testing.T) {
		objs := clusterDeploymentObjects()
		objs[0].(*kcmv1.Credential).Spec.Region = "eu-west"
		a, c := newTestAdapter(t, objs...)

		_, err := getRegionalClient(context.Background(), c, serviceSetFor(), a.SystemNamespace)
		assert.Error(t, err)
	})
}
