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
	"fmt"
	"slices"
	"time"

	kcmv1 "github.com/K0rdent/kcm/api/v1beta1"
	addoncontrollerv1beta1 "github.com/projectsveltos/addon-controller/api/v1beta1"
	"github.com/projectsveltos/addon-controller/lib/clusterops"
	libsveltosv1beta1 "github.com/projectsveltos/libsveltos/api/v1beta1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

var errNoMatchingClusters = errors.New("no matching clusters for ServiceSet")

func (r *Adapter) collectServiceStatuses(ctx context.Context, rgnClient client.Client, serviceSet *kcmv1.ServiceSet) (err error) {
	start := time.Now()
	l := ctrl.LoggerFrom(ctx)
	l.Info("Collecting Service statuses")
	statusesCollectedCondition := findCondition(serviceSet, kcmv1.ServiceSetStatusesCollectedCondition)
	initialConditionStatus := statusesCollectedCondition.Status

	status := metav1.ConditionFalse
	reason := kcmv1.ServiceSetStatusesNotCollectedReason
	message := kcmv1.ServiceSetStatusesNotCollectedMessage

	defer func(initialStatus metav1.ConditionStatus) {
		updated := updateCondition(serviceSet, statusesCollectedCondition, status, reason, message, r.timeFunc())
		// we'll emit event only if the status changed from the initial value and it's now true.
		conditionStatusChangedToTrue := initialStatus != status && status == metav1.ConditionTrue
		// kcm emits the event: it owns ServiceSet events since the provider moved out of tree.
		if conditionStatusChangedToTrue && updated {
			l.Info("Successfully collected services statuses")
		}
		l.V(1).Info("Finished services status collection", "duration", time.Since(start))
	}(initialConditionStatus)

	if serviceSet.Spec.Provider.SelfManagement {
		clusterProfile := new(addoncontrollerv1beta1.ClusterProfile)
		key := client.ObjectKeyFromObject(serviceSet)
		if err := rgnClient.Get(ctx, key, clusterProfile); err != nil {
			return fmt.Errorf("failed to get ClusterProfile: %w", err)
		}

		l.V(1).Info("Found matching ClusterProfile", "ClusterProfile", client.ObjectKeyFromObject(clusterProfile))
		err = collectServiceStatusesFromProfileOrClusterProfile(ctx, rgnClient, serviceSet, clusterProfile)
	} else {
		profile := new(addoncontrollerv1beta1.Profile)
		key := client.ObjectKeyFromObject(serviceSet)
		if err := rgnClient.Get(ctx, key, profile); err != nil {
			return fmt.Errorf("failed to get Profile: %w", err)
		}

		l.V(1).Info("Found matching Profile", "Profile", client.ObjectKeyFromObject(profile))
		err = collectServiceStatusesFromProfileOrClusterProfile(ctx, rgnClient, serviceSet, profile)
	}
	if err != nil {
		return fmt.Errorf("failed to collect service statuses: %w", err)
	}

	r.updateServicesInReadyStateCondition(serviceSet)

	status = metav1.ConditionTrue
	reason = kcmv1.ServiceSetStatusesCollectedReason
	message = kcmv1.ServiceSetStatusesCollectedMessage
	return nil
}

// updateServicesInReadyStateCondition updates the "ServicesInReadyState" condition.
func (r *Adapter) updateServicesInReadyStateCondition(serviceSet *kcmv1.ServiceSet) {
	deployedCount := 0
	for _, svc := range serviceSet.Status.Services {
		if svc.State == kcmv1.ServiceStateDeployed {
			deployedCount++
		}
	}
	totalCount := len(serviceSet.Spec.Services)
	svcReadyCond := findCondition(serviceSet, kcmv1.ServicesInReadyStateCondition)
	svcReadyStatus := metav1.ConditionTrue
	if deployedCount != totalCount {
		svcReadyStatus = metav1.ConditionFalse
	}

	// update condition only if deployed <= total to avoid scenarios where
	// a service is removed from spec but is still present in the status.
	if deployedCount <= totalCount {
		updateCondition(serviceSet, svcReadyCond, svcReadyStatus, kcmv1.ServicesInReadyStateCondition,
			fmt.Sprintf("%d/%d", deployedCount, totalCount), r.timeFunc())
	}
}

func getClusterSummaryForServiceSet(ctx context.Context, rgnClient client.Client, serviceSet *kcmv1.ServiceSet, profileObj client.Object) (*addoncontrollerv1beta1.ClusterSummary, error) {
	l := ctrl.LoggerFrom(ctx)

	var (
		matchingRefs []corev1.ObjectReference
		profileKind  string
		profileName  string
	)

	switch p := profileObj.(type) {
	case *addoncontrollerv1beta1.Profile:
		matchingRefs = p.Status.MatchingClusterRefs
		profileKind = addoncontrollerv1beta1.ProfileKind
		profileName = p.Name
		l.V(1).Info("Processing Profile", "profile", client.ObjectKeyFromObject(p))
	case *addoncontrollerv1beta1.ClusterProfile:
		matchingRefs = p.Status.MatchingClusterRefs
		profileKind = addoncontrollerv1beta1.ClusterProfileKind
		profileName = p.Name
		l.V(1).Info("Processing ClusterProfile", "clusterProfile", client.ObjectKeyFromObject(p))
	default:
		return nil, fmt.Errorf("unsupported profile type: %T", profileObj)
	}

	if len(matchingRefs) == 0 {
		l.Info("No matching clusters found for ServiceSet")
		serviceSet.Status.Deployed = false
		return nil, errNoMatchingClusters
	}

	// Use the first matching cluster reference for both types because:
	// 1. For Profile type we expect that it will match only a single cluster.
	// 2. While ClusterProfile can match multiple clusters, we are using it
	// only for self-management of the management cluster.
	//
	// TODO(https://github.com/k0rdent/kcm/issues/2083):
	// It seems like there is no reason to use ClusterProfile instead of Profile for self-management
	// other than backwards compatibility. Even then users should simply be able to delete
	// the ClusterProfile created by previous versions. We already broke backward compatibility
	// for MCS by creating Profiles instead of ClusterProfiles via ServiceSet.
	// If we use Profile for self-management, a lot of the code in this file can be simplified.
	// Also note that the status of ServiceSet is not designed to capture statuses from multiple clusters.
	obj := matchingRefs[0]
	isSveltosCluster := obj.APIVersion == libsveltosv1beta1.GroupVersion.WithKind(libsveltosv1beta1.SveltosClusterKind).GroupVersion().String()
	summaryName := clusterops.GetClusterSummaryName(profileKind, profileName, obj.Name, isSveltosCluster)

	summary := new(addoncontrollerv1beta1.ClusterSummary)
	summaryRef := client.ObjectKey{Name: summaryName, Namespace: obj.Namespace}
	if err := rgnClient.Get(ctx, summaryRef, summary); err != nil {
		return nil, fmt.Errorf("failed to get ClusterSummary %s to fetch status: %w", summaryRef.String(), err)
	}
	return summary, nil
}

func collectServiceStatusesFromProfileOrClusterProfile(ctx context.Context, rgnClient client.Client, serviceSet *kcmv1.ServiceSet, profileObj client.Object) (_ error) {
	l := ctrl.LoggerFrom(ctx)

	summary, err := getClusterSummaryForServiceSet(ctx, rgnClient, serviceSet, profileObj)
	if errors.Is(err, errNoMatchingClusters) {
		return nil
	}
	if err != nil {
		return err
	}

	l.V(1).Info("Found matching ClusterSummary", "summary", client.ObjectKeyFromObject(summary))
	serviceSet.Status.Services = servicesStateFromSummary(l, summary, serviceSet)
	serviceSet.Status.Deployed = !slices.ContainsFunc(serviceSet.Status.Services, func(s kcmv1.ServiceState) bool {
		return s.State != kcmv1.ServiceStateDeployed
	})

	return nil
}
