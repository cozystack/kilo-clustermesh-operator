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

	"github.com/cockroachdb/errors"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
	"sigs.k8s.io/controller-runtime/pkg/client"

	v1alpha1 "github.com/squat/kilo-clustermesh-operator/api/v1alpha1"
)

// RestConfigFromSecret reads a kubeconfig Secret and returns a rest.Config.
func RestConfigFromSecret(
	ctx context.Context,
	kubeClient client.Client,
	namespace string,
	ref v1alpha1.SecretKeyRef,
) (*rest.Config, error) {
	key := types.NamespacedName{Namespace: namespace, Name: ref.Name}

	var secret corev1.Secret

	err := kubeClient.Get(ctx, key, &secret)
	if err != nil {
		return nil, errors.Wrapf(err, "getting kubeconfig secret %s/%s", namespace, ref.Name)
	}

	data, ok := secret.Data[ref.Key]
	if !ok {
		return nil, errors.Newf("secret %s/%s has no key %q", namespace, ref.Name, ref.Key)
	}

	cfg, err := clientcmd.RESTConfigFromKubeConfig(data)
	if err != nil {
		return nil, errors.Wrap(err, "parsing kubeconfig from secret")
	}

	return cfg, nil
}
