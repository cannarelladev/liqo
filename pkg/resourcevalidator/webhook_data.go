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
	"sync"
	"time"

	admissionv1 "k8s.io/api/admission/v1"
	v1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/labels"
	"sigs.k8s.io/controller-runtime/pkg/client"
	logf "sigs.k8s.io/controller-runtime/pkg/log"

	vkv1alpha1 "github.com/liqotech/liqo/apis/virtualkubelet/v1alpha1"
)

// log is for logging in this package.
var cachelog = logf.Log.WithName("[ webhook-cache ]")

type peeringInfo struct {
	ClusterID        string
	PeeringQuota     v1.ResourceList
	UsedPeeringQuota v1.ResourceList
	FreePeeringQuota v1.ResourceList
	LastUpdateTime   string
}

type peeringCache struct {
	cacheMutex  sync.RWMutex
	peeringInfo map[string]peeringInfo
}

func (pi *peeringInfo) addResources(resources v1.ResourceList) {
	for key, val := range resources {
		if prevFree, ok := pi.FreePeeringQuota[key]; ok {
			prevFree.Add(val)
			pi.FreePeeringQuota[key] = prevFree
		} else {
			pi.FreePeeringQuota[key] = val.DeepCopy()
		}
		if prevUsed, ok := pi.UsedPeeringQuota[key]; ok {
			prevUsed.Sub(val)
			pi.UsedPeeringQuota[key] = prevUsed
		} else {
			pi.UsedPeeringQuota[key] = val.DeepCopy()
		}
	}
	pi.LastUpdateTime = time.Now().Format(time.RFC3339)
}

func (pi *peeringInfo) subtractResources(resources v1.ResourceList) {
	for key, val := range resources {
		if prevFree, ok := pi.FreePeeringQuota[key]; ok {
			prevFree.Sub(val)
			pi.FreePeeringQuota[key] = prevFree
		} else {
			pi.FreePeeringQuota[key] = val.DeepCopy()
		}
		if prevUsed, ok := pi.UsedPeeringQuota[key]; ok {
			prevUsed.Add(val)
			pi.UsedPeeringQuota[key] = prevUsed
		} else {
			pi.UsedPeeringQuota[key] = val.DeepCopy()
		}
	}
	pi.LastUpdateTime = time.Now().Format(time.RFC3339)
}

func createPeeringInfo(clusterID string, resources v1.ResourceList) peeringInfo {
	return peeringInfo{
		ClusterID:        clusterID,
		PeeringQuota:     resources.DeepCopy(),
		UsedPeeringQuota: generateQuotaPattern(resources),
		FreePeeringQuota: resources.DeepCopy(),
		LastUpdateTime:   time.Now().Format(time.RFC3339),
	}
}

func (pc *peeringCache) getPeeringFromCache(clusterID string) (peeringInfo, bool) {
	pi, found := pc.peeringInfo[clusterID]
	return pi, found
}

func (pc *peeringCache) addPeeringToCache(clusterID string, pi peeringInfo) {
	pc.peeringInfo[clusterID] = pi
}

func (pc *peeringCache) deletePeeringFromCache(clusterID string) {
	delete(pc.peeringInfo, clusterID)
}

func (pc *peeringCache) updatePeeringInCache(clusterID string, pi peeringInfo) {
	pc.peeringInfo[clusterID] = pi
}

func refreshPeeringQuota(ctx context.Context, c client.Client, clusterID string) (v1.ResourceList, error) {
	var resources v1.ResourceList
	shadowPodList := vkv1alpha1.ShadowPodList{}
	if err := c.List(ctx, &shadowPodList, &client.ListOptions{
		LabelSelector: labels.SelectorFromSet(map[string]string{"virtualkubelet.liqo.io/origin": clusterID}),
	}); err != nil {
		return nil, err
	}
	for _, shadowPod := range shadowPodList.Items {
		resources.Cpu().Add(*shadowPod.Spec.Pod.Containers[0].Resources.Limits.Cpu())
		resources.Memory().Add(*shadowPod.Spec.Pod.Containers[0].Resources.Limits.Memory())
		resources.Storage().Add(*shadowPod.Spec.Pod.Containers[0].Resources.Limits.Storage())
	}
	return resources, nil
}

func (pi *peeringInfo) testAndUpdatePeeringInfo(shadowPodQuota v1.ResourceList, operation admissionv1.Operation) error {
	cachelog.Info(fmt.Sprintf("\tOperation: %s", operation))
	cachelog.Info(quotaFormatter(shadowPodQuota, "\tShadowPodQuota"))
	cachelog.Info(quotaFormatter(pi.PeeringQuota, "\tPeeringInfo Quota"))
	cachelog.Info(quotaFormatter(pi.UsedPeeringQuota, "\tPeeringInfo UsedQuota"))
	cachelog.Info(quotaFormatter(pi.FreePeeringQuota, "\tPeeringInfo FreeQuota"))

	switch operation {
	case admissionv1.Create:
		if pi.FreePeeringQuota.Cpu().MilliValue() < shadowPodQuota.Cpu().MilliValue() {
			cachelog.Info(
				fmt.Sprintf("\tPEERING INFO: Peering CPU quota usage exceeded - FREE %s / REQUESTED %s",
					pi.FreePeeringQuota.Cpu(),
					shadowPodQuota.Cpu()),
			)
			return fmt.Errorf("PEERING INFO: Peering CPU quota usage exceeded")
		}
		if pi.FreePeeringQuota.Memory().Value() < shadowPodQuota.Memory().Value() {
			cachelog.Info(
				fmt.Sprintf("\tPEERING INFO: Peering Memory quota usage exceeded - FREE %s / REQUESTED %s",
					pi.FreePeeringQuota.Memory(),
					shadowPodQuota.Memory()),
			)
			return fmt.Errorf("PEERING INFO: Peering Memory quota usage exceeded")
		}
		if pi.FreePeeringQuota.Storage().Value() < shadowPodQuota.Storage().Value() {
			cachelog.Info(
				fmt.Sprintf("\tPEERING INFO: Peering Storage quota usage exceeded - FREE %s / REQUESTED %s",
					pi.FreePeeringQuota.Storage(),
					shadowPodQuota.Storage()),
			)
			return fmt.Errorf("PEERING INFO: Peering Disk quota usage exceeded")
		}
		pi.subtractResources(shadowPodQuota)
		cachelog.Info(quotaFormatter(pi.PeeringQuota, "\tUpdated PeeringInfo Quota"))
		cachelog.Info(quotaFormatter(pi.UsedPeeringQuota, "\tUpdated PeeringInfo UsedQuota"))
		cachelog.Info(quotaFormatter(pi.FreePeeringQuota, "\tUpdated PeeringInfo FreeQuota"))
		return nil
	case admissionv1.Delete:
		cachelog.Info(fmt.Sprintf("\tOperation: %s", operation))
		cachelog.Info(quotaFormatter(shadowPodQuota, "\tShadowPodQuota"))
		cachelog.Info(quotaFormatter(pi.PeeringQuota, "\tPeeringInfo Quota"))
		cachelog.Info(quotaFormatter(pi.UsedPeeringQuota, "\tPeeringInfo UsedQuota"))
		cachelog.Info(quotaFormatter(pi.FreePeeringQuota, "\tPeeringInfo FreeQuota"))
		pi.addResources(shadowPodQuota)
		cachelog.Info(quotaFormatter(pi.PeeringQuota, "\tUpdated PeeringInfo Quota"))
		cachelog.Info(quotaFormatter(pi.UsedPeeringQuota, "\tUpdated PeeringInfo UsedQuota"))
		cachelog.Info(quotaFormatter(pi.FreePeeringQuota, "\tUpdated PeeringInfo FreeQuota"))
		return nil
	default:
		return fmt.Errorf("PEERING INFO: operation not supported")
	}
}

func (pi *peeringInfo) getQuota() v1.ResourceList {
	return pi.PeeringQuota
}

func (pi *peeringInfo) getUsedQuota() v1.ResourceList {
	return pi.UsedPeeringQuota
}

func (pi *peeringInfo) getFreeQuota() v1.ResourceList {
	return pi.FreePeeringQuota
}
