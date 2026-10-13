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

package serviceset

import (
	kcmv1 "github.com/K0rdent/kcm/api/v1beta1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// effectiveNamespace falls back to "default" namespace in case provided service namespace is empty.
func effectiveNamespace(serviceNamespace string) string {
	if serviceNamespace == "" {
		return metav1.NamespaceDefault
	}
	return serviceNamespace
}

// DeployedVersions indexes, per service, the version last confirmed on the
// cluster. Only services the provider reports as Deployed are recorded: one
// still provisioning has confirmed nothing, and a missing entry is what tells
// callers so.
func DeployedVersions(serviceSet *kcmv1.ServiceSet) map[client.ObjectKey]string {
	versions := make(map[client.ObjectKey]string, len(serviceSet.Status.Services))
	for _, state := range serviceSet.Status.Services {
		if state.State != kcmv1.ServiceStateDeployed || state.Version == "" {
			continue
		}
		versions[ServiceKey(state.Namespace, state.Name)] = state.Version
	}
	return versions
}

// FullyDeployed reports whether every service has reached the version its spec
// asks for.
//
// Stricter than Status.Deployed, which only says the provider is done with what
// it was last handed. The verifier can mark a service Deployed on the
// fingerprint of the version it is upgrading away from, leaving Status.Version
// behind Spec.Version and the stamp that advances it still owed - a state that
// looks finished but is not.
func FullyDeployed(serviceSet *kcmv1.ServiceSet) bool {
	if !serviceSet.Status.Deployed {
		return false
	}

	deployed := DeployedVersions(serviceSet)
	for _, svc := range serviceSet.Spec.Services {
		if svc.Version == "" {
			continue
		}
		if deployed[ServiceKey(svc.Namespace, svc.Name)] != svc.Version {
			return false
		}
	}
	return true
}

// ServiceKey returns a unique identifier for a service
// within [github.com/K0rdent/kcm/api/v1beta1.ServiceSpec].
func ServiceKey(namespace, name string) client.ObjectKey {
	return client.ObjectKey{
		Namespace: effectiveNamespace(namespace),
		Name:      name,
	}
}
