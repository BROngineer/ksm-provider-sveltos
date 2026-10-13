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

package scheme

import (
	kcmv1 "github.com/K0rdent/kcm/api/v1beta1"
	sourcev1 "github.com/fluxcd/source-controller/api/v1"
	addoncontrollerv1beta1 "github.com/projectsveltos/addon-controller/api/v1beta1"
	libsveltosv1beta1 "github.com/projectsveltos/libsveltos/api/v1beta1"
	"k8s.io/apimachinery/pkg/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	clusterapiv1 "sigs.k8s.io/cluster-api/api/core/v1beta2"
)

// Regional returns the scheme for clients of clusters hosting Sveltos objects. It holds only
// the types the adapter reads there, unlike kcm's GetRegionalSchemeWithSveltos which serves
// every kcm controller.
func Regional() (*runtime.Scheme, error) {
	s := runtime.NewScheme()
	for _, add := range []func(*runtime.Scheme) error{
		clientgoscheme.AddToScheme,
		addoncontrollerv1beta1.AddToScheme,
		libsveltosv1beta1.AddToScheme,
		clusterapiv1.AddToScheme,
	} {
		if err := add(s); err != nil {
			return nil, err
		}
	}
	return s, nil
}

// Management returns the scheme for the management cluster client. Without regions the
// management cluster also hosts the Sveltos objects, so it extends Regional.
func Management() (*runtime.Scheme, error) {
	s, err := Regional()
	if err != nil {
		return nil, err
	}
	for _, add := range []func(*runtime.Scheme) error{
		kcmv1.AddToScheme,
		sourcev1.AddToScheme,
	} {
		if err := add(s); err != nil {
			return nil, err
		}
	}
	return s, nil
}
