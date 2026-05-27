/*
Copyright 2026 The Kilo Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package main

import (
	"context"
	"crypto/tls"
	"flag"
	"log/slog"
	"os"

	// Import all Kubernetes client auth plugins (e.g. Azure, GCP, OIDC, etc.)
	// to ensure that exec-entrypoint and run can make use of them.
	_ "k8s.io/client-go/plugin/pkg/client/auth"

	"github.com/cockroachdb/errors"
	"k8s.io/apimachinery/pkg/runtime"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/rest"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/healthz"
	"sigs.k8s.io/controller-runtime/pkg/log/zap"
	"sigs.k8s.io/controller-runtime/pkg/manager"
	"sigs.k8s.io/controller-runtime/pkg/metrics/filters"
	metricsserver "sigs.k8s.io/controller-runtime/pkg/metrics/server"
	"sigs.k8s.io/controller-runtime/pkg/webhook"

	kilov1alpha1 "github.com/squat/kilo-clustermesh-operator/api/v1alpha1"
	"github.com/squat/kilo-clustermesh-operator/internal/controller"
	"github.com/squat/kilo-clustermesh-operator/internal/crd"
	"github.com/squat/kilo-clustermesh-operator/internal/multicluster"
	"github.com/squat/kilo-clustermesh-operator/internal/restart"
	kilopeerv1alpha1 "github.com/squat/kilo-clustermesh-operator/pkg/kilo/v1alpha1"
	// +kubebuilder:scaffold:imports
)

const (
	podNamespaceEnv     = "POD_NAMESPACE"
	leaderElectionID    = "f27237f1.squat.ai"
	controllerEventName = "clustermesh-controller"
)

// version and revision are set at build time via -X linker flags:
//
//	-X main.version=${VERSION} -X main.revision=${REVISION}
//
// They default to the zero string when not provided (e.g. in local dev builds).
var (
	version  string
	revision string
)

var (
	scheme   = runtime.NewScheme()
	setupLog = ctrl.Log.WithName("setup")
)

func init() {
	utilruntime.Must(clientgoscheme.AddToScheme(scheme))

	utilruntime.Must(kilov1alpha1.AddToScheme(scheme))
	utilruntime.Must(kilopeerv1alpha1.AddToScheme(scheme))
	// +kubebuilder:scaffold:scheme
}

func main() {
	if err := run(); err != nil {
		setupLog.Error(err, "operator exited with error")
		os.Exit(1)
	}
}

func run() error {
	opts := parseFlags()

	ctrl.SetLogger(zap.New(zap.UseFlagOptions(&opts.zapOpts)))
	slogger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelInfo}))

	namespace, err := readNamespace()
	if err != nil {
		return err
	}

	cfg := ctrl.GetConfigOrDie()

	ctx, cancel := context.WithCancel(ctrl.SetupSignalHandler())
	defer cancel()

	if err := crd.InstallOrUpdate(ctx, cfg); err != nil {
		return errors.Wrap(err, "installing CRD")
	}

	registry, err := buildInitialRegistry(ctx, cfg, slogger)
	if err != nil {
		return errors.Wrap(err, "building cluster registry")
	}

	mgr, err := newManager(cfg, &opts)
	if err != nil {
		return err
	}

	for name, c := range registry.All() {
		if err := mgr.Add(c); err != nil {
			return errors.Wrapf(err, "registering cluster %q with manager", name)
		}
	}

	if err := wireReconciler(mgr, registry, slogger, cancel); err != nil {
		return err
	}

	if err := wireChangeWatcher(ctx, mgr, slogger, cancel); err != nil {
		return err
	}

	if err := mgr.AddHealthzCheck("healthz", healthz.Ping); err != nil {
		return errors.Wrap(err, "setting up health check")
	}

	if err := mgr.AddReadyzCheck("readyz", healthz.Ping); err != nil {
		return errors.Wrap(err, "setting up ready check")
	}

	setupLog.Info("Starting manager",
		"namespace", namespace,
		"clusters", registry.Clusters(),
		"version", version,
		"revision", revision,
	)

	if err := mgr.Start(ctx); err != nil {
		return errors.Wrap(err, "manager exited with error")
	}

	return nil
}

type runtimeOpts struct {
	metricsAddr          string
	probeAddr            string
	metricsCertPath      string
	metricsCertName      string
	metricsCertKey       string
	webhookCertPath      string
	webhookCertName      string
	webhookCertKey       string
	enableLeaderElection bool
	secureMetrics        bool
	enableHTTP2          bool
	zapOpts              zap.Options
}

func parseFlags() runtimeOpts {
	opts := runtimeOpts{zapOpts: zap.Options{Development: true}}

	flag.StringVar(&opts.metricsAddr, "metrics-bind-address", "0",
		"The address the metrics endpoint binds to. Use :8443 for HTTPS or :8080 for HTTP, or 0 to disable.")
	flag.StringVar(&opts.probeAddr, "health-probe-bind-address", ":8081",
		"The address the probe endpoint binds to.")
	flag.BoolVar(&opts.enableLeaderElection, "leader-elect", false,
		"Enable leader election for controller manager.")
	flag.BoolVar(&opts.secureMetrics, "metrics-secure", true,
		"If set, the metrics endpoint is served securely via HTTPS.")
	flag.StringVar(&opts.webhookCertPath, "webhook-cert-path", "",
		"The directory that contains the webhook certificate.")
	flag.StringVar(&opts.webhookCertName, "webhook-cert-name", "tls.crt",
		"The name of the webhook certificate file.")
	flag.StringVar(&opts.webhookCertKey, "webhook-cert-key", "tls.key",
		"The name of the webhook key file.")
	flag.StringVar(&opts.metricsCertPath, "metrics-cert-path", "",
		"The directory that contains the metrics server certificate.")
	flag.StringVar(&opts.metricsCertName, "metrics-cert-name", "tls.crt",
		"The name of the metrics server certificate file.")
	flag.StringVar(&opts.metricsCertKey, "metrics-cert-key", "tls.key",
		"The name of the metrics server key file.")
	flag.BoolVar(&opts.enableHTTP2, "enable-http2", false,
		"If set, HTTP/2 will be enabled for the metrics and webhook servers.")
	opts.zapOpts.BindFlags(flag.CommandLine)
	flag.Parse()

	return opts
}

// readNamespace reads the operator's own namespace, set via the POD_NAMESPACE
// env var (downward API) at deploy time.
func readNamespace() (string, error) {
	ns := os.Getenv(podNamespaceEnv)
	if ns == "" {
		return "", errors.Newf("%s environment variable is required", podNamespaceEnv)
	}

	return ns, nil
}

// buildInitialRegistry lists all ClusterMesh resources in the operator's
// namespace and constructs a registry that holds clients for every declared
// cluster. If no ClusterMesh resources exist yet, an empty registry is
// returned and the change-watcher will trigger a restart once one is created.
func buildInitialRegistry(ctx context.Context, cfg *rest.Config, log *slog.Logger) (*multicluster.ClusterRegistry, error) {
	preClient, err := client.New(cfg, client.Options{Scheme: scheme})
	if err != nil {
		return nil, errors.Wrap(err, "building pre-manager client")
	}

	var meshes kilov1alpha1.ClusterMeshList
	if err := preClient.List(ctx, &meshes); err != nil {
		return nil, errors.Wrap(err, "listing ClusterMesh resources")
	}

	entries := mergeClusterEntries(meshes.Items)

	registry, err := multicluster.Build(ctx, entries, cfg, preClient, scheme, log)

	return registry, errors.Wrap(err, "constructing registry")
}

// mergeClusterEntries collapses every cluster entry across every ClusterMesh
// into a single list, deduplicating by cluster name (first occurrence wins).
// Each entry is tagged with the namespace of the ClusterMesh it came from so
// the kubeconfig Secret can be resolved without an explicit namespace field
// on KubeconfigSecretRef.
func mergeClusterEntries(meshes []kilov1alpha1.ClusterMesh) []multicluster.EntrySource {
	seen := make(map[string]struct{})

	entries := make([]multicluster.EntrySource, 0)

	for i := range meshes {
		for j := range meshes[i].Spec.Clusters {
			entry := meshes[i].Spec.Clusters[j]
			if _, dup := seen[entry.Name]; dup {
				continue
			}

			seen[entry.Name] = struct{}{}
			entries = append(entries, multicluster.EntrySource{
				Entry:         entry,
				MeshNamespace: meshes[i].Namespace,
			})
		}
	}

	return entries
}

func newManager(cfg *rest.Config, opts *runtimeOpts) (manager.Manager, error) {
	var tlsOpts []func(*tls.Config)

	disableHTTP2 := func(c *tls.Config) {
		setupLog.Info("Disabling HTTP/2")

		c.NextProtos = []string{"http/1.1"}
	}

	if !opts.enableHTTP2 {
		tlsOpts = append(tlsOpts, disableHTTP2)
	}

	webhookServerOptions := webhook.Options{TLSOpts: tlsOpts}

	if opts.webhookCertPath != "" {
		webhookServerOptions.CertDir = opts.webhookCertPath
		webhookServerOptions.CertName = opts.webhookCertName
		webhookServerOptions.KeyName = opts.webhookCertKey
	}

	metricsServerOptions := metricsserver.Options{
		BindAddress:   opts.metricsAddr,
		SecureServing: opts.secureMetrics,
		TLSOpts:       tlsOpts,
	}

	if opts.secureMetrics {
		metricsServerOptions.FilterProvider = filters.WithAuthenticationAndAuthorization
	}

	if opts.metricsCertPath != "" {
		metricsServerOptions.CertDir = opts.metricsCertPath
		metricsServerOptions.CertName = opts.metricsCertName
		metricsServerOptions.KeyName = opts.metricsCertKey
	}

	mgr, err := ctrl.NewManager(cfg, ctrl.Options{
		Scheme:                 scheme,
		Metrics:                metricsServerOptions,
		WebhookServer:          webhook.NewServer(webhookServerOptions),
		HealthProbeBindAddress: opts.probeAddr,
		LeaderElection:         opts.enableLeaderElection,
		LeaderElectionID:       leaderElectionID,
		// The manager's cache watches namespaced types we own
		// (ClusterMesh + Secret) cluster-wide so users can place
		// ClusterMesh CRs alongside whichever Secret the Kubernetes
		// distribution emits (e.g. Cozystack/Kamaji puts tenant
		// admin-kubeconfig Secrets in tenant-root).
	})

	return mgr, errors.Wrap(err, "creating manager")
}

func wireReconciler(mgr manager.Manager, registry *multicluster.ClusterRegistry, slogger *slog.Logger, cancel context.CancelFunc) error {
	r := &controller.ClusterMeshReconciler{
		Client:   mgr.GetClient(),
		Scheme:   mgr.GetScheme(),
		Registry: registry,
		Log:      slogger,
		Recorder: mgr.GetEventRecorder(controllerEventName),
		Cancel:   cancel,
	}

	return errors.Wrap(r.SetupWithManager(mgr), "registering ClusterMesh reconciler")
}

func wireChangeWatcher(
	ctx context.Context,
	mgr manager.Manager,
	slogger *slog.Logger,
	cancel context.CancelFunc,
) error {
	preClient, err := client.New(mgr.GetConfig(), client.Options{Scheme: scheme})
	if err != nil {
		return errors.Wrap(err, "building pre-manager client for fingerprint")
	}

	watcher := &restart.ChangeWatcher{
		Client: mgr.GetClient(),
		Log:    slogger,
		Cancel: cancel,
	}

	bootstrap := &restart.ChangeWatcher{
		Client: preClient,
		Log:    slogger,
	}

	fingerprint, err := bootstrap.ComputeFingerprint(ctx)
	if err != nil {
		return errors.Wrap(err, "computing start fingerprint")
	}

	watcher.StartFingerprint = fingerprint

	return errors.Wrap(watcher.SetupWithManager(mgr), "registering change-watcher")
}
