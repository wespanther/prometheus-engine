// Copyright 2022 Google LLC
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     https://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package e2e

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/GoogleCloudPlatform/prometheus-engine/pkg/operator"
	monitoringv1 "github.com/GoogleCloudPlatform/prometheus-engine/pkg/operator/apis/monitoring/v1"
	"github.com/google/go-cmp/cmp"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/wait"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

func TestAlertmanagerDefault(t *testing.T) {
	t.Parallel()
	tctx := newOperatorContext(t)

	alertmanagerConfig := `
receivers:
  - name: "foobar"
route:
  receiver: "foobar"
`
	testAlertmanager(context.Background(), tctx, nil,
		&corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{
				Name:      operator.AlertmanagerPublicSecretName,
				Namespace: tctx.pubNamespace,
			},
			Data: map[string][]byte{
				operator.AlertmanagerPublicSecretKey: []byte(alertmanagerConfig),
			},
		},
		operator.AlertmanagerPublicSecretKey,
	)
}

func TestAlertmanagerCustom(t *testing.T) {
	t.Parallel()
	tctx := newOperatorContext(t)

	alertmanagerConfig := `
receivers:
  - name: "foobar"
route:
  receiver: "foobar"
`
	testAlertmanager(context.Background(), tctx,
		&monitoringv1.ManagedAlertmanagerSpec{
			ConfigSecret: &corev1.SecretKeySelector{
				LocalObjectReference: corev1.LocalObjectReference{
					Name: "my-secret-name",
				},
				Key: "my-secret-key",
			},
		},
		&corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "my-secret-name",
				Namespace: tctx.pubNamespace,
			},
			Data: map[string][]byte{
				"my-secret-key": []byte(alertmanagerConfig),
			},
		},
		"my-secret-key",
	)
}

func testAlertmanager(ctx context.Context, t *OperatorContext, spec *monitoringv1.ManagedAlertmanagerSpec, secret *corev1.Secret, key string) {
	t.createOperatorConfigFrom(ctx, monitoringv1.OperatorConfig{
		Collection: monitoringv1.CollectionSpec{
			ExternalLabels: map[string]string{
				"external_key": "external_val",
			},
			Filter: monitoringv1.ExportFilters{
				MatchOneOf: []string{
					"{job='foo'}",
					"{__name__=~'up'}",
				},
			},
			KubeletScraping: &monitoringv1.KubeletScraping{
				Interval: "5s",
			},
		},
		ManagedAlertmanager: spec,
	})
	t.Run("deployed", t.subtest(func(ctx context.Context, t *OperatorContext) {
		t.Parallel()
		testAlertmanagerDeployed(ctx, t)
	}))
	t.Run("config set", t.subtest(func(ctx context.Context, t *OperatorContext) {
		t.Parallel()
		testAlertmanagerConfig(ctx, t, secret, key)
	}))
}

func testAlertmanagerDeployed(ctx context.Context, t *OperatorContext) {
	err := wait.Poll(time.Second, 1*time.Minute, func() (bool, error) {
		var ss appsv1.StatefulSet
		if err := t.Client().Get(ctx, client.ObjectKey{Namespace: t.namespace, Name: operator.NameAlertmanager}, &ss); err != nil {
			if apierrors.IsNotFound(err) {
				return false, nil
			}
			t.Log(fmt.Errorf("getting alertmanager StatefulSet failed: %w", err))
			return false, fmt.Errorf("getting alertmanager StatefulSet failed: %w", err)
		}

		// Assert we have the expected annotations.
		wantedAnnotations := map[string]string{
			"components.gke.io/component-name":               "managed_prometheus",
			"cluster-autoscaler.kubernetes.io/safe-to-evict": "true",
		}
		if diff := cmp.Diff(wantedAnnotations, ss.Spec.Template.Annotations); diff != "" {
			return false, fmt.Errorf("unexpected annotations (-want, +got): %s", diff)
		}

		return true, nil
	})
	if err != nil {
		t.Errorf("unable to get alertmanager statefulset: %s", err)
	}
}

func testAlertmanagerConfig(ctx context.Context, t *OperatorContext, pub *corev1.Secret, key string) {
	if err := t.Client().Create(ctx, pub); err != nil {
		t.Fatalf("unable to create alertmanager config secret: %s", err)
	}

	if err := wait.Poll(3*time.Second, 3*time.Minute, func() (bool, error) {
		var secret corev1.Secret
		if err := t.Client().Get(ctx, client.ObjectKey{Namespace: t.namespace, Name: operator.AlertmanagerSecretName}, &secret); err != nil {
			if apierrors.IsNotFound(err) {
				return false, nil
			}
			t.Log(fmt.Errorf("getting alertmanager secret failed: %s", err))
			return false, fmt.Errorf("getting alertmanager secret failed: %w", err)
		}

		bytes, ok := secret.Data["config.yaml"]
		if !ok {
			t.Log(errors.New("getting alertmanager secret data in config.yaml failed"))
			return false, errors.New("getting alertmanager secret data in config.yaml failed")
		}

		// Grab data from public secret and compare.
		data := pub.Data[key]
		if diff := cmp.Diff(data, bytes); diff != "" {
			return false, fmt.Errorf("unexpected configuration (-want, +got): %s", diff)
		}
		return true, nil
	}); err != nil {
		t.Errorf("unable to get alertmanager config: %s", err)
	}
}
