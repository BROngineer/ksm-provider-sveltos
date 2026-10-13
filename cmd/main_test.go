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

package main

import "testing"

func TestValidateEndpoint(t *testing.T) {
	tests := []struct {
		name     string
		endpoint string
		insecure bool
		wantErr  bool
	}{
		{"https endpoint with TLS", "https://ksm.projectsveltos.svc:9443", false, false},
		{"http endpoint in insecure mode", "http://ksm.projectsveltos.svc:9443", true, false},
		{"http endpoint with TLS", "http://ksm.projectsveltos.svc:9443", false, true},
		{"https endpoint in insecure mode", "https://ksm.projectsveltos.svc:9443", true, true},
		{"relative endpoint", "/provider", false, true},
		{"unsupported scheme", "ftp://ksm.projectsveltos.svc", false, true},
		{"no host", "https://", false, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := validateEndpoint(tt.endpoint, tt.insecure)
			if (err != nil) != tt.wantErr {
				t.Errorf("validateEndpoint(%q, insecure=%v) error = %v, wantErr %v", tt.endpoint, tt.insecure, err, tt.wantErr)
			}
		})
	}
}
