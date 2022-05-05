// Copyright 2019-2022 The Liqo Authors
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//      http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package resourceValidator

import (
	"context"
	"fmt"
	"net/http"

	// "github.com/onsi/ginkgo/types".
	admissionv1 "k8s.io/api/admission/v1"
	// "k8s.io/client-go/kubernetes".
	// "k8s.io/klog/v2".
	// "k8s.io/apimachinery/pkg/runtime".
	// apierrors "k8s.io/apimachinery/pkg/api/errors".
	// "k8s.io/apimachinery/pkg/util/validation/field".

	// "k8s.io/apimachinery/pkg/runtime".
	// "k8s.io/klog/v2".
	// ctrl "sigs.k8s.io/controller-runtime".

	"sigs.k8s.io/controller-runtime/pkg/client"
	// "sigs.k8s.io/controller-runtime/pkg/webhook".

	logf "sigs.k8s.io/controller-runtime/pkg/log"
	// "sigs.k8s.io/controller-runtime/pkg/webhook".
	vkv1alpha1 "github.com/liqotech/liqo/apis/virtualkubelet/v1alpha1"
	"sigs.k8s.io/controller-runtime/pkg/webhook/admission"
)

// log is for logging in this package.
var shadowpodlog = logf.Log.WithName("shadowpod-resource")

//+kubebuilder:webhook:path=/validate-shadowpod,mutating=false,failurePolicy=fail,sideEffects=None,groups=webhook.liqo.io,resources=shadowpods,verbs=create;update;delete,versions=v1,name=vshadowpod.kb.io,admissionReviewVersions=v1

type shadowPodValidator struct {
	Client  client.Client
	decoder *admission.Decoder
}

// NewShadowPodValidator creates a new shadow pod validator.
func NewShadowPodValidator(c client.Client) admission.Handler {
	return &shadowPodValidator{
		Client: c,
	}
}

func (spv *shadowPodValidator) Handle(ctx context.Context, req admission.Request) admission.Response {

	shadowpod := &vkv1alpha1.ShadowPod{}
	var decodeErr error

	shadowpodlog.Info(string(req.Operation))
	if req.Operation == admissionv1.Delete {
		return admission.Allowed("allowed")
	} else if req.Operation == admissionv1.Create {
		shadowpod, decodeErr = spv.DecodeShadowPod(req.Object)
		if decodeErr != nil {
			return admission.Errored(http.StatusBadRequest, decodeErr)
		}
	}

	return checkValidShadowPod(shadowpod)
}

// InjectDecoder injects the decoder.
func (spv *shadowPodValidator) InjectDecoder(d *admission.Decoder) error {
	spv.decoder = d
	return nil
}

// checkValidShadowPod checks if the shadow pod is valid.
func checkValidShadowPod(sp *vkv1alpha1.ShadowPod) admission.Response {
	key := "shadowpod-webhook"

	annotation, found := sp.Annotations[key]

	limits := sp.Spec.Pod.Containers[0].Resources.Limits

	if !found {
		return admission.Denied(fmt.Sprintf("missing annotation %s", key))
	}
	if limits.Cpu().IsZero() {
		return admission.Denied("missing cpu limit")
	}
	if limits.Memory().IsZero() {
		return admission.Denied("missing memory limit")
	}
	if annotation != "allowed" {
		return admission.Denied(fmt.Sprintf("shadowpod webhook denied: %s is not allowed", key))
	}
	if limits.Cpu().MilliValue() > 1000 {
		return admission.Denied(fmt.Sprintf("shadowpod webhook denied: cpu limit %s is too high", limits.Cpu().String()))
	}
	// 256000 * 1000 = 256MB
	if limits.Memory().Value() > 256000*1000 {
		return admission.Denied(fmt.Sprintf("shadowpod webhook denied: memory limit %s is too high", limits.Memory().String()))
	}

	return admission.Allowed("allowed")
}
