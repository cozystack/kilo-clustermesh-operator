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

package crd

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
)

func TestParseCRD(t *testing.T) {
	crd, err := parseCRD()
	require.NoError(t, err)
	assert.Equal(t, "clustermeshes.kilo.squat.ai", crd.Name)
	assert.Equal(t, "kilo.squat.ai", crd.Spec.Group)
	assert.Len(t, crd.Spec.Versions, 1)
	assert.Equal(t, "v1alpha1", crd.Spec.Versions[0].Name)
	assert.Equal(t, "ClusterMesh", crd.Spec.Names.Kind)
	assert.Equal(t, "clustermeshes", crd.Spec.Names.Plural)
	assert.Equal(t, apiextensionsv1.NamespaceScoped, crd.Spec.Scope)
}

func TestEmbeddedCRDIsNotEmpty(t *testing.T) {
	assert.NotEmpty(t, clusterMeshCRDYAML)
	assert.Contains(t, string(clusterMeshCRDYAML), "kilo.squat.ai")
}
