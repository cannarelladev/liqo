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

package resourcevalidator

import (
	"context"
	"fmt"
	"regexp"

	v1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	logf "sigs.k8s.io/controller-runtime/pkg/log"

	sharing "github.com/liqotech/liqo/apis/sharing/v1alpha1"
	vkv1alpha1 "github.com/liqotech/liqo/apis/virtualkubelet/v1alpha1"
)

var cachelog = logf.Log.WithName("[ webhook-cache ]")
var shadowpodlog = logf.Log.WithName("[ shadowpod-resource ]")
var webhooklog = logf.Log.WithName("[ webhook ]")
var resourceofferlog = logf.Log.WithName("[ resourceoffer-resource ]")
var webhookrefreshlog = logf.Log.WithName("[ webhook-refresh ]")

// DecodeShadowPod decodes a shadow pod from a given runtime object.
func (spv *ShadowPodValidator) DecodeShadowPod(obj runtime.RawExtension) (shadowpod *vkv1alpha1.ShadowPod, err error) {
	shadowpod = &vkv1alpha1.ShadowPod{}
	err = spv.decoder.DecodeRaw(obj, shadowpod)
	return
}

func quotaFormatter(quota v1.ResourceList, quotaName string) string {
	result := fmt.Sprintf("%s [ ", quotaName)
	r := regexp.MustCompile("^hugepages-")
	for k, v := range quota {
		if res := r.MatchString(k.String()); !res {
			result += fmt.Sprintf("%s: %s, ", k, v.String())
		}
	}
	result += "]"
	return result
}

/* func generateQuotaPattern(quota v1.ResourceList) v1.ResourceList {
	quantity := resource.NewQuantity(0, resource.DecimalSI)
	result := v1.ResourceList{}
	for k := range quota {
		result[k] = quantity.DeepCopy()
	}
	return result
} */

func getQuotaFromResourceOffer(resourceoffer *sharing.ResourceOffer) v1.ResourceList {
	resources := v1.ResourceList{}

	for key, value := range resourceoffer.Spec.ResourceQuota.Hard {
		resources[key] = value
	}
	return resources
}

func getQuotaFromShadowPod(shadowpod *vkv1alpha1.ShadowPod) v1.ResourceList {
	resources := v1.ResourceList{}
	for key, value := range shadowpod.Spec.Pod.Containers[0].Resources.Limits {
		resources[key] = value
	}
	return resources
}

func (spv *ShadowPodValidator) getShadowPodListByClusterID(ctx context.Context, shadowPodList *vkv1alpha1.ShadowPodList, clusterID string) error {
	c := spv.Client
	err := c.List(ctx, shadowPodList, &client.ListOptions{
		LabelSelector: labels.SelectorFromSet(map[string]string{"virtualkubelet.liqo.io/origin": clusterID}),
	})
	return err
}

// This Function compares 2 timestamps to see if the second one is older than the first one of more than a given value of seconds.
/* func isTimestampOlderThan(timestamp1, timestamp2 string, seconds int) bool {
	t1, err := time.Parse(time.RFC3339, timestamp1)
	if err != nil {
		cachelog.Error(err, "Error parsing timestamp", "timestamp", timestamp1)
		return false
	}
	t2, err := time.Parse(time.RFC3339, timestamp2)
	if err != nil {
		cachelog.Error(err, "Error parsing timestamp", "timestamp", timestamp2)
		return false
	}
	diff := t1.Sub(t2)
	if diff.Seconds() > float64(seconds) {
		return true
	}
	return false
} */

/* func filterResourceOffer(list []sharing.ResourceOffer, clusterID string) *sharing.ResourceOffer {
	for _, resourceoffer := range list {
		if resourceoffer.Labels["discovery.liqo.io/cluster-id"] == clusterID {
			return &resourceoffer
		}
	}
	return nil
} */
