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
	"context"
	"time"

	"github.com/cockroachdb/errors"
	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	apiextensionsclient "k8s.io/apiextensions-apiserver/pkg/client/clientset/clientset"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/rest"
	"sigs.k8s.io/yaml"
)

const (
	crdName      = "clustermeshes.kilo.squat.ai"
	waitTimeout  = 30 * time.Second
	pollInterval = 500 * time.Millisecond
)

// InstallOrUpdate ensures the ClusterMesh CRD is present and up-to-date in the cluster.
// It creates the CRD if it does not exist, or updates it if the spec has changed.
// After apply it waits up to 30 seconds for the CRD to reach Established=True.
func InstallOrUpdate(ctx context.Context, cfg *rest.Config) error {
	desired, err := parseCRD()
	if err != nil {
		return err
	}

	client, err := apiextensionsclient.NewForConfig(cfg)
	if err != nil {
		return errors.Wrap(err, "create apiextensions client")
	}

	crdClient := client.ApiextensionsV1().CustomResourceDefinitions()

	existing, err := crdClient.Get(ctx, crdName, metav1.GetOptions{})

	switch {
	case apierrors.IsNotFound(err):
		_, err = crdClient.Create(ctx, desired, metav1.CreateOptions{})
		if err != nil {
			return errors.Wrap(err, "create CRD")
		}
	case err != nil:
		return errors.Wrap(err, "get CRD")
	default:
		desired.ResourceVersion = existing.ResourceVersion

		_, err = crdClient.Update(ctx, desired, metav1.UpdateOptions{})
		if err != nil {
			return errors.Wrap(err, "update CRD")
		}
	}

	return waitForCRD(ctx, client)
}

// parseCRD deserialises the embedded YAML into an apiextensionsv1.CustomResourceDefinition.
func parseCRD() (*apiextensionsv1.CustomResourceDefinition, error) {
	crd := &apiextensionsv1.CustomResourceDefinition{}

	err := yaml.Unmarshal(clusterMeshCRDYAML, crd)
	if err != nil {
		return nil, errors.Wrap(err, "unmarshal CRD YAML")
	}

	return crd, nil
}

// waitForCRD polls until the CRD has the Established condition set to True,
// or the context deadline (max waitTimeout) is reached.
func waitForCRD(ctx context.Context, client apiextensionsclient.Interface) error {
	ctx, cancel := context.WithTimeout(ctx, waitTimeout)
	defer cancel()

	ticker := time.NewTicker(pollInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return errors.New("timed out waiting for CRD to become established")
		case <-ticker.C:
			crd, err := client.ApiextensionsV1().CustomResourceDefinitions().Get(ctx, crdName, metav1.GetOptions{})
			if err != nil {
				return errors.Wrap(err, "poll CRD status")
			}

			for _, cond := range crd.Status.Conditions {
				if cond.Type == apiextensionsv1.Established && cond.Status == apiextensionsv1.ConditionTrue {
					return nil
				}
			}
		}
	}
}
