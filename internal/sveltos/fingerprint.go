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
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strings"
	"sync"
	"time"

	kcmv1 "github.com/K0rdent/kcm/api/v1beta1"
	addoncontrollerv1beta1 "github.com/projectsveltos/addon-controller/api/v1beta1"
	"github.com/projectsveltos/addon-controller/lib/clusterops"
	libsveltosv1beta1 "github.com/projectsveltos/libsveltos/api/v1beta1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// fetchHelmArtifactsForVerifier loads the sveltos artifacts the verifier
// needs to compute per-service hashes:
//
//   - ClusterSummary  — for HelmReleaseSummaries[i].{ValuesHash, PatchesHash}
//   - ClusterConfiguration — for Charts[i].{AppVersion, ChartVersion,
//     Namespace, ReleaseName, RepoURL}
//
// Both are namespaced; both live in the matching cluster's namespace
// (CAPI Cluster.Namespace for non-self-managed, "mgmt" for self-managed).
//
// The ClusterConfiguration is found via owner references rather than
// reproducing sveltos's unexported name-format helpers: sveltos stamps the
// Profile/ClusterProfile as an owner ref on the ClusterConfiguration when
// it first records resources for that (cluster, profile) pair, so the
// owner UID is a stable identity link.
//
// Returns (nil, nil, nil) — without error — when sveltos hasn't picked a
// matching cluster yet or hasn't created the artifacts. The verifier
// treats that case as "no fingerprint information available" and falls
// back to the downgrade-only path.
func fetchHelmArtifactsForVerifier(
	ctx context.Context,
	rgnClient client.Client,
	serviceSet *kcmv1.ServiceSet,
) (
	summary *addoncontrollerv1beta1.ClusterSummary,
	cc *addoncontrollerv1beta1.ClusterConfiguration,
	profileKind string,
	profileName string,
	err error,
) {
	var profileObj client.Object
	if serviceSet.Spec.Provider.SelfManagement {
		profileObj = new(addoncontrollerv1beta1.ClusterProfile)
	} else {
		profileObj = new(addoncontrollerv1beta1.Profile)
	}
	if err := rgnClient.Get(ctx, client.ObjectKeyFromObject(serviceSet), profileObj); err != nil {
		if apierrors.IsNotFound(err) {
			return nil, nil, "", "", nil
		}
		return nil, nil, "", "", fmt.Errorf("get sveltos profile %s: %w", client.ObjectKeyFromObject(serviceSet), err)
	}

	var (
		matchingRefs []corev1.ObjectReference
		profileUID   types.UID
	)
	switch p := profileObj.(type) {
	case *addoncontrollerv1beta1.Profile:
		matchingRefs = p.Status.MatchingClusterRefs
		profileKind = addoncontrollerv1beta1.ProfileKind
		profileName = p.Name
		profileUID = p.UID
	case *addoncontrollerv1beta1.ClusterProfile:
		matchingRefs = p.Status.MatchingClusterRefs
		profileKind = addoncontrollerv1beta1.ClusterProfileKind
		profileName = p.Name
		profileUID = p.UID
	}
	if len(matchingRefs) == 0 {
		return nil, nil, "", "", nil
	}

	cluster := matchingRefs[0]
	isSveltos := cluster.APIVersion == libsveltosv1beta1.GroupVersion.WithKind(libsveltosv1beta1.SveltosClusterKind).GroupVersion().String()

	// ClusterSummary — sveltos exports a name helper for this one.
	summaryName := clusterops.GetClusterSummaryName(profileKind, profileName, cluster.Name, isSveltos)
	summary = new(addoncontrollerv1beta1.ClusterSummary)
	summaryKey := client.ObjectKey{Name: summaryName, Namespace: cluster.Namespace}
	if err := rgnClient.Get(ctx, summaryKey, summary); err != nil {
		if !apierrors.IsNotFound(err) {
			return nil, nil, "", "", fmt.Errorf("get ClusterSummary %s: %w", summaryKey, err)
		}
		summary = nil
	}

	// Prune the CC cache — pruning frequency scales with
	// verifier activity, no goroutines/tickers needed.
	// we'll sweep the cache so that next findOwnedClusterConfiguration
	// call won't return the already expired entry.
	sweepClusterConfigCache()

	cc, err = findOwnedClusterConfiguration(ctx, rgnClient, cluster.Namespace, profileUID)
	if err != nil {
		return nil, nil, "", "", err
	}

	return summary, cc, profileKind, profileName, nil
}

// clusterConfigCacheTTL bounds how long a found ClusterConfiguration
// stays in the cache without being referenced. The regional client is
// NOT cache-backed (bare client.New from kubeconfig bytes), so every
// List call is a live API request. Sveltos updates a ClusterConfiguration
// only on apply events — infrequent relative to reconcile rate — so a
// short TTL trades a small staleness window for eliminating the LIST
// per verifier round in the steady state.
const clusterConfigCacheTTL = 30 * time.Second

type ccCacheKey struct {
	namespace  string
	profileUID types.UID
}

// ccCacheEntry pairs the last-observed ClusterConfiguration with the
// time it was written to the cache. sweepClusterConfigCache evicts
// entries whose writtenAt is older than clusterConfigCacheTTL — TTL is
// write-based, not access-based. Access-based refresh would keep a
// stale snapshot alive indefinitely for hot ServiceSets (poller hits
// every 10s, TTL 30s → entry never expires), preventing us from
// observing sveltos's late-arriving updates to Status.ProfileResources
// (e.g. the per-profile Helm section that appears seconds after the CC
// itself). Write-based TTL bounds staleness at clusterConfigCacheTTL.
type ccCacheEntry struct {
	writtenAt time.Time
	cc        *addoncontrollerv1beta1.ClusterConfiguration
}

// clusterConfigCache memoises the (namespace, profileUID) → CC lookup
// so findOwnedClusterConfiguration doesn't LIST the regional namespace
// on every verifier round.
var clusterConfigCache = struct {
	entries map[ccCacheKey]ccCacheEntry
	sync.Mutex
}{entries: make(map[ccCacheKey]ccCacheEntry)}

// findOwnedClusterConfiguration returns the ClusterConfiguration in
// namespace whose OwnerReferences include profileUID. Returns (nil, nil)
// when none exists yet (sveltos may not have recorded resources for the
// profile on this cluster yet). Callers already handle a nil result as
// "no fingerprint information available" so a sentinel error would only
// force pointless errors.Is checks at every call site.
//
// Results are cached with a TTL because the regional client used here
// is not cache-backed — every miss is a real LIST on the regional API
// server. See clusterConfigCacheTTL.
func findOwnedClusterConfiguration(
	ctx context.Context,
	rgnClient client.Client,
	namespace string,
	profileUID types.UID,
) (*addoncontrollerv1beta1.ClusterConfiguration, error) {
	key := ccCacheKey{namespace: namespace, profileUID: profileUID}
	now := time.Now()

	clusterConfigCache.Lock()
	if cached, ok := clusterConfigCache.entries[key]; ok {
		clusterConfigCache.Unlock()
		return cached.cc, nil
	}
	clusterConfigCache.Unlock()

	list := new(addoncontrollerv1beta1.ClusterConfigurationList)
	if err := rgnClient.List(ctx, list, client.InNamespace(namespace)); err != nil {
		return nil, fmt.Errorf("list ClusterConfigurations in %s: %w", namespace, err)
	}
	var found *addoncontrollerv1beta1.ClusterConfiguration
	for i := range list.Items {
		for _, owner := range list.Items[i].OwnerReferences {
			if owner.UID == profileUID {
				found = &list.Items[i]
				break
			}
		}
		if found != nil {
			break
		}
	}

	// Cache both hits and misses: a miss (found == nil) is common
	// during the window between Profile creation and sveltos observing
	// the cluster, and re-listing every reconcile in that window is
	// exactly the load we want to shed. Downstream callers already
	// treat nil as "not yet available" and behave correctly.
	clusterConfigCache.Lock()
	clusterConfigCache.entries[key] = ccCacheEntry{cc: found, writtenAt: now}
	clusterConfigCache.Unlock()

	return found, nil
}

// sweepClusterConfigCache evicts cache entries whose writtenAt is
// older than clusterConfigCacheTTL. Write-based TTL — see ccCacheEntry
// doc-comment for why access-based would keep stale snapshots alive
// forever. Called once per verifier round from
// fetchHelmArtifactsForVerifier so pruning frequency scales with
// reconcile activity.
func sweepClusterConfigCache() {
	cutoff := time.Now().Add(-clusterConfigCacheTTL)
	clusterConfigCache.Lock()
	defer clusterConfigCache.Unlock()
	for key, entry := range clusterConfigCache.entries {
		if entry.writtenAt.Before(cutoff) {
			delete(clusterConfigCache.entries, key)
		}
	}
}

// serviceHashesFromArtifacts builds a (releaseNamespace, releaseName) →
// ServiceHash map for every Helm release sveltos has recorded for the
// supplied profile. Missing inputs are tolerated: if either summary or cc
// is nil the result is an empty map.
func serviceHashesFromArtifacts(
	summary *addoncontrollerv1beta1.ClusterSummary,
	cc *addoncontrollerv1beta1.ClusterConfiguration,
	profileKind, profileName string,
) map[client.ObjectKey]string {
	hashes := make(map[client.ObjectKey]string)
	if summary == nil || cc == nil {
		return hashes
	}

	// Locate this profile's section inside the ClusterConfiguration.
	idx, err := addoncontrollerv1beta1.GetClusterConfigurationSectionIndex(cc, profileKind, profileName)
	if err != nil {
		return hashes
	}

	var features []addoncontrollerv1beta1.Feature
	if profileKind == addoncontrollerv1beta1.ClusterProfileKind {
		features = cc.Status.ClusterProfileResources[idx].Features
	} else {
		features = cc.Status.ProfileResources[idx].Features
	}

	var charts []addoncontrollerv1beta1.Chart
	for i := range features {
		if features[i].FeatureID == libsveltosv1beta1.FeatureHelm {
			charts = features[i].Charts
			break
		}
	}

	// Index by (releaseNamespace, releaseName) for cross-source lookup.
	chartByKey := make(map[client.ObjectKey]*addoncontrollerv1beta1.Chart, len(charts))
	for i := range charts {
		chartByKey[client.ObjectKey{Namespace: charts[i].Namespace, Name: charts[i].ReleaseName}] = &charts[i]
	}

	for i := range summary.Status.HelmReleaseSummaries {
		rs := &summary.Status.HelmReleaseSummaries[i]
		key := client.ObjectKey{Namespace: rs.ReleaseNamespace, Name: rs.ReleaseName}
		chart, ok := chartByKey[key]
		if !ok {
			continue
		}
		if h := computeServiceHash(rs, chart); h != "" {
			hashes[key] = h
		}
	}

	return hashes
}

// computeServiceHash produces a stable hex-sha256 fingerprint covering
// chart identity, rendered values, and sveltos-applied patches for a
// single helm-deployed service.
//
//	ReleaseHash = sha256(appVersion|chartVersion|namespace|releaseName|repoURL)
//	ServiceHash = sha256(ReleaseHash || ValuesHash || PatchesHash)
//
// `lastAppliedTime` is INTENTIONALLY excluded — sveltos rewrites it on every
// re-touch even when nothing else changed, and including it would defeat
// the whole point of the fingerprint.
//
// Returns "" when either input is nil. Callers MUST treat empty as
// "no fingerprint available" — never short-circuit comparisons against
// other empties (e.g. a brand-new service whose ClusterConfiguration
// entry hasn't appeared yet would otherwise spuriously match a
// brand-new ServiceState whose LastDeployedHash is also unset).
func computeServiceHash(release *addoncontrollerv1beta1.HelmChartSummary, chart *addoncontrollerv1beta1.Chart) string {
	if release == nil || chart == nil {
		return ""
	}

	// Stable, explicit field ordering — no map iteration.
	releaseInput := strings.Join([]string{
		"appVersion=" + chart.AppVersion,
		"chartVersion=" + chart.ChartVersion,
		"namespace=" + chart.Namespace,
		"releaseName=" + chart.ReleaseName,
		"repoURL=" + chart.RepoURL,
	}, "|")
	releaseHash := sha256.Sum256([]byte(releaseInput))

	// sha256.Hash.Write is documented to never return an error; the
	// blank-assignments silence errcheck without adding real error handling.
	combined := sha256.New()
	_, _ = combined.Write(releaseHash[:])
	_, _ = combined.Write(release.ValuesHash)
	_, _ = combined.Write(release.PatchesHash)
	return hex.EncodeToString(combined.Sum(nil))
}
