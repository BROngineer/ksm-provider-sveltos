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
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/brongineer/ksm-provider-api/api/provider"
	providerserver "github.com/brongineer/ksm-provider-api/provider/server"
)

var _ providerserver.Provider = (*Adapter)(nil)

func NewAdapter(c client.Client, systemNamespace string) *Adapter {
	return &Adapter{}
}

// DeployServices runs what kcm's in-tree reconciler did for a ServiceSet up to and excluding
// the status write: ensure the Profile, collect service states, fingerprint deployed charts.
// The conditions those steps set on the in-memory ServiceSet are translated into the result.
func (r *Adapter) DeployServices(ctx context.Context, req provider.DeployServicesRequest) (provider.DeployServicesResult, error) {
	return provider.DeployServicesResult{}, nil
}

// DeleteServices removes the Profile (or ClusterProfile) of a ServiceSet being deleted.
func (r *Adapter) DeleteServices(ctx context.Context, req provider.DeleteServicesRequest) (provider.DeleteServicesResult, error) {
	return provider.DeleteServicesResult{}, nil
}

func failureReason(profileConditionReason string) provider.DeployServicesResultReason {
	return ""
}

func toServiceStates(states []kcmv1.ServiceState, hashes map[client.ObjectKey]string) []provider.ServiceState {
	return nil
}
