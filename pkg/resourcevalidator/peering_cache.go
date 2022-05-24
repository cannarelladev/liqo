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
	"time"

	"k8s.io/apimachinery/pkg/util/wait"
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
func (spv *ShadowPodValidator) refreshCache(ctx context.Context) (done bool, err error) {
	webhookrefreshlog.Info("[ REFRESH ] Refreshing peering cache")
	c := spv.Client
	shadowPodList := vkv1alpha1.ShadowPodList{}
	resourceOfferList := sharing.ResourceOfferList{}
	if err := c.List(ctx, &resourceOfferList, &client.ListOptions{}); err != nil {
		return true, err
	}
	if !spv.PeeringCache.init {
		webhookrefreshlog.Info("----------------------------------------------------")
		webhookrefreshlog.Info("[ INITIALIZATION ] Cache initialization started")
		spv.PeeringCache.init = true
		for i := range resourceOfferList.Items {
			ro := &resourceOfferList.Items[i]
			clusterID := ro.Labels["discovery.liqo.io/cluster-id"]
			if clusterID == "" {
				return true, fmt.Errorf("ResourceOffer %s has no cluster id", ro.Name)
			}
			webhookrefreshlog.Info(fmt.Sprintf("[ INITIALIZATION ] Generating PeeringInfo in cache for corresponding ResourceOffer %s", ro.Name))
			pi := createPeeringInfo(clusterID, getQuotaFromResourceOffer(ro))

			pi.Lock()
			// Get the List of shadow pods running on the cluster
			if err := spv.getShadowPodListByClusterID(ctx, &shadowPodList, clusterID); err != nil {
				pi.Unlock()
				return true, err
			}
			if len(shadowPodList.Items) > 0 {
				webhookrefreshlog.Info(fmt.Sprintf("[ INITIALIZATION ] Found %d ShadowPods running on cluster %s", len(shadowPodList.Items), clusterID))
				alignShadowPods(&shadowPodList, pi)
			}

			spv.PeeringCache.addPeeringToCache(clusterID, pi)
			pi.Unlock()
		}
		webhookrefreshlog.Info("[ INITIALIZATION ] Cache initialization complete")
		webhookrefreshlog.Info("----------------------------------------------------")
	} else {
		webhookrefreshlog.Info("----------------------------------------------------")
		webhookrefreshlog.Info("[ REFRESH ] Cache refresh started")
		// Ciclo su tutti i Peering registrati in cache
		for clusterID, pi := range spv.PeeringCache.getAllPeeringInfo() {
			pi.Lock()
			webhookrefreshlog.Info("[ REFRESH ] Refreshing PeeringInfo for clusterID: " + clusterID)
			spMap := make(map[string]string)

			// Get the List of shadow pods running on the cluster
			if err := spv.getShadowPodListByClusterID(ctx, &shadowPodList, clusterID); err != nil {
				pi.Unlock()
				return true, err
			}

			webhookrefreshlog.Info("[ REFRESH ] Found " + fmt.Sprintf("%d", len(shadowPodList.Items)) + " ShadowPods for clusterID: " + clusterID)
			// Check su tutti gli ShadowPods del cluster
			checkShadowPods(&shadowPodList, pi, spMap)

			webhookrefreshlog.Info("[ REFRESH ] Searching for terminated ShadowPodDescription to be removed from cache")
			// Alignment of all ShadowPodDescriptions in cache
			alignShadowPodDescriptions(pi, spMap)

			pi.Unlock()
		}

		webhookrefreshlog.Info("[ REFRESH ] Cache refresh completed")
		webhookrefreshlog.Info("[ REFRESH ] ResourceOffers - PeeringInfo alignment check started")
		if err := checkAlignmentResourceOfferPeeringInfo(ctx, spv); err != nil {
			return true, err
		}
		webhookrefreshlog.Info("[ REFRESH ] ResourceOffers - PeeringInfo alignment check completed")
		webhookrefreshlog.Info("----------------------------------------------------")
	}
	return false, nil
}

// RefreshTimer is a wrapper funcion that receives a ShadowPodValidator and starts an PollImmediateInfinite timer to periodically refresh the cache.
func RefreshTimer(spv *ShadowPodValidator) func(ctx context.Context) error {
	return func(ctx context.Context) error {
		seconds, _ := time.ParseDuration("30s")
		return wait.PollImmediateInfiniteWithContext(ctx, seconds, spv.refreshCache)
	}
}

func alignShadowPods(shadowPodList *vkv1alpha1.ShadowPodList, pi *peeringInfo) {
	for i := range shadowPodList.Items {
		shadowPod := &shadowPodList.Items[i]
		_, found := pi.getShadowPodDescription(string(shadowPod.GetUID()))
		if !found {
			pi.addShadowPod(createShadowPodDescription(string(shadowPod.GetUID()), getQuotaFromShadowPod(shadowPod)))
			webhookrefreshlog.Info("[ INITIALIZATION ] ShadowPod " + string(shadowPod.GetUID()) + " added in cache")
		}
	}
}

func checkShadowPods(shadowPodList *vkv1alpha1.ShadowPodList, pi *peeringInfo, spMap map[string]string) {
	for i := range shadowPodList.Items {
		shadowPod := &shadowPodList.Items[i]
		_, found := pi.getShadowPodDescription(string(shadowPod.GetUID()))
		if !found {
			webhookrefreshlog.Info(fmt.Sprintf("[ REFRESH ] ShadowPod %s not found in cache", shadowPod.GetUID()))
			// TODO: this should never happen, but maybe we need to manage also this case better
			continue
		}
		spMap[string(shadowPod.GetUID())] = string(shadowPod.GetUID())
	}
}

func alignShadowPodDescriptions(pi *peeringInfo, spMap map[string]string) {
	for _, shadowPodDescription := range pi.getAllShadowPodDescription() {
		if _, ok := spMap[shadowPodDescription.getUID()]; !shadowPodDescription.isRunning() && !ok {
			pi.removeShadowPod(shadowPodDescription)
			webhookrefreshlog.Info("[ REFRESH ] ShadowPodDescription " + shadowPodDescription.getUID() + " removed from cache")
		}
	}
}

func checkAlignmentResourceOfferPeeringInfo(ctx context.Context, spv *ShadowPodValidator) error {
	shadowPodList := vkv1alpha1.ShadowPodList{}
	resourceOfferList := sharing.ResourceOfferList{}
	c := spv.Client

	// Get the List of resource offers
	if err := c.List(ctx, &resourceOfferList); err != nil {
		return err
	}

	// Get the List of existing PeeringInfo
	peeringInfoList := spv.PeeringCache.getAllPeeringInfo()

	// Check if there are new ResourceOffers in the system snapshot
	for i := range resourceOfferList.Items {
		ro := &resourceOfferList.Items[i]
		clusterID := ro.Labels["discovery.liqo.io/cluster-id"]
		if clusterID == "" {
			return fmt.Errorf("ResourceOffer %s has no cluster id", ro.Name)
		}

		// Check if the ResourceOffer is not present in the cache
		if _, found := peeringInfoList[clusterID]; !found {
			webhookrefreshlog.Info("[ REFRESH ] ResourceOffer " + ro.Name + " not found in cache, adding it")
			newPI := createPeeringInfo(clusterID, getQuotaFromResourceOffer(ro))
			newPI.Lock()

			// Get the List of ShadowPods running on the cluster
			if err := spv.getShadowPodListByClusterID(ctx, &shadowPodList, clusterID); err != nil {
				return err
			}

			// Create the ShadowPodDescription for each ShadowPod
			for i := range shadowPodList.Items {
				shadowPod := &shadowPodList.Items[i]
				// Add the ShadowPodDescription to the PeeringInfo
				newPI.addShadowPod(createShadowPodDescription(string(shadowPod.GetUID()), getQuotaFromShadowPod(shadowPod)))
			}
			newPI.Unlock()

			// Add the PeeringInfo to the cache
			spv.PeeringCache.addPeeringToCache(clusterID, newPI)
		}
	}

	// Check if PeeringInfos still have corresponding ResourceOffers
	for i := range peeringInfoList {
		clusterID := peeringInfoList[i].getClusterID()
		foundRO := false

		// Check if the corresponding ResourceOffer is still present in the system snapshot
		for j := range resourceOfferList.Items {
			ro := &resourceOfferList.Items[j]
			if clusterID == ro.Labels["discovery.liqo.io/cluster-id"] {
				foundRO = true
				break
			}
		}

		// If the corresponding ResourceOffer is not present anymore, remove the PeeringInfo from the cache
		if !foundRO {
			webhookrefreshlog.Info("[ REFRESH ] ResourceOffer " + clusterID + " not found in system snapshot, removing it from cache")
			spv.PeeringCache.deletePeeringFromCache(clusterID)
		}
	}
	return nil
}
