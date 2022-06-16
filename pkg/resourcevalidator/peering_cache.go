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
	"sync"
	"time"

	"k8s.io/apimachinery/pkg/util/wait"
	"sigs.k8s.io/controller-runtime/pkg/client"

	sharing "github.com/liqotech/liqo/apis/sharing/v1alpha1"
	vkv1alpha1 "github.com/liqotech/liqo/apis/virtualkubelet/v1alpha1"
	"github.com/liqotech/liqo/pkg/discovery"
)

type peeringCache struct {
	peeringInfo sync.Map
	ready       bool
}

/**
 * PeeringCache methods
 */

// Probe checks if the webhook cache is Ready.
/* func Probe(req *http.Request) error {
	if ready {
		return nil
	}
	return fmt.Errorf("webhook cache not yet configured")
} */

func (pc *peeringCache) getPeeringFromCache(clusterID string) (*peeringInfo, bool) {
	pi, found := pc.peeringInfo.Load(clusterID)
	return pi.(*peeringInfo), found
}

func (pc *peeringCache) addPeeringToCache(clusterID string, pi *peeringInfo) {
	pc.peeringInfo.Store(clusterID, pi)
}

func (pc *peeringCache) deletePeeringFromCache(clusterID string) {
	pc.peeringInfo.Delete(clusterID)
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
	if !spv.PeeringCache.ready {
		webhookrefreshlog.Info("----------------------------------------------------")
		webhookrefreshlog.Info("[ INITIALIZATION ] Cache initialization started")
		for i := range resourceOfferList.Items {
			ro := &resourceOfferList.Items[i]
			clusterID := ro.Labels[discovery.ClusterIDLabel]
			if clusterID == "" {
				return true, fmt.Errorf("ResourceOffer %s has no cluster id", ro.Name)
			}
			webhookrefreshlog.Info(fmt.Sprintf("[ INITIALIZATION ] Generating PeeringInfo in cache for corresponding ResourceOffer %s", clusterID))
			pi := createPeeringInfo(clusterID, ro.OwnerReferences[0].Name, getQuotaFromResourceOffer(ro))

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
		spv.PeeringCache.ready = true
	} else {
		webhookrefreshlog.Info("----------------------------------------------------")
		webhookrefreshlog.Info("[ REFRESH ] Cache refresh started")
		// Ciclo su tutti i Peering registrati in cache
		spMap := make(map[string]string)

		spv.PeeringCache.peeringInfo.Range(
			func(key, value interface{}) bool {
				pi := value.(*peeringInfo)
				clusterID := key.(string)
				pi.Lock()
				webhookrefreshlog.Info("[ REFRESH ] Refreshing PeeringInfo for clusterID: " + clusterID + " [ " + pi.getClusterName() + " ]")

				// Get the List of shadow pods running on the cluster
				if err := spv.getShadowPodListByClusterID(ctx, &shadowPodList, clusterID); err != nil {
					pi.Unlock()
					return false
				}

				webhookrefreshlog.Info("[ REFRESH ] Found " + fmt.Sprintf("%d", len(shadowPodList.Items)) + " ShadowPods for clusterID: " + clusterID + " [ " + pi.getClusterName() + " ]")
				// Check on all cluster ShadowPods
				checkShadowPods(&shadowPodList, pi, spMap)

				webhookrefreshlog.Info("[ REFRESH ] Searching for terminated ShadowPodDescription to be removed from cache")
				// Alignment of all ShadowPodDescriptions in cache
				alignShadowPodDescriptions(pi, spMap)

				pi.Unlock()
				return true
			},
		)

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
		_, found := pi.getShadowPodDescription(shadowPod.GetName())
		if !found {
			pi.addShadowPod(createShadowPodDescription(shadowPod.GetName(), string(shadowPod.GetUID()), getQuotaFromShadowPod(shadowPod)))
			webhookrefreshlog.Info("[ INITIALIZATION ] ShadowPod " + shadowPod.GetName() + " added in cache")
		}
	}
}

func checkShadowPods(shadowPodList *vkv1alpha1.ShadowPodList, pi *peeringInfo, spMap map[string]string) {
	for i := range shadowPodList.Items {
		shadowPod := &shadowPodList.Items[i]
		_, found := pi.getShadowPodDescription(shadowPod.GetName())
		if !found {
			webhookrefreshlog.Info(fmt.Sprintf("[ REFRESH ] ShadowPod %s not found in cache", shadowPod.GetName()))
			// TODO: this should never happen, but maybe we need to manage also this case better
			continue
		}
		spMap[shadowPod.GetName()] = shadowPod.GetName()
	}
}

func alignShadowPodDescriptions(pi *peeringInfo, spMap map[string]string) {
	for _, shadowPodDescription := range pi.getAllShadowPodDescription() {
		if _, ok := spMap[shadowPodDescription.getName()]; !shadowPodDescription.isRunning() && !ok {
			pi.removeShadowPod(shadowPodDescription)
			webhookrefreshlog.Info("[ REFRESH ] ShadowPodDescription " + shadowPodDescription.getName() + " removed from cache")
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

	// Check if there are new ResourceOffers in the system snapshot
	for i := range resourceOfferList.Items {
		ro := &resourceOfferList.Items[i]
		clusterID := ro.Labels[discovery.ClusterIDLabel]
		if clusterID == "" {
			return fmt.Errorf("ResourceOffer %s has no cluster id", ro.Name)
		}

		// Check if the ResourceOffer is not present in the cache
		if _, found := spv.PeeringCache.peeringInfo.Load(clusterID); !found {
			webhookrefreshlog.Info("[ REFRESH ] ResourceOffer " + ro.Name + " not found in cache, adding it")
			newPI := createPeeringInfo(clusterID, ro.OwnerReferences[0].Name, getQuotaFromResourceOffer(ro))
			newPI.Lock()

			// Get the List of ShadowPods running on the cluster
			if err := spv.getShadowPodListByClusterID(ctx, &shadowPodList, clusterID); err != nil {
				return err
			}

			// Create the ShadowPodDescription for each ShadowPod
			for i := range shadowPodList.Items {
				shadowPod := &shadowPodList.Items[i]
				// Add the ShadowPodDescription to the PeeringInfo
				newPI.addShadowPod(createShadowPodDescription(shadowPod.GetName(), string(shadowPod.GetUID()), getQuotaFromShadowPod(shadowPod)))
			}
			newPI.Unlock()

			// Add the PeeringInfo to the cache
			spv.PeeringCache.addPeeringToCache(clusterID, newPI)
		}
	}

	// Check if PeeringInfos still have corresponding ResourceOffers
	spv.PeeringCache.peeringInfo.Range(
		func(key, value interface{}) bool {
			peeringInfo := value.(*peeringInfo)
			clusterID := key.(string)
			foundRO := false

			// Check if the corresponding ResourceOffer is still present in the system snapshot and there are some updates
			for j := range resourceOfferList.Items {
				ro := &resourceOfferList.Items[j]
				if clusterID == ro.Labels[discovery.ClusterIDLabel] {
					foundRO = true
					webhookrefreshlog.Info("[ REFRESH ] Checking for some ResourceOffer Quota updates")
					peeringInfo.Lock()
					resourceOfferUpdates(ro, peeringInfo)
					peeringInfo.Unlock()
					break
				}
			}

			// If the corresponding ResourceOffer is not present anymore, remove the PeeringInfo from the cache
			if !foundRO {
				webhookrefreshlog.Info("[ REFRESH ] ResourceOffer " + clusterID + " not found in system snapshot, removing it from cache")
				spv.PeeringCache.deletePeeringFromCache(clusterID)
			}
			return true
		},
	)

	return nil
}

func resourceOfferUpdates(ro *sharing.ResourceOffer, pi *peeringInfo) {
	quota := pi.getQuota()
	isUpdated := false
	for key, value := range ro.Spec.ResourceQuota.Hard {
		if value != quota[key] {
			isUpdated = true
			break
		}
	}
	if isUpdated {
		newQuota := getQuotaFromResourceOffer(ro)
		spdList := pi.getAllShadowPodDescription()
		newFreeQuota := newQuota.DeepCopy()
		for _, spd := range spdList {
			for key, val := range spd.getQuota() {
				if prevFree, ok := newFreeQuota[key]; ok {
					prevFree.Sub(val)
					newFreeQuota[key] = prevFree
				} else {
					newFreeQuota[key] = val.DeepCopy()
				}
			}
		}
		webhookrefreshlog.Info("[ REFRESH ] Quota of PeeringInfo " + pi.getClusterID() + " [ " + pi.getClusterName() + " ] has been updated")
		pi.updatePeeringQuota(newQuota)
		pi.updateFreePeeringQuota(newFreeQuota)
	}
}
