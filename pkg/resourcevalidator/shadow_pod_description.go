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
	"time"

	v1 "k8s.io/api/core/v1"
)

// ShadowPodDescription is a struct that contains the main informations about a shadow pod.
type ShadowPodDescription struct {
	UID       string
	Quota     v1.ResourceList
	timestamp string
	running   bool
}

func createShadowPodDescription(uid string, resources v1.ResourceList) ShadowPodDescription {
	return ShadowPodDescription{
		UID:       uid,
		Quota:     resources.DeepCopy(),
		timestamp: time.Now().Format(time.RFC3339),
		running:   true,
	}
}

func (spd *ShadowPodDescription) terminate() {
	spd.running = false
	spd.timestamp = time.Now().Format(time.RFC3339)
}

func (spd ShadowPodDescription) isRunning() bool {
	return spd.running
}

func (spd ShadowPodDescription) getQuota() v1.ResourceList {
	return spd.Quota
}

func (spd ShadowPodDescription) getUID() string {
	return spd.UID
}
