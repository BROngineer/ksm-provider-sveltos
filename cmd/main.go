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

import (
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/go-logr/logr"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/healthz"
	"sigs.k8s.io/controller-runtime/pkg/log/zap"
	metricsserver "sigs.k8s.io/controller-runtime/pkg/metrics/server"

	"github.com/brongineer/ksm-provider-api/auth"
	"github.com/brongineer/ksm-provider-api/httpserver"
	"github.com/brongineer/ksm-provider-api/notify"
	providerserver "github.com/brongineer/ksm-provider-api/provider/server"
	registryclient "github.com/brongineer/ksm-provider-api/registry/client"
	"github.com/brongineer/ksm-provider-sveltos/internal/scheme"
	"github.com/brongineer/ksm-provider-sveltos/internal/sveltos"
	kubeutil "github.com/brongineer/ksm-provider-sveltos/internal/util/kube"
)

// tokenReviewCacheTTL is below kube-apiserver's own webhook token cache default (2m).
const tokenReviewCacheTTL = time.Minute

type config struct {
	providerName      string
	systemNamespace   string
	podNamespace      string
	deploymentName    string
	kcmServiceAccount string

	listenAddress string
	tlsCertFile   string
	tlsKeyFile    string
	insecure      bool

	registryURL       string
	registryCAFile    string
	registryTokenFile string
	endpoint          string
	endpointCAFile    string
	heartbeatInterval time.Duration
	providerTimeout   time.Duration

	metricsAddress          string
	probeAddress            string
	enableLeaderElection    bool
	enableSveltosExpireCtrl bool
}

func main() {
	var cfg config
	flag.StringVar(&cfg.providerName, "provider-name", "ksm-projectsveltos", "Provider name kcm matches against ServiceSet.spec.provider.name.")
	flag.StringVar(&cfg.systemNamespace, "system-namespace", "kcm-system", "kcm system namespace, where Region kubeconfig Secrets live.")
	flag.StringVar(&cfg.podNamespace, "pod-namespace", os.Getenv("POD_NAMESPACE"), "Namespace of this adapter's Deployment.")
	flag.StringVar(&cfg.deploymentName, "deployment-name", "ksm-provider-sveltos", "Name of this adapter's Deployment, checked on shutdown for scale-to-zero.")
	flag.StringVar(&cfg.kcmServiceAccount, "kcm-service-account", "kcm-system/kcm-controller-manager", "namespace/name of the kcm ServiceAccount allowed to call the adapter.")
	flag.StringVar(&cfg.listenAddress, "listen-address", ":9443", "Address of the provider API server.")
	flag.StringVar(&cfg.tlsCertFile, "tls-cert-file", "/etc/ksm-provider/tls/tls.crt", "Serving certificate of the provider API.")
	flag.StringVar(&cfg.tlsKeyFile, "tls-key-file", "/etc/ksm-provider/tls/tls.key", "Serving key of the provider API.")
	flag.BoolVar(&cfg.insecure, "insecure", false, "Serve the provider API over plain HTTP. Testing and demos only: tokens travel in clear text.")
	flag.StringVar(&cfg.registryURL, "registry-url", "", "Base URL of the kcm registry.")
	flag.StringVar(&cfg.registryCAFile, "registry-ca-file", "", "CA bundle verifying the kcm registry; empty uses system roots.")
	flag.StringVar(&cfg.registryTokenFile, "registry-token-file", "/var/run/secrets/ksm/registry/token", "Projected ServiceAccount token for the registry audience.")
	flag.StringVar(&cfg.endpoint, "endpoint", "", "Base URL kcm uses to reach this adapter.")
	flag.StringVar(&cfg.endpointCAFile, "endpoint-ca-file", "/etc/ksm-provider/tls/ca.crt", "CA bundle kcm uses to verify this adapter.")
	flag.DurationVar(&cfg.heartbeatInterval, "heartbeat-interval", 10*time.Second, "Interval between registry heartbeats.")
	flag.DurationVar(&cfg.providerTimeout, "provider-timeout", 0, "Timeout kcm should use per call; zero leaves the registry default.")
	flag.StringVar(&cfg.metricsAddress, "metrics-bind-address", ":8080", "Metrics endpoint address; \"0\" disables it.")
	flag.StringVar(&cfg.probeAddress, "health-probe-bind-address", ":8081", "Health probe address.")
	flag.BoolVar(&cfg.enableLeaderElection, "leader-elect", true, "Enable leader election.")
	flag.BoolVar(&cfg.enableSveltosExpireCtrl, "enable-sveltos-expire-ctrl", false, "Renew tokens of the management SveltosCluster.")
	opts := zap.Options{}
	opts.BindFlags(flag.CommandLine)
	flag.Parse()

	ctrl.SetLogger(zap.New(zap.UseFlagOptions(&opts)))
	if err := run(cfg); err != nil {
		ctrl.Log.Error(err, "adapter failed")
		os.Exit(1)
	}
}

func run(cfg config) error {
	if cfg.registryURL == "" || cfg.endpoint == "" || cfg.podNamespace == "" {
		return errors.New("--registry-url, --endpoint and --pod-namespace (or POD_NAMESPACE) are required")
	}
	if err := validateEndpoint(cfg.endpoint, cfg.insecure); err != nil {
		return err
	}
	kcmNamespace, kcmName, ok := strings.Cut(cfg.kcmServiceAccount, "/")
	if !ok {
		return fmt.Errorf("--kcm-service-account must be namespace/name, got %q", cfg.kcmServiceAccount)
	}

	s, err := scheme.Management()
	if err != nil {
		return fmt.Errorf("build scheme: %w", err)
	}
	mgr, err := ctrl.NewManager(ctrl.GetConfigOrDie(), ctrl.Options{
		Scheme:                        s,
		Metrics:                       metricsserver.Options{BindAddress: cfg.metricsAddress},
		HealthProbeBindAddress:        cfg.probeAddress,
		LeaderElection:                cfg.enableLeaderElection,
		LeaderElectionID:              cfg.providerName + ".ksm.k0rdent.mirantis.com",
		LeaderElectionReleaseOnCancel: true, // the Suspended heartbeat is sent before the lease is released
	})
	if err != nil {
		return fmt.Errorf("create manager: %w", err)
	}
	if err := mgr.AddHealthzCheck("healthz", healthz.Ping); err != nil {
		return err
	}
	if err := mgr.AddReadyzCheck("readyz", healthz.Ping); err != nil {
		return err
	}
	if err := mgr.Add(kubeutil.RESTMapperCacheSweeper()); err != nil {
		return fmt.Errorf("add REST mapper cache sweeper: %w", err)
	}

	cs, err := kubernetes.NewForConfig(mgr.GetConfig())
	if err != nil {
		return fmt.Errorf("create clientset: %w", err)
	}
	logger := slog.New(logr.ToSlogHandler(ctrl.Log.WithName("ksm")))

	adapter := sveltos.NewAdapter(mgr.GetClient(), cfg.systemNamespace)
	handler, err := providerserver.NewHandler(providerserver.HandlerConfig{
		Provider:       adapter,
		Authenticator:  auth.NewCachedAuthenticator(auth.NewTokenReviewAuthenticator(cs, auth.ProviderAudience), tokenReviewCacheTTL),
		AllowedCallers: []string{auth.ServiceAccountUsername(kcmNamespace, kcmName)},
		Logger:         logger,
	})
	if err != nil {
		return fmt.Errorf("create provider handler: %w", err)
	}
	if err := mgr.Add(&httpserver.Server{
		Handler:  handler,
		Addr:     cfg.listenAddress,
		CertFile: cfg.tlsCertFile,
		KeyFile:  cfg.tlsKeyFile,
		Insecure: cfg.insecure,
		Logger:   logger,
	}); err != nil {
		return fmt.Errorf("add provider server: %w", err)
	}

	registryCA, err := readOptionalFile(cfg.registryCAFile)
	if err != nil {
		return err
	}
	// A plain-HTTP endpoint has no certificate to verify, so no CA is announced.
	var endpointCA []byte
	if !cfg.insecure {
		if endpointCA, err = readOptionalFile(cfg.endpointCAFile); err != nil {
			return err
		}
	}
	heartbeater, err := registryclient.NewHeartbeater(registryclient.Config{
		RegistryURL:      cfg.registryURL,
		RegistryCABundle: registryCA,
		TokenFile:        cfg.registryTokenFile,
		Name:             cfg.providerName,
		Endpoint:         cfg.endpoint,
		EndpointCABundle: endpointCA,
		APIVersions:      []string{"v1"},
		ProviderTimeout:  cfg.providerTimeout,
		Interval:         cfg.heartbeatInterval,
		SuspendCheck: registryclient.DeploymentScaledToZero(cs, types.NamespacedName{
			Namespace: cfg.podNamespace, Name: cfg.deploymentName,
		}),
		Logger: logger,
	})
	if err != nil {
		return fmt.Errorf("create heartbeater: %w", err)
	}
	if err := mgr.Add(heartbeater); err != nil {
		return fmt.Errorf("add heartbeater: %w", err)
	}

	recorder, stopRecorder := notify.NewRecorder(cs, mgr.GetScheme(), cfg.providerName)
	defer stopRecorder()
	if err := sveltos.SetupPoller(mgr, recorder, heartbeater.Registered(), cfg.systemNamespace, cfg.providerName); err != nil {
		return err
	}

	if cfg.enableSveltosExpireCtrl {
		if err := (&sveltos.ClusterReconciler{Client: mgr.GetClient()}).SetupWithManager(mgr); err != nil {
			return fmt.Errorf("set up SveltosCluster controller: %w", err)
		}
	}

	return mgr.Start(ctrl.SetupSignalHandler())
}

// validateEndpoint rejects an endpoint whose scheme does not match the serving mode: kcm calls the
// announced URL as is, so a mismatch fails every call instead of failing at startup.
func validateEndpoint(endpoint string, insecure bool) error {
	u, err := url.Parse(endpoint)
	if err != nil || u.Host == "" {
		return fmt.Errorf("--endpoint %q must be an absolute URL", endpoint)
	}
	want := "https"
	if insecure {
		want = "http"
	}
	if u.Scheme != want {
		return fmt.Errorf("--endpoint %q must use %s with --insecure=%v", endpoint, want, insecure)
	}
	return nil
}

func readOptionalFile(path string) ([]byte, error) {
	if path == "" {
		return nil, nil
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", path, err)
	}
	return data, nil
}
