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
	"fmt"
	"sync"
	"time"

	admissionv1 "k8s.io/api/admission/v1"
	v1 "k8s.io/api/core/v1"
)

type peeringInfo struct {
	ClusterID        string
	SPList           map[string]ShadowPodDescription
	PeeringQuota     v1.ResourceList
	UsedPeeringQuota v1.ResourceList
	FreePeeringQuota v1.ResourceList
	piMutex          sync.RWMutex
	LastUpdateTime   string
	running          bool
}

/**
 * PeeringInfo methods
 */

func (pi *peeringInfo) Lock() {
	pi.piMutex.Lock()
}

func (pi *peeringInfo) Unlock() {
	pi.piMutex.Unlock()
}

func (pi *peeringInfo) isRunning() bool {
	return pi.running
}

func (pi *peeringInfo) terminate() {
	pi.Lock()
	pi.running = false
	pi.Unlock()
}

func (pi *peeringInfo) start() {
	pi.Lock()
	pi.running = true
	pi.Unlock()
}

func getOrCreatePeeringInfo(cache *peeringCache, clusterID string, roQuota v1.ResourceList) *peeringInfo {
	peeringInfo, found := cache.getPeeringFromCache(clusterID)
	if !found {
		webhooklog.Info(fmt.Sprintf("\t\tPeeringInfo not found for ClusterID %s", clusterID))
		peeringInfo = createPeeringInfo(clusterID, roQuota)
		webhooklog.Info(fmt.Sprintf("\t\tPeeringInfo created for ClusterID %s", clusterID))
		webhooklog.Info(quotaFormatter(peeringInfo.getQuota(), "\t\tNew PeeringInfo Quota limits"))
		webhooklog.Info(quotaFormatter(peeringInfo.getUsedQuota(), "\t\tNew PeeringInfo UsedQuota limits"))
		webhooklog.Info(quotaFormatter(peeringInfo.getFreeQuota(), "\t\tNew PeeringInfo FreeQuota limits"))
		cache.addPeeringToCache(clusterID, peeringInfo)
	}
	return peeringInfo
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

func createPeeringInfo(clusterID string, resources v1.ResourceList) *peeringInfo {
	return &peeringInfo{
		ClusterID:        clusterID,
		SPList:           map[string]ShadowPodDescription{},
		PeeringQuota:     resources.DeepCopy(),
		UsedPeeringQuota: generateQuotaPattern(resources),
		FreePeeringQuota: resources.DeepCopy(),
		piMutex:          sync.RWMutex{},
		LastUpdateTime:   time.Now().Format(time.RFC3339),
	}
}

func (pi *peeringInfo) addShadowPod(spd ShadowPodDescription) {
	pi.SPList[spd.UID] = spd
	pi.subtractResources(spd.Quota)
}

func (pi *peeringInfo) terminateShadowPod(spd ShadowPodDescription) {
	spd.terminate()
	pi.SPList[spd.UID] = spd
	pi.addResources(spd.Quota)
}

func (pi *peeringInfo) removeShadowPod(spd ShadowPodDescription) {
	delete(pi.SPList, spd.UID)
}

func (pi *peeringInfo) updateShadowPod(spd ShadowPodDescription) {
	pi.SPList[spd.UID] = spd
}

func (pi *peeringInfo) getShadowPodDescription(uid string) (ShadowPodDescription, bool) {
	spd, ok := pi.SPList[uid]
	return spd, ok
}

func (pi *peeringInfo) getAllShadowPodDescription() map[string]ShadowPodDescription {
	return pi.SPList
}

func (pi *peeringInfo) testAndUpdatePeeringInfo(spd ShadowPodDescription, operation admissionv1.Operation) error {
	cachelog.Info(fmt.Sprintf("\tOperation: %s", operation))
	spdQuota := spd.getQuota()
	pi.Lock()
	defer pi.Unlock()
	cachelog.Info(quotaFormatter(spd.getQuota(), "\tShadowPodQuota"))
	cachelog.Info(quotaFormatter(pi.PeeringQuota, "\tPeeringInfo Quota"))
	cachelog.Info(quotaFormatter(pi.UsedPeeringQuota, "\tPeeringInfo UsedQuota"))
	cachelog.Info(quotaFormatter(pi.FreePeeringQuota, "\tPeeringInfo FreeQuota"))

	switch operation {
	case admissionv1.Create:
		if pi.FreePeeringQuota.Cpu().MilliValue() < spdQuota.Cpu().MilliValue() {
			cachelog.Info(
				fmt.Sprintf("\tPEERING INFO: Peering CPU quota usage exceeded - FREE %s / REQUESTED %s",
					pi.FreePeeringQuota.Cpu(),
					spdQuota.Cpu()),
			)
			return fmt.Errorf("PEERING INFO: Peering CPU quota usage exceeded")
		}
		if pi.FreePeeringQuota.Memory().Value() < spdQuota.Memory().Value() {
			cachelog.Info(
				fmt.Sprintf("\tPEERING INFO: Peering Memory quota usage exceeded - FREE %s / REQUESTED %s",
					pi.FreePeeringQuota.Memory(),
					spdQuota.Memory()),
			)
			return fmt.Errorf("PEERING INFO: Peering Memory quota usage exceeded")
		}
		if pi.FreePeeringQuota.Storage().Value() < spdQuota.Storage().Value() {
			cachelog.Info(
				fmt.Sprintf("\tPEERING INFO: Peering Storage quota usage exceeded - FREE %s / REQUESTED %s",
					pi.FreePeeringQuota.Storage(),
					spdQuota.Storage()),
			)
			return fmt.Errorf("PEERING INFO: Peering Disk quota usage exceeded")
		}
		pi.addShadowPod(spd)
		cachelog.Info(quotaFormatter(pi.PeeringQuota, "\tUpdated PeeringInfo Quota"))
		cachelog.Info(quotaFormatter(pi.UsedPeeringQuota, "\tUpdated PeeringInfo UsedQuota"))
		cachelog.Info(quotaFormatter(pi.FreePeeringQuota, "\tUpdated PeeringInfo FreeQuota"))
		return nil
	case admissionv1.Delete:
		cachelog.Info(quotaFormatter(spdQuota, "\tShadowPod Quota"))
		cachelog.Info(quotaFormatter(pi.PeeringQuota, "\tPeeringInfo Quota"))
		cachelog.Info(quotaFormatter(pi.UsedPeeringQuota, "\tPeeringInfo UsedQuota"))
		cachelog.Info(quotaFormatter(pi.FreePeeringQuota, "\tPeeringInfo FreeQuota"))
		pi.terminateShadowPod(spd)
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
