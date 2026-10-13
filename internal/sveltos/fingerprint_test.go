// Copyright 2026
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
	"time"

	addoncontrollerv1beta1 "github.com/projectsveltos/addon-controller/api/v1beta1"
	libsveltosv1beta1 "github.com/projectsveltos/libsveltos/api/v1beta1"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

// --- clusterConfigCache eviction & hit path ---

// listCountingClient wraps a real fake client and counts List calls so
// findOwnedClusterConfiguration cache-hit behavior can be asserted.
type listCountingClient struct {
	client.Client
	listCount int
}

func (c *listCountingClient) List(ctx context.Context, list client.ObjectList, opts ...client.ListOption) error {
	c.listCount++
	return c.Client.List(ctx, list, opts...)
}

// TestFindOwnedClusterConfiguration_CachesResult asserts that a second
// lookup for the same (namespace, profileUID) hits the process-global
// cache rather than issuing another LIST against the regional API.
// This is the reviewer-motivated perf fix: the regional client isn't
// cache-backed, so uncached lookups amount to a real API call per
// verifier round.
func TestFindOwnedClusterConfiguration_CachesResult(t *testing.T) {
	// Snapshot + isolate the process-global CC cache.
	clusterConfigCache.Lock()
	original := maps.Clone(clusterConfigCache.entries)
	clusterConfigCache.entries = make(map[ccCacheKey]ccCacheEntry)
	clusterConfigCache.Unlock()
	t.Cleanup(func() {
		clusterConfigCache.Lock()
		clusterConfigCache.entries = original
		clusterConfigCache.Unlock()
	})

	const namespace = "cc-cache-test"
	profileUID := types.UID("profile-uid-123")

	cc := &addoncontrollerv1beta1.ClusterConfiguration{
		Name:            "cc-1",
		Namespace:       namespace,
		OwnerReferences: []metav1.OwnerReference{{UID: profileUID}},
	}
	scheme := runtime.NewScheme()
	require.NoError(t, addoncontrollerv1beta1.AddToScheme(scheme))
	inner := fake.NewClientBuilder().WithScheme(scheme).WithObjects(cc).Build()
	spy := &listCountingClient{Client: inner}

	first, err := findOwnedClusterConfiguration(context.Background(), spy, namespace, profileUID)
	require.NoError(t, err)
	require.NotNil(t, first, "first lookup must find the CC")
	require.Equal(t, 1, spy.listCount, "first lookup must LIST")

	second, err := findOwnedClusterConfiguration(context.Background(), spy, namespace, profileUID)
	require.NoError(t, err)
	require.NotNil(t, second)
	assert.Equal(t, 1, spy.listCount, "second lookup must be served from cache (no additional LIST)")
	assert.Same(t, first, second, "cache should return the same pointer on hit")
}

// TestFindOwnedClusterConfiguration_CachesNegativeResult asserts that
// even a "not found yet" lookup is cached — this is the common case
// between Profile creation and sveltos observing the cluster, and
// re-listing the whole namespace in that window is exactly the load
// the cache is meant to shed.
func TestFindOwnedClusterConfiguration_CachesNegativeResult(t *testing.T) {
	clusterConfigCache.Lock()
	original := maps.Clone(clusterConfigCache.entries)
	clusterConfigCache.entries = make(map[ccCacheKey]ccCacheEntry)
	clusterConfigCache.Unlock()
	t.Cleanup(func() {
		clusterConfigCache.Lock()
		clusterConfigCache.entries = original
		clusterConfigCache.Unlock()
	})

	scheme := runtime.NewScheme()
	require.NoError(t, addoncontrollerv1beta1.AddToScheme(scheme))
	inner := fake.NewClientBuilder().WithScheme(scheme).Build() // no CC exists
	spy := &listCountingClient{Client: inner}

	first, err := findOwnedClusterConfiguration(context.Background(), spy, "empty-ns", types.UID("nope"))
	require.NoError(t, err)
	require.Nil(t, first)
	require.Equal(t, 1, spy.listCount)

	second, err := findOwnedClusterConfiguration(context.Background(), spy, "empty-ns", types.UID("nope"))
	require.NoError(t, err)
	assert.Nil(t, second)
	assert.Equal(t, 1, spy.listCount, "negative result must also be cached")
}

// TestSweepClusterConfigCache_EvictsStaleEntries asserts that
// sweepClusterConfigCache drops entries whose writtenAt is older than
// clusterConfigCacheTTL while preserving fresh ones. Write-based TTL —
// unlike the ruleCache, this cache does NOT refresh on read (see
// ccCacheEntry doc-comment).
func TestSweepClusterConfigCache_EvictsStaleEntries(t *testing.T) {
	clusterConfigCache.Lock()
	original := maps.Clone(clusterConfigCache.entries)
	clusterConfigCache.entries = make(map[ccCacheKey]ccCacheEntry)
	clusterConfigCache.Unlock()
	t.Cleanup(func() {
		clusterConfigCache.Lock()
		clusterConfigCache.entries = original
		clusterConfigCache.Unlock()
	})

	freshKey := ccCacheKey{namespace: "ns-a", profileUID: "fresh"}
	staleKey := ccCacheKey{namespace: "ns-b", profileUID: "stale"}
	now := time.Now()

	clusterConfigCache.Lock()
	clusterConfigCache.entries[freshKey] = ccCacheEntry{writtenAt: now.Add(-clusterConfigCacheTTL / 2)}
	clusterConfigCache.entries[staleKey] = ccCacheEntry{writtenAt: now.Add(-2 * clusterConfigCacheTTL)}
	clusterConfigCache.Unlock()

	sweepClusterConfigCache()

	clusterConfigCache.Lock()
	defer clusterConfigCache.Unlock()
	_, freshOK := clusterConfigCache.entries[freshKey]
	_, staleOK := clusterConfigCache.entries[staleKey]
	assert.True(t, freshOK, "fresh entry must survive sweep")
	assert.False(t, staleOK, "stale entry must be evicted")
}

// --- computeServiceHash ---

func sampleHelmRelease() *addoncontrollerv1beta1.HelmChartSummary {
	return &addoncontrollerv1beta1.HelmChartSummary{
		ReleaseName:      "mcs-podinfo",
		ReleaseNamespace: "podinfo",
		ValuesHash:       []byte("values-abc"),
		PatchesHash:      []byte("patches-xyz"),
	}
}

func sampleChart() *addoncontrollerv1beta1.Chart {
	return &addoncontrollerv1beta1.Chart{
		ReleaseName:  "mcs-podinfo",
		Namespace:    "podinfo",
		ChartVersion: "6.5.0",
		AppVersion:   "6.5.0",
		RepoURL:      "GitRepository://kcm-system/podinfo-6-5-0/charts/podinfo",
	}
}

func TestComputeServiceHash_Stable(t *testing.T) {
	a := computeServiceHash(sampleHelmRelease(), sampleChart())
	b := computeServiceHash(sampleHelmRelease(), sampleChart())
	assert.NotEmpty(t, a)
	assert.Equal(t, a, b, "same input must produce same hash")
}

func TestComputeServiceHash_NilInputsReturnEmpty(t *testing.T) {
	assert.Empty(t, computeServiceHash(nil, sampleChart()))
	assert.Empty(t, computeServiceHash(sampleHelmRelease(), nil))
	assert.Empty(t, computeServiceHash(nil, nil))
}

func TestComputeServiceHash_ExcludesLastAppliedTime(t *testing.T) {
	c1 := sampleChart()
	c2 := sampleChart()
	c2.LastAppliedTime = &metav1.Time{Time: time.Now()}
	a := computeServiceHash(sampleHelmRelease(), c1)
	b := computeServiceHash(sampleHelmRelease(), c2)
	assert.Equal(t, a, b, "lastAppliedTime must NOT influence the hash")
}

func TestComputeServiceHash_DetectsEveryComponentChange(t *testing.T) {
	base := computeServiceHash(sampleHelmRelease(), sampleChart())

	cases := []struct {
		name   string
		mutate func(*addoncontrollerv1beta1.HelmChartSummary, *addoncontrollerv1beta1.Chart)
	}{
		{"chart appVersion", func(_ *addoncontrollerv1beta1.HelmChartSummary, c *addoncontrollerv1beta1.Chart) {
			c.AppVersion = "6.5.1"
		}},
		{"chart chartVersion", func(_ *addoncontrollerv1beta1.HelmChartSummary, c *addoncontrollerv1beta1.Chart) {
			c.ChartVersion = "6.6.0"
		}},
		{"chart namespace", func(_ *addoncontrollerv1beta1.HelmChartSummary, c *addoncontrollerv1beta1.Chart) {
			c.Namespace = "other-ns"
		}},
		{"chart releaseName", func(_ *addoncontrollerv1beta1.HelmChartSummary, c *addoncontrollerv1beta1.Chart) {
			c.ReleaseName = "other"
		}},
		{"chart repoURL", func(_ *addoncontrollerv1beta1.HelmChartSummary, c *addoncontrollerv1beta1.Chart) {
			c.RepoURL = "OCIRepository://elsewhere"
		}},
		{"release ValuesHash", func(r *addoncontrollerv1beta1.HelmChartSummary, _ *addoncontrollerv1beta1.Chart) {
			r.ValuesHash = []byte("values-new")
		}},
		{"release PatchesHash", func(r *addoncontrollerv1beta1.HelmChartSummary, _ *addoncontrollerv1beta1.Chart) {
			r.PatchesHash = []byte("patches-new")
		}},
	}

	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			r := sampleHelmRelease()
			c := sampleChart()
			tt.mutate(r, c)
			got := computeServiceHash(r, c)
			assert.NotEqual(t, base, got, "mutating %s must change the hash", tt.name)
		})
	}
}

// --- serviceHashesFromArtifacts ---

func TestServiceHashesFromArtifacts_NilInputs(t *testing.T) {
	assert.Empty(t, serviceHashesFromArtifacts(nil, nil, addoncontrollerv1beta1.ClusterProfileKind, "p"))
	assert.Empty(t, serviceHashesFromArtifacts(nil, &addoncontrollerv1beta1.ClusterConfiguration{}, addoncontrollerv1beta1.ClusterProfileKind, "p"))
	assert.Empty(t, serviceHashesFromArtifacts(&addoncontrollerv1beta1.ClusterSummary{}, nil, addoncontrollerv1beta1.ClusterProfileKind, "p"))
}

func TestServiceHashesFromArtifacts_BuildsMapForClusterProfile(t *testing.T) {
	summary := &addoncontrollerv1beta1.ClusterSummary{
		Status: addoncontrollerv1beta1.ClusterSummaryStatus{
			HelmReleaseSummaries: []addoncontrollerv1beta1.HelmChartSummary{
				{ReleaseName: "mcs-podinfo", ReleaseNamespace: "podinfo", ValuesHash: []byte("v1"), PatchesHash: []byte("p1")},
				{ReleaseName: "mcs-prometheus", ReleaseNamespace: "monitoring", ValuesHash: []byte("v2"), PatchesHash: []byte("p2")},
			},
		},
	}
	cc := &addoncontrollerv1beta1.ClusterConfiguration{
		Status: addoncontrollerv1beta1.ClusterConfigurationStatus{
			ClusterProfileResources: []addoncontrollerv1beta1.ClusterProfileResource{
				{
					ClusterProfileName: "myset",
					Features: []addoncontrollerv1beta1.Feature{
						{
							FeatureID: libsveltosv1beta1.FeatureHelm,
							Charts: []addoncontrollerv1beta1.Chart{
								{ReleaseName: "mcs-podinfo", Namespace: "podinfo", ChartVersion: "6.5.0", AppVersion: "6.5.0", RepoURL: "git://podinfo"},
								{ReleaseName: "mcs-prometheus", Namespace: "monitoring", ChartVersion: "27.1.0", AppVersion: "27.1.0", RepoURL: "git://prom"},
							},
						},
					},
				},
			},
		},
	}

	hashes := serviceHashesFromArtifacts(summary, cc, addoncontrollerv1beta1.ClusterProfileKind, "myset")
	require.Len(t, hashes, 2)
	assert.NotEmpty(t, hashes[client.ObjectKey{Namespace: "podinfo", Name: "mcs-podinfo"}])
	assert.NotEmpty(t, hashes[client.ObjectKey{Namespace: "monitoring", Name: "mcs-prometheus"}])
	assert.NotEqual(t,
		hashes[client.ObjectKey{Namespace: "podinfo", Name: "mcs-podinfo"}],
		hashes[client.ObjectKey{Namespace: "monitoring", Name: "mcs-prometheus"}],
		"distinct services must hash distinctly")
}

func TestServiceHashesFromArtifacts_SkipsReleaseWithoutChartEntry(t *testing.T) {
	// HelmReleaseSummaries lists a release that ClusterConfiguration has
	// no Charts[] entry for. The release is skipped.
	summary := &addoncontrollerv1beta1.ClusterSummary{
		Status: addoncontrollerv1beta1.ClusterSummaryStatus{
			HelmReleaseSummaries: []addoncontrollerv1beta1.HelmChartSummary{
				{ReleaseName: "no-chart", ReleaseNamespace: "ns", ValuesHash: []byte("x")},
			},
		},
	}
	cc := &addoncontrollerv1beta1.ClusterConfiguration{
		Status: addoncontrollerv1beta1.ClusterConfigurationStatus{
			ClusterProfileResources: []addoncontrollerv1beta1.ClusterProfileResource{{
				ClusterProfileName: "myset",
				Features:           []addoncontrollerv1beta1.Feature{{FeatureID: libsveltosv1beta1.FeatureHelm}},
			}},
		},
	}
	hashes := serviceHashesFromArtifacts(summary, cc, addoncontrollerv1beta1.ClusterProfileKind, "myset")
	assert.Empty(t, hashes)
}

func TestServiceHashesFromArtifacts_WrongProfileSectionReturnsEmpty(t *testing.T) {
	cc := &addoncontrollerv1beta1.ClusterConfiguration{
		Status: addoncontrollerv1beta1.ClusterConfigurationStatus{
			ClusterProfileResources: []addoncontrollerv1beta1.ClusterProfileResource{{
				ClusterProfileName: "different-set",
				Features:           []addoncontrollerv1beta1.Feature{{FeatureID: libsveltosv1beta1.FeatureHelm}},
			}},
		},
	}
	summary := &addoncontrollerv1beta1.ClusterSummary{}
	// Asking for "myset" but CC only has "different-set" → no section, no hashes.
	hashes := serviceHashesFromArtifacts(summary, cc, addoncontrollerv1beta1.ClusterProfileKind, "myset")
	assert.Empty(t, hashes)
}
