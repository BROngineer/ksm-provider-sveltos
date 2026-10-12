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
	"testing"

	kcmv1 "github.com/K0rdent/kcm/api/v1beta1"
	"github.com/stretchr/testify/assert"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

func Test_FullyDeployed(t *testing.T) {
	t.Parallel()

	serviceSet := func(deployed bool, specVer, state, statusVer string) *kcmv1.ServiceSet {
		return &kcmv1.ServiceSet{
			Spec: kcmv1.ServiceSetSpec{Services: []kcmv1.ServiceWithValues{
				{Name: "cert-manager", Namespace: "cert-manager", Version: specVer},
			}},
			Status: kcmv1.ServiceSetStatus{
				Deployed: deployed,
				Services: []kcmv1.ServiceState{
					{Name: "cert-manager", Namespace: "cert-manager", State: state, Version: statusVer},
				},
			},
		}
	}

	for _, tc := range []struct {
		name string
		ss   *kcmv1.ServiceSet
		want bool
	}{
		{
			name: "not deployed",
			ss:   serviceSet(false, "1.20.3", kcmv1.ServiceStateProvisioning, "1.20.2"),
			want: false,
		},
		{
			name: "deployed and at the version the spec asks for",
			ss:   serviceSet(true, "1.20.3", kcmv1.ServiceStateDeployed, "1.20.3"),
			want: true,
		},
		{
			name: "deployed but the version still trails the spec",
			ss:   serviceSet(true, "1.20.3", kcmv1.ServiceStateDeployed, "1.20.2"),
			want: false,
		},
		{
			name: "deployed with nothing confirmed yet",
			ss:   serviceSet(true, "1.20.3", kcmv1.ServiceStateDeployed, ""),
			want: false,
		},
		{
			name: "a service not reported as Deployed confirms nothing",
			ss:   serviceSet(true, "1.20.3", kcmv1.ServiceStateProvisioning, "1.20.3"),
			want: false,
		},
		{
			name: "spec carries no version to compare against",
			ss:   serviceSet(true, "", kcmv1.ServiceStateDeployed, "1.20.2"),
			want: true,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tc.want, FullyDeployed(tc.ss))
		})
	}
}

func Test_ServiceKey(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name      string
		namespace string
		want      client.ObjectKey
	}{
		{name: "explicit namespace", namespace: "ingress", want: client.ObjectKey{Namespace: "ingress", Name: "nginx"}},
		{name: "empty namespace falls back to default", namespace: "", want: client.ObjectKey{Namespace: "default", Name: "nginx"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tc.want, ServiceKey(tc.namespace, "nginx"))
		})
	}
}
