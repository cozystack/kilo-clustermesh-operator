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

package multicluster

import (
	"context"
	"net/http"

	"github.com/cockroachdb/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/rest"
	k8sevents "k8s.io/client-go/tools/events"
	"k8s.io/client-go/tools/record"
	"sigs.k8s.io/controller-runtime/pkg/cache"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/cluster"

	v1alpha1 "github.com/squat/kilo-clustermesh-operator/api/v1alpha1"
)

// ClusterRegistry holds controller-runtime Cluster objects for every cluster in a ClusterMesh.
type ClusterRegistry struct {
	clusters map[string]cluster.Cluster
	local    string
}

// NewFromClients constructs a ClusterRegistry directly from pre-built client.Client
// instances. This is intended for integration tests where envtest provides the clients
// directly, bypassing the normal kubeconfig-based cluster construction.
// The local parameter must match one of the keys in clients.
func NewFromClients(clients map[string]client.Client, local string) *ClusterRegistry {
	wrapped := make(map[string]cluster.Cluster, len(clients))
	for name, c := range clients {
		wrapped[name] = &directCluster{client: c}
	}

	return &ClusterRegistry{clusters: wrapped, local: local}
}

// directCluster is a minimal cluster.Cluster implementation that wraps a
// pre-built client.Client. Only GetClient() is implemented; all other methods
// panic and must not be called in integration tests.
type directCluster struct {
	client client.Client
}

func (d *directCluster) GetClient() client.Client { return d.client }
func (d *directCluster) GetConfig() *rest.Config  { panic("directCluster: GetConfig not implemented") }

func (d *directCluster) GetScheme() *runtime.Scheme {
	panic("directCluster: GetScheme not implemented")
}

func (d *directCluster) GetFieldIndexer() client.FieldIndexer {
	panic("directCluster: GetFieldIndexer not implemented")
}

func (d *directCluster) GetCache() cache.Cache { panic("directCluster: GetCache not implemented") }

func (d *directCluster) GetRESTMapper() meta.RESTMapper {
	panic("directCluster: GetRESTMapper not implemented")
}

func (d *directCluster) GetAPIReader() client.Reader {
	panic("directCluster: GetAPIReader not implemented")
}

func (d *directCluster) GetHTTPClient() *http.Client {
	panic("directCluster: GetHTTPClient not implemented")
}

func (d *directCluster) Start(_ context.Context) error { return nil }

func (d *directCluster) GetEventRecorderFor(_ string) record.EventRecorder {
	panic("directCluster: GetEventRecorderFor not implemented")
}

func (d *directCluster) GetEventRecorder(_ string) k8sevents.EventRecorder {
	panic("directCluster: GetEventRecorder not implemented")
}

// EntrySource pairs a cluster entry with the namespace of the ClusterMesh
// resource that contains it. The operator watches ClusterMesh objects
// cluster-wide, and the CRD's kubeconfigSecretRef has no namespace field,
// so the originating namespace is the only place the Secret can be read
// from.
type EntrySource struct {
	Entry          v1alpha1.ClusterEntry
	MeshNamespace string
}

// Build creates a ClusterRegistry from a list of cluster entries.
// localCfg is the rest.Config of the cluster where the controller runs.
// kubeClient is used to read kubeconfig Secrets for remote clusters from the
// namespace of the ClusterMesh resource that contributed each entry.
func Build(
	ctx context.Context,
	entries []EntrySource,
	localCfg *rest.Config,
	kubeClient client.Client,
	scheme *runtime.Scheme,
) (*ClusterRegistry, error) {
	reg := &ClusterRegistry{
		clusters: make(map[string]cluster.Cluster, len(entries)),
	}

	for _, src := range entries {
		entry := src.Entry

		cfg, err := configForEntry(ctx, &entry, localCfg, src.MeshNamespace, kubeClient)
		if err != nil {
			return nil, err
		}

		if entry.Local {
			reg.local = entry.Name
		}

		c, err := cluster.New(cfg, func(o *cluster.Options) {
			o.Scheme = scheme
		})
		if err != nil {
			return nil, errors.Wrapf(err, "creating cluster object for %q", entry.Name)
		}

		reg.clusters[entry.Name] = c
	}

	return reg, nil
}

// configForEntry returns the rest.Config for a cluster entry.
// For local entries it copies localCfg; for remote entries it reads the
// kubeconfig Secret from the originating ClusterMesh's namespace.
func configForEntry(
	ctx context.Context,
	entry *v1alpha1.ClusterEntry,
	localCfg *rest.Config,
	meshNamespace string,
	kubeClient client.Client,
) (*rest.Config, error) {
	if entry.Local {
		return rest.CopyConfig(localCfg), nil
	}

	if entry.KubeconfigSecretRef == nil {
		return nil, errors.Newf("cluster %q is not local but has no kubeconfigSecretRef", entry.Name)
	}

	cfg, err := RestConfigFromSecret(ctx, kubeClient, meshNamespace, *entry.KubeconfigSecretRef)
	if err != nil {
		return nil, errors.Wrapf(err, "building config for cluster %q", entry.Name)
	}

	return cfg, nil
}

// Get returns the Cluster for the given name.
func (r *ClusterRegistry) Get(name string) (cluster.Cluster, bool) {
	c, ok := r.clusters[name]

	return c, ok
}

// Client returns the controller-runtime client for the given cluster.
func (r *ClusterRegistry) Client(name string) (client.Client, bool) {
	c, ok := r.clusters[name]
	if !ok {
		return nil, false
	}

	return c.GetClient(), true
}

// LocalName returns the name of the local cluster.
func (r *ClusterRegistry) LocalName() string {
	return r.local
}

// Clusters returns all cluster names.
func (r *ClusterRegistry) Clusters() []string {
	names := make([]string, 0, len(r.clusters))
	for name := range r.clusters {
		names = append(names, name)
	}

	return names
}

// All returns a map of all cluster.Cluster objects.
// Used by main.go to register them with mgr.Add().
func (r *ClusterRegistry) All() map[string]cluster.Cluster {
	return r.clusters
}
