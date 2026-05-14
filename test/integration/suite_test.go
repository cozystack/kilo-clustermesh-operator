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

package integration

import (
	"context"
	"log/slog"
	"os"
	"path/filepath"
	"testing"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/tools/record"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/envtest"
	logrzap "sigs.k8s.io/controller-runtime/pkg/log/zap"

	v1alpha1 "github.com/squat/kilo-clustermesh-operator/api/v1alpha1"
	"github.com/squat/kilo-clustermesh-operator/internal/controller"
	"github.com/squat/kilo-clustermesh-operator/internal/multicluster"
	kilov1alpha1 "github.com/squat/kilo-clustermesh-operator/pkg/kilo/v1alpha1"
)

// testEnv holds the two envtest environments and the clients/reconciler shared
// across all integration tests in this package.
type testEnv struct {
	localEnv     *envtest.Environment
	remoteEnv    *envtest.Environment
	localClient  client.Client
	remoteClient client.Client
	reconciler   *controller.ClusterMeshReconciler
	scheme       *runtime.Scheme
}

// globalEnv is initialised once in TestMain and shared across all tests.
var globalEnv testEnv

// TestMain starts both envtest environments, installs CRDs, builds clients and
// a ClusterMeshReconciler, then runs all tests in this package.
func TestMain(m *testing.M) {
	ctrl.SetLogger(logrzap.New(logrzap.UseDevMode(true)))

	scheme := buildScheme()

	// Locate testdata and the project CRD directory.
	testdataDir := filepath.Join("testdata")
	crdDir := filepath.Join("..", "..", "config", "crd", "bases")

	// ---- local envtest: ClusterMesh CRD + Peer CRD ----
	localEnv := &envtest.Environment{
		CRDDirectoryPaths: []string{crdDir, testdataDir},
		Scheme:            scheme,
	}

	localCfg, err := localEnv.Start()
	if err != nil {
		panic("starting local envtest: " + err.Error())
	}

	// ---- remote envtest: Peer CRD only ----
	remoteEnv := &envtest.Environment{
		CRDDirectoryPaths: []string{testdataDir},
		Scheme:            scheme,
	}

	remoteCfg, err := remoteEnv.Start()
	if err != nil {
		_ = localEnv.Stop()
		panic("starting remote envtest: " + err.Error())
	}

	localClient, err := client.New(localCfg, client.Options{Scheme: scheme})
	if err != nil {
		panic("building local client: " + err.Error())
	}

	remoteClient, err := client.New(remoteCfg, client.Options{Scheme: scheme})
	if err != nil {
		panic("building remote client: " + err.Error())
	}

	// The registry maps cluster names → clients.  Tests will construct a mesh
	// spec whose cluster names match these keys.
	registry := multicluster.NewFromClients(map[string]client.Client{
		"local":  localClient,
		"remote": remoteClient,
	}, "local")

	reconciler := &controller.ClusterMeshReconciler{
		Client:   localClient,
		Scheme:   scheme,
		Registry: registry,
		Log:      slog.Default(),
		Recorder: record.NewFakeRecorder(100),
	}

	globalEnv = testEnv{
		localEnv:     localEnv,
		remoteEnv:    remoteEnv,
		localClient:  localClient,
		remoteClient: remoteClient,
		reconciler:   reconciler,
		scheme:       scheme,
	}

	// Ensure there is a default namespace available.
	ctx := context.Background()
	ensureNamespace(ctx, localClient, "default")

	code := m.Run()

	_ = localEnv.Stop()
	_ = remoteEnv.Stop()

	os.Exit(code)
}

// buildScheme registers all API types used in integration tests.
func buildScheme() *runtime.Scheme {
	s := runtime.NewScheme()

	if err := corev1.AddToScheme(s); err != nil {
		panic("adding core/v1 to scheme: " + err.Error())
	}

	if err := v1alpha1.AddToScheme(s); err != nil {
		panic("adding kilo/v1alpha1 ClusterMesh to scheme: " + err.Error())
	}

	if err := kilov1alpha1.AddToScheme(s); err != nil {
		panic("adding kilo/v1alpha1 Peer to scheme: " + err.Error())
	}

	return s
}

// ensureNamespace creates a namespace if it does not exist.
func ensureNamespace(ctx context.Context, cl client.Client, name string) {
	ns := &corev1.Namespace{}
	ns.Name = name
	_ = cl.Create(ctx, ns)
}
