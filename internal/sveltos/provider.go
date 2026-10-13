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
	"fmt"
	"time"

	kcmv1 "github.com/K0rdent/kcm/api/v1beta1"
	addoncontrollerv1beta1 "github.com/projectsveltos/addon-controller/api/v1beta1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/brongineer/ksm-provider-api/api/provider"
	providerserver "github.com/brongineer/ksm-provider-api/provider/server"
)

var _ providerserver.Provider = (*Adapter)(nil)

func NewAdapter(c client.Client, systemNamespace string) *Adapter {
	return &Adapter{Client: c, SystemNamespace: systemNamespace, timeFunc: time.Now}
}

// DeployServices runs what kcm's in-tree reconciler did for a ServiceSet up to and excluding
// the status write: ensure the Profile, collect service states, fingerprint deployed charts.
// The conditions those steps set on the in-memory ServiceSet are translated into the result.
func (r *Adapter) DeployServices(ctx context.Context, req provider.DeployServicesRequest) (provider.DeployServicesResult, error) {
	l := ctrl.LoggerFrom(ctx).WithValues("serviceSet", client.ObjectKey{Namespace: req.Namespace, Name: req.Name})

	serviceSet, err := r.getServiceSet(ctx, req.Namespace, req.Name)
	if err != nil {
		return provider.DeployServicesResult{}, err
	}
	if serviceSet.Generation < req.Generation {
		return provider.DeployServicesResult{}, fmt.Errorf("read generation %d, requested %d: %w",
			serviceSet.Generation, req.Generation, providerserver.ErrStaleGeneration)
	}

	rgnClient, err := getRegionalClient(ctx, r.Client, serviceSet, r.SystemNamespace)
	if err != nil {
		return provider.DeployServicesResult{}, fmt.Errorf("failed to get regional client: %w", err)
	}

	result := provider.DeployServicesResult{ObservedGeneration: serviceSet.Generation}

	if err := r.ensureProfile(ctx, rgnClient, serviceSet); err != nil {
		l.Error(err, "failed to ensure Profile")
		cond := findCondition(serviceSet, kcmv1.ServiceSetProfileCondition)
		result.Reason = new(failureReason(cond.Reason))
		result.Message = new(cond.Message)
		result.Services = toServiceStates(serviceSet.Status.Services, nil)
		return result, nil
	}
	result.Applied = true

	// As in kcm, health verification (and with it fingerprinting) only follows a successful collection.
	if err := r.collectServiceStatuses(ctx, rgnClient, serviceSet); err != nil {
		l.Error(err, "failed to collect service statuses")
		cond := findCondition(serviceSet, kcmv1.ServiceSetStatusesCollectedCondition)
		result.CollectMessage = new(cond.Message)
		result.Services = toServiceStates(serviceSet.Status.Services, nil)
		return result, nil
	}
	result.Collected = true

	var hashes map[client.ObjectKey]string
	summary, cc, profileKind, profileName, err := fetchHelmArtifactsForVerifier(ctx, rgnClient, serviceSet)
	if err != nil {
		// Best effort, as kcm's verifier: no fingerprint this round, the next call retries.
		l.Error(err, "failed to fetch Helm artifacts for fingerprinting")
	} else {
		hashes = serviceHashesFromArtifacts(summary, cc, profileKind, profileName)
	}

	result.Services = toServiceStates(serviceSet.Status.Services, hashes)
	return result, nil
}

// DeleteServices removes the Profile (or ClusterProfile) of a ServiceSet being deleted, as the
// Profile part of kcm's reconcileDelete did.
func (r *Adapter) DeleteServices(ctx context.Context, req provider.DeleteServicesRequest) (provider.DeleteServicesResult, error) {
	serviceSet, err := r.getServiceSet(ctx, req.Namespace, req.Name)
	if err != nil {
		return provider.DeleteServicesResult{}, err
	}

	rgnClient, err := getRegionalClient(ctx, r.Client, serviceSet, r.SystemNamespace)
	if err != nil {
		return provider.DeleteServicesResult{}, fmt.Errorf("failed to get regional client: %w", err)
	}

	var profile client.Object
	if serviceSet.Spec.Provider.SelfManagement {
		profile = new(addoncontrollerv1beta1.ClusterProfile)
	} else {
		profile = new(addoncontrollerv1beta1.Profile)
	}

	key := client.ObjectKeyFromObject(serviceSet)
	err = rgnClient.Get(ctx, key, profile)
	if client.IgnoreNotFound(err) != nil {
		return provider.DeleteServicesResult{}, fmt.Errorf("failed to get Profile: %w", err)
	}
	if apierrors.IsNotFound(err) {
		return provider.DeleteServicesResult{Done: true}, nil
	}

	if profile.GetDeletionTimestamp().IsZero() {
		ctrl.LoggerFrom(ctx).Info("Deleting Profile", "profile", profile.GetName())
		if err := rgnClient.Delete(ctx, profile); client.IgnoreNotFound(err) != nil {
			return provider.DeleteServicesResult{}, fmt.Errorf("failed to delete Profile: %w", err)
		}
	}
	return provider.DeleteServicesResult{Done: false}, nil
}

func (r *Adapter) getServiceSet(ctx context.Context, namespace, name string) (*kcmv1.ServiceSet, error) {
	serviceSet := new(kcmv1.ServiceSet)
	key := client.ObjectKey{Namespace: namespace, Name: name}
	if err := r.Get(ctx, key, serviceSet); err != nil {
		if apierrors.IsNotFound(err) {
			return nil, fmt.Errorf("ServiceSet %s: %w", key, providerserver.ErrNotFound)
		}
		return nil, fmt.Errorf("failed to get ServiceSet %s: %w", key, err)
	}
	return serviceSet, nil
}

// failureReason maps the ServiceSetProfile condition reason ensureProfile sets onto the
// contract's reason enum. Lookup and create/update failures stay at NotReady in kcm's code.
func failureReason(profileConditionReason string) provider.DeployServicesResultReason {
	switch profileConditionReason {
	case kcmv1.ServiceSetProfileBuildFailedReason:
		return provider.DeployServicesResultReasonInvalidInput
	case kcmv1.ServiceSetHelmChartsBuildFailedReason,
		kcmv1.ServiceSetKustomizationRefsBuildFailedReason,
		kcmv1.ServiceSetPolicyRefsBuildFailedReason:
		return provider.DeployServicesResultReasonSourceUnavailable
	default:
		return provider.DeployServicesResultReasonProviderError
	}
}

// toServiceStates keys hashes like kcm's verifier does: by the service's own namespace and name.
func toServiceStates(states []kcmv1.ServiceState, hashes map[client.ObjectKey]string) []provider.ServiceState {
	out := make([]provider.ServiceState, 0, len(states))
	for _, s := range states {
		state := provider.ServiceState{
			Name:             s.Name,
			Namespace:        s.Namespace,
			Template:         s.Template,
			Type:             provider.ServiceStateType(s.Type),
			State:            provider.ServiceStateState(s.State),
			Version:          nonEmpty(s.Version),
			FailureMessage:   nonEmpty(s.FailureMessage),
			LastDeployedHash: nonEmpty(s.LastDeployedHash),
			DeployedHash:     nonEmpty(hashes[client.ObjectKey{Namespace: s.Namespace, Name: s.Name}]),
		}
		if s.LastStateTransitionTime != nil {
			state.LastStateTransitionTime = new(s.LastStateTransitionTime.Time)
		}
		out = append(out, state)
	}
	return out
}

func nonEmpty(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}
