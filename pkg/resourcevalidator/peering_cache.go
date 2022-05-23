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

	"k8s.io/apimachinery/pkg/labels"
	"sigs.k8s.io/controller-runtime/pkg/client"

	sharing "github.com/liqotech/liqo/apis/sharing/v1alpha1"
	vkv1alpha1 "github.com/liqotech/liqo/apis/virtualkubelet/v1alpha1"
)

type peeringCache struct {
	peeringInfo map[string]*peeringInfo
	init        bool
}

/**
 * PeeringCache methods
 */

func (pc *peeringCache) getAllPeeringInfo() map[string]*peeringInfo {
	return pc.peeringInfo
}

func (pc *peeringCache) getPeeringFromCache(clusterID string) (*peeringInfo, bool) {
	pi, found := pc.peeringInfo[clusterID]
	return pi, found
}

func (pc *peeringCache) addPeeringToCache(clusterID string, pi *peeringInfo) {
	pc.peeringInfo[clusterID] = pi
}

func (pc *peeringCache) deletePeeringFromCache(clusterID string) {
	delete(pc.peeringInfo, clusterID)
}

func (pc *peeringCache) updatePeeringInCache(clusterID string, pi *peeringInfo) {
	pc.peeringInfo[clusterID] = pi
}

// TODO: refresh has to be an update and not a new cache generation.
func (spv *shadowPodValidator) refreshCache(ctx context.Context, c client.Client) error {
	shadowPodList := vkv1alpha1.ShadowPodList{}
	resourceOfferList := sharing.ResourceOfferList{}
	if err := c.List(ctx, &resourceOfferList, &client.ListOptions{}); err != nil {
		return err
	}
	if !spv.PeeringCache.init {
		spv.PeeringCache.init = true
		for i := range resourceOfferList.Items {
			ro := &resourceOfferList.Items[i]
			ClusterID := ro.Labels["discovery.liqo.io/cluster-id"]
			if ClusterID == "" {
				return fmt.Errorf("resource offer %s has no cluster id", ro.Name)
			}
			spv.PeeringCache.addPeeringToCache(ClusterID, createPeeringInfo(ClusterID, getQuotaFromResourceOffer(ro)))
		}
	} else {
		// Ciclo su tutti i Peering registrati in cache
		for clusterID, pi := range spv.PeeringCache.getAllPeeringInfo() {
			pi.Lock()
			spMap := make(map[string]string)

			// Get the List of shadow pods running on the cluster
			if err := c.List(ctx, &shadowPodList, &client.ListOptions{
				LabelSelector: labels.SelectorFromSet(map[string]string{"virtualkubelet.liqo.io/origin": clusterID}),
			}); err != nil {
				pi.Unlock()
				return err
			}

			// Ciclo su tutti i shadow pods del cluster
			for i := range shadowPodList.Items {
				shadowPod := &shadowPodList.Items[i]
				_, found := pi.getShadowPodDescription(string(shadowPod.GetUID()))
				if !found {
					cachelog.Info(fmt.Sprintf("Shadow pod %s not found in cache", shadowPod.GetUID()))
					// TODO: this should never happen, but maybe we need to manage also this case better
					continue
				}
				spMap[string(shadowPod.GetUID())] = string(shadowPod.GetUID())
			}

			// Ciclo su tutti i shadow pods registrati in cache
			for _, shadowPodDescription := range pi.getAllShadowPodDescription() {
				if _, ok := spMap[shadowPodDescription.getUID()]; !shadowPodDescription.isRunning() && !ok {
					pi.removeShadowPod(shadowPodDescription)
				}
			}
			pi.Unlock()
		}

		// Check if there are new resource offers in the system snapshot
		if len(spv.PeeringCache.peeringInfo) < len(resourceOfferList.Items) {
			for i := range resourceOfferList.Items {
				ro := &resourceOfferList.Items[i]
				ClusterID := ro.Labels["discovery.liqo.io/cluster-id"]
				if ClusterID == "" {
					return fmt.Errorf("resource offer %s has no cluster id", ro.Name)
				}
				if _, found := spv.PeeringCache.getPeeringFromCache(ClusterID); !found {
					newPI := createPeeringInfo(ClusterID, getQuotaFromResourceOffer(ro))
					// Get the List of shadow pods running on the cluster
					if err := c.List(ctx, &shadowPodList, &client.ListOptions{
						LabelSelector: labels.SelectorFromSet(map[string]string{"virtualkubelet.liqo.io/origin": ClusterID}),
					}); err != nil {
						return err
					}
					for i := range shadowPodList.Items {
						shadowPod := &shadowPodList.Items[i]
						newPI.addShadowPod(createShadowPodDescription(string(shadowPod.GetUID()), getQuotaFromShadowPod(shadowPod)))
					}
					spv.PeeringCache.addPeeringToCache(ClusterID, newPI)
				}
			}
		}
	}

	return nil
}
