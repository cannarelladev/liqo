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
	"fmt"
	"sync"
	"time"

	admissionv1 "k8s.io/api/admission/v1"
	v1 "k8s.io/api/core/v1"
)

type peeringInfo struct {
	clusterID        string
	clusterName      string
	SPList           map[string]ShadowPodDescription
	PeeringQuota     v1.ResourceList
	FreePeeringQuota v1.ResourceList
	piMutex          sync.RWMutex
	LastUpdateTime   string
	// running          bool
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

/* func (pi *peeringInfo) isRunning() bool {
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
} */

func getOrCreatePeeringInfo(cache *peeringCache, clusterID, clusterName string, roQuota v1.ResourceList) *peeringInfo {
	peeringInfo, found := cache.getPeeringFromCache(clusterID)
	if !found {
		webhooklog.Info(fmt.Sprintf("\t\tPeeringInfo not found for clusterID %s", clusterID))
		peeringInfo = createPeeringInfo(clusterID, clusterName, roQuota)
		webhooklog.Info(fmt.Sprintf("\t\tPeeringInfo created for clusterID %s", clusterID))
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
	}
	pi.LastUpdateTime = time.Now().Format(time.RFC3339)
}

func createPeeringInfo(clusterID, clusterName string, resources v1.ResourceList) *peeringInfo {
	return &peeringInfo{
		clusterID:    clusterID,
		clusterName:  clusterName,
		SPList:       map[string]ShadowPodDescription{},
		PeeringQuota: resources.DeepCopy(),
		//UsedPeeringQuota: generateQuotaPattern(resources),
		FreePeeringQuota: resources.DeepCopy(),
		piMutex:          sync.RWMutex{},
		LastUpdateTime:   time.Now().Format(time.RFC3339),
	}
}

func (pi *peeringInfo) getClusterID() string {
	return pi.clusterID
}

func (pi *peeringInfo) getClusterName() string {
	return pi.clusterName
}

func (pi *peeringInfo) updatePeeringQuota(resources v1.ResourceList) {
	webhookrefreshlog.Info(quotaFormatter(pi.PeeringQuota, "\t\tOld Peering Quota %s"))
	pi.PeeringQuota = resources.DeepCopy()
	webhookrefreshlog.Info(quotaFormatter(pi.PeeringQuota, "\t\tNew Peering Quota %s"))
}

func (pi *peeringInfo) updateFreePeeringQuota(resources v1.ResourceList) {
	webhookrefreshlog.Info(quotaFormatter(pi.FreePeeringQuota, "\t\tOld Free Quota %s"))
	pi.FreePeeringQuota = resources.DeepCopy()
	webhookrefreshlog.Info(quotaFormatter(pi.FreePeeringQuota, "\t\tNew Free Quota %s"))
}

func (pi *peeringInfo) addShadowPod(spd ShadowPodDescription) {
	pi.SPList[spd.getName()] = spd
	pi.subtractResources(spd.Quota)
}

func (pi *peeringInfo) terminateShadowPod(spd ShadowPodDescription) {
	spd.terminate()
	pi.SPList[spd.getName()] = spd
	pi.addResources(spd.Quota)
}

func (pi *peeringInfo) removeShadowPod(spd ShadowPodDescription) {
	delete(pi.SPList, spd.getName())
}

func (pi *peeringInfo) updateShadowPod(spd ShadowPodDescription) {
	pi.SPList[spd.getName()] = spd
}

func (pi *peeringInfo) getQuota() v1.ResourceList {
	return pi.PeeringQuota
}

func (pi *peeringInfo) getUsedQuota() v1.ResourceList {
	UsedQuota := v1.ResourceList{}
	Quota := pi.getQuota()
	FreeQuota := pi.getFreeQuota()
	for key, val := range Quota {
		tmpQuota := val.DeepCopy()
		tmpQuota.Sub(FreeQuota[key])
		UsedQuota[key] = tmpQuota
	}
	return UsedQuota
}

func (pi *peeringInfo) getFreeQuota() v1.ResourceList {
	return pi.FreePeeringQuota
}

func (pi *peeringInfo) getShadowPodDescription(uid string) (ShadowPodDescription, bool) {
	spd, ok := pi.SPList[uid]
	return spd, ok
}

func (pi *peeringInfo) getAllShadowPodDescription() map[string]ShadowPodDescription {
	return pi.SPList
}

func (pi *peeringInfo) testAndUpdatePeeringInfo(spd ShadowPodDescription, operation admissionv1.Operation, dryRun bool) error {
	cachelog.Info(fmt.Sprintf("\tOperation: %s", operation))
	pi.Lock()
	cachelog.Info(quotaFormatter(spd.getQuota(), "\tShadowPodQuota"))
	cachelog.Info(quotaFormatter(pi.getQuota(), "\tPeeringInfo Quota"))
	cachelog.Info(quotaFormatter(pi.getUsedQuota(), "\tPeeringInfo UsedQuota"))
	cachelog.Info(quotaFormatter(pi.getFreeQuota(), "\tPeeringInfo FreeQuota"))

	switch operation {
	case admissionv1.Create:
		if err := pi.checkResources(spd); err != nil {
			pi.Unlock()
			return err
		}
		if !dryRun {
			pi.addShadowPod(spd)
		}
		cachelog.Info(quotaFormatter(pi.PeeringQuota, "\tUpdated PeeringInfo Quota"))
		cachelog.Info(quotaFormatter(pi.getUsedQuota(), "\tUpdated PeeringInfo UsedQuota"))
		cachelog.Info(quotaFormatter(pi.FreePeeringQuota, "\tUpdated PeeringInfo FreeQuota"))
		pi.Unlock()
		return nil
	case admissionv1.Delete:
		if !dryRun {
			pi.terminateShadowPod(spd)
		}
		cachelog.Info(quotaFormatter(pi.PeeringQuota, "\tUpdated PeeringInfo Quota"))
		cachelog.Info(quotaFormatter(pi.getUsedQuota(), "\tUpdated PeeringInfo UsedQuota"))
		cachelog.Info(quotaFormatter(pi.FreePeeringQuota, "\tUpdated PeeringInfo FreeQuota"))
		pi.Unlock()
		return nil
	default:
		pi.Unlock()
		return fmt.Errorf("PEERING INFO: operation not supported")
	}
}

func (pi *peeringInfo) checkResources(spd ShadowPodDescription) error {
	cpuFlag := false
	memoryFlag := false
	for key, val := range spd.getQuota() {
		if freeQuota, ok := pi.FreePeeringQuota[key]; ok {
			if key == v1.ResourceCPU {
				cpuFlag = true
			}
			if key == v1.ResourceMemory {
				memoryFlag = true
			}
			if freeQuota.Value() < val.Value() {
				return fmt.Errorf("PEERING INFO: %s quota usage exceeded - FREE %s / REQUESTED %s",
					key,
					freeQuota.String(),
					val.String())
			}
		} else {
			return fmt.Errorf("PEERING INFO: Peering %s quota not found in the PeeringInfo", key)
		}
	}
	if !cpuFlag || !memoryFlag {
		return fmt.Errorf("PEERING INFO: Peering CPU or Memory quota not correctly defined for the ShadowPod %s", spd.getName())
	}
	return nil
}
