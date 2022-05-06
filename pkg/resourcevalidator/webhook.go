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

	admissionv1 "k8s.io/api/admission/v1"

	"k8s.io/apimachinery/pkg/labels"
	"sigs.k8s.io/controller-runtime/pkg/client"

	vkv1alpha1 "github.com/liqotech/liqo/apis/virtualkubelet/v1alpha1"
	logf "sigs.k8s.io/controller-runtime/pkg/log"

	sharing "github.com/liqotech/liqo/apis/sharing/v1alpha1"
	"sigs.k8s.io/controller-runtime/pkg/webhook/admission"
)

// log is for logging in this package.
var shadowpodlog = logf.Log.WithName("shadowpod-resource")

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
	var resourceoffer *sharing.ResourceOffer
	var decodeErr error

	shadowpodlog.Info(string(req.Operation))
	if req.Operation == admissionv1.Delete {
		return admission.Allowed("allowed")
	} else if req.Operation == admissionv1.Create {
		shadowpod, decodeErr = spv.DecodeShadowPod(req.Object)
		if decodeErr != nil {
			return admission.Errored(http.StatusBadRequest, decodeErr)
		}

		originLabel, found := shadowpod.Labels["virtualkubelet.liqo.io/origin"]

		if !found {
			return admission.Denied("missing origin Cluster ID label")
		}

		resourceofferList := &sharing.ResourceOfferList{}
		err := spv.Client.
			List(ctx, resourceofferList, &client.ListOptions{
				LabelSelector: labels.SelectorFromSet(map[string]string{"discovery.liqo.io/cluster-id": originLabel}),
			})
		if err != nil {
			return admission.Denied(fmt.Sprintf("error listing resource offers: %s %s", originLabel, err))
		}

		switch len(resourceofferList.Items) {
		case 0:
			return admission.Denied(fmt.Sprintf("no resource offer found with matching origin label %s", originLabel))
		case 1:
			resourceoffer = &resourceofferList.Items[0]
		default:
			return admission.Denied(fmt.Sprintf("multiple resource offers found with matching origin label %s", originLabel))
		}
	}

	return checkValidShadowPod(shadowpod, resourceoffer)
}

// InjectDecoder injects the decoder.
func (spv *shadowPodValidator) InjectDecoder(d *admission.Decoder) error {
	spv.decoder = d
	return nil
}

// checkValidShadowPod checks if the shadow pod is valid.
func checkValidShadowPod(sp *vkv1alpha1.ShadowPod, ro *sharing.ResourceOffer) admission.Response {
	key := "shadowpod-webhook"

	annotation, found := sp.Annotations[key]

	spLimits := sp.Spec.Pod.Containers[0].Resources.Limits
	roLimits := ro.Spec.ResourceQuota.Hard

	if !found {
		return admission.Denied(fmt.Sprintf("missing annotation %s", key))
	}
	if spLimits.Cpu().IsZero() {
		return admission.Denied("missing cpu limit")
	}
	if spLimits.Memory().IsZero() {
		return admission.Denied("missing memory limit")
	}
	if annotation != "allowed" {
		return admission.Denied(fmt.Sprintf("shadowpod webhook denied: %s is not allowed", key))
	}
	if spLimits.Cpu().MilliValue() > roLimits.Cpu().MilliValue() {
		return admission.Denied(fmt.Sprintf("shadowpod webhook denied: cpu limit %s is too high", spLimits.Cpu().String()))
	}
	// 256000 * 1000 = 256MB
	if spLimits.Memory().Value() > roLimits.Memory().Value() {
		return admission.Denied(fmt.Sprintf("shadowpod webhook denied: memory limit %s is too high", spLimits.Memory().String()))
	}

	return admission.Allowed("allowed")
}
