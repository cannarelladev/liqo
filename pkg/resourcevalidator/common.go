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
	// "context"
	"fmt"

	vkv1alpha1 "github.com/liqotech/liqo/apis/virtualkubelet/v1alpha1"
	v1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	"k8s.io/apimachinery/pkg/runtime"
	// "sigs.k8s.io/controller-runtime/pkg/client"
	// sharing "github.com/liqotech/liqo/apis/sharing/v1alpha1"
)

func (spv *shadowPodValidator) DecodeShadowPod(obj runtime.RawExtension) (shadowpod *vkv1alpha1.ShadowPod, err error) {
	shadowpod = &vkv1alpha1.ShadowPod{}
	err = spv.decoder.DecodeRaw(obj, shadowpod)
	return
}

func quotaFormatter(quota v1.ResourceList, quotaName string) string {
	result := fmt.Sprintf("%s [ ", quotaName)
	for k, v := range quota {
		result += fmt.Sprintf("%s: %s, ", k, v.String())
	}
	result += "]"
	return result
}

func generateQuotaPattern(quota v1.ResourceList) v1.ResourceList {
	quantity := resource.Quantity(*resource.NewQuantity(0, resource.DecimalSI))
	result := v1.ResourceList{}
	for k, _ := range quota {
		result[k] = quantity.DeepCopy()
	}
	return result
}

/* func getResourceOfferByLabel(ctx context.Context, client client.Client, label string) (offer *sharing.ResourceOffer, err error) {
	resourceofferList := &sharing.ResourceOfferList{}
	offer = &sharing.ResourceOffer{}
	err = client.List(ctx, resourceofferList, &client.ListOptions{
		LabelSelector: labels.SelectorFromSet(map[string]string{"discovery.liqo.io/cluster-id": label}),
	})

	return
} */
