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
	"maps"
	"testing"

	kcmv1 "github.com/K0rdent/kcm/api/v1beta1"
	addoncontrollerv1beta1 "github.com/projectsveltos/addon-controller/api/v1beta1"
	libsveltosv1beta1 "github.com/projectsveltos/libsveltos/api/v1beta1"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

const testProfileUID = types.UID("profile-uid")

func isolateClusterConfigCache(t *testing.T) {
	t.Helper()
	clusterConfigCache.Lock()
	original := maps.Clone(clusterConfigCache.entries)
	clusterConfigCache.entries = make(map[ccCacheKey]ccCacheEntry)
	clusterConfigCache.Unlock()
	t.Cleanup(func() {
		clusterConfigCache.Lock()
		clusterConfigCache.entries = original
		clusterConfigCache.Unlock()
	})
}

func ownedClusterConfiguration(ss *kcmv1.ServiceSet) *addoncontrollerv1beta1.ClusterConfiguration {
	return &addoncontrollerv1beta1.ClusterConfiguration{
		Name:            "cc-" + testCluster,
		Namespace:       testNamespace,
		OwnerReferences: []metav1.OwnerReference{{UID: testProfileUID, Kind: addoncontrollerv1beta1.ProfileKind, Name: ss.Name}},
		Status: addoncontrollerv1beta1.ClusterConfigurationStatus{
			ProfileResources: []addoncontrollerv1beta1.ProfileResource{{
				ProfileName: ss.Name,
				Features: []addoncontrollerv1beta1.Feature{{
					FeatureID: libsveltosv1beta1.FeatureHelm,
					Charts: []addoncontrollerv1beta1.Chart{{
						ReleaseName: "nginx", Namespace: "ingress",
						ChartVersion: "4.11.0", AppVersion: "1.11.0", RepoURL: "https://kubernetes.github.io/ingress-nginx",
					}},
				}},
			}},
		},
	}
}

func TestFetchHelmArtifactsForVerifierAndHash(t *testing.T) {
	isolateClusterConfigCache(t)
	ss := helmServiceSet()
	profile := profileFor(ss, capiClusterRef())
	profile.UID = testProfileUID
	_, c := newTestAdapter(t, profile, deployedHelmSummary(ss), ownedClusterConfiguration(ss))

	summary, cc, kind, name, err := fetchHelmArtifactsForVerifier(context.Background(), c, ss)
	require.NoError(t, err)
	require.NotNil(t, summary)
	require.NotNil(t, cc)
	assert.Equal(t, addoncontrollerv1beta1.ProfileKind, kind)
	assert.Equal(t, ss.Name, name)

	hashes := serviceHashesFromArtifacts(summary, cc, kind, name)
	assert.NotEmpty(t, hashes[client.ObjectKey{Namespace: "ingress", Name: "nginx"}])
}

func TestFetchHelmArtifactsForVerifierWithoutArtifacts(t *testing.T) {
	tests := []struct {
		name    string
		profile func(ss *kcmv1.ServiceSet) client.Object
	}{
		{name: "no Profile yet", profile: func(*kcmv1.ServiceSet) client.Object { return nil }},
		{name: "Profile without matching cluster", profile: func(ss *kcmv1.ServiceSet) client.Object { return profileFor(ss) }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			isolateClusterConfigCache(t)
			ss := helmServiceSet()
			var objs []client.Object
			if p := tt.profile(ss); p != nil {
				objs = append(objs, p)
			}
			_, c := newTestAdapter(t, objs...)

			summary, cc, _, _, err := fetchHelmArtifactsForVerifier(context.Background(), c, ss)
			require.NoError(t, err, "missing artifacts mean no fingerprint yet, not an error")
			assert.Nil(t, summary)
			assert.Nil(t, cc)
		})
	}
}
