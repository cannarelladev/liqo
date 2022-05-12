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
	"sync"

	//"encoding/json"
	"fmt"
	"net/http"

	admissionv1 "k8s.io/api/admission/v1"
	v1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/labels"
	"sigs.k8s.io/controller-runtime/pkg/client"

	vkv1alpha1 "github.com/liqotech/liqo/apis/virtualkubelet/v1alpha1"
	logf "sigs.k8s.io/controller-runtime/pkg/log"

	kerrors "k8s.io/apimachinery/pkg/api/errors"

	sharing "github.com/liqotech/liqo/apis/sharing/v1alpha1"
	"sigs.k8s.io/controller-runtime/pkg/webhook/admission"
)

// log is for logging in this package.
var shadowpodlog = logf.Log.WithName("[ shadowpod-resource ]")
var webhooklog = logf.Log.WithName("[ webhook ]")
var resourceofferlog = logf.Log.WithName("[ resourceoffer-resource ]")

type shadowPodValidator struct {
	Client       client.Client
	PeeringCache *peeringCache
	decoder      *admission.Decoder
}

// NewShadowPodValidator creates a new shadow pod validator.
func NewShadowPodValidator(c client.Client) admission.Handler {
	return &shadowPodValidator{
		Client:       c,
		PeeringCache: &peeringCache{sync.RWMutex{}, map[string]peeringInfo{}},
	}
}

func (spv *shadowPodValidator) Handle(ctx context.Context, req admission.Request) admission.Response {

	webhooklog.Info(fmt.Sprintf("\t\tOperation: %s", req.Operation))

	switch req.Operation {
	case admissionv1.Create:
		return spv.HandleCreate(ctx, req)
	case admissionv1.Delete:
		return spv.HandleDelete(ctx, req)
	default:
		return admission.Denied("Unsupported operation")
	}
}

// InjectDecoder injects the decoder.
func (spv *shadowPodValidator) InjectDecoder(d *admission.Decoder) error {
	spv.decoder = d
	return nil
}

func (spv *shadowPodValidator) HandleCreate(ctx context.Context, req admission.Request) admission.Response {
	shadowpod, decodeErr := spv.DecodeShadowPod(req.Object)
	if decodeErr != nil {
		return admission.Errored(http.StatusBadRequest, decodeErr)
	}

	clusterID, found := shadowpod.Labels["virtualkubelet.liqo.io/origin"]
	if !found {
		return admission.Denied("missing origin Cluster ID label")
	}

	shadowpodlog.Info(fmt.Sprintf("\tShadowPod %s decoded: UID: %s - ClusterID %s", shadowpod.Name, shadowpod.GetUID(), clusterID))

	resourceoffer, err := spv.getResourceOfferByLabel(ctx, clusterID)
	if err != nil {
		// TODO: to be improved
		return admission.Errored(http.StatusBadRequest, err)
	}

	resourceofferlog.Info(fmt.Sprintf("ResourceOffer founded for ClusterID %s", clusterID))

	return checkValidShadowPod(spv.PeeringCache, shadowpod, resourceoffer, clusterID)
}

func (spv *shadowPodValidator) HandleDelete(ctx context.Context, req admission.Request) admission.Response {
	shadowpod, decodeErr := spv.DecodeShadowPod(req.OldObject)
	if decodeErr != nil {
		return admission.Errored(http.StatusBadRequest, decodeErr)
	}

	clusterID, found := shadowpod.Labels["virtualkubelet.liqo.io/origin"]
	if !found {
		return admission.Denied("missing origin Cluster ID label")
	}

	resourceoffer, err := spv.getResourceOfferByLabel(ctx, clusterID)
	if err != nil {
		// TODO: to be improved
		return admission.Errored(http.StatusBadRequest, err)
	}

	spQuota := getQuotaFromShadowPod(shadowpod)
	roQuota := getQuotaFromResourceOffer(resourceoffer)

	peeringInfo, found := spv.PeeringCache.getPeeringFromCache(clusterID)
	if !found {
		peeringInfo = createPeeringInfo(clusterID, roQuota)
		spv.PeeringCache.addPeeringToCache(clusterID, peeringInfo)
		return admission.Allowed("allowed")
	}

	err = peeringInfo.testAndUpdatePeeringInfo(spQuota, admissionv1.Delete)
	if err != nil {
		return admission.Denied(err.Error())
	}

	spv.PeeringCache.updatePeeringInCache(clusterID, peeringInfo)

	return admission.Allowed("allowed")
}

func (spv *shadowPodValidator) getResourceOfferByLabel(ctx context.Context, label string) (*sharing.ResourceOffer, error) {
	resourceofferList := &sharing.ResourceOfferList{}
	if err := spv.Client.
		List(ctx, resourceofferList, &client.ListOptions{
			LabelSelector: labels.SelectorFromSet(map[string]string{"discovery.liqo.io/cluster-id": label}),
		}); err != nil {
		return nil, err
	}

	switch len(resourceofferList.Items) {
	case 0:
		return nil, kerrors.NewNotFound(sharing.ResourceOfferGroupResource, label)
	case 1:
		return &resourceofferList.Items[0], nil
	default:
		return nil, fmt.Errorf("multiple resource offers found with matching label %s", label)
	}
}

func getQuotaFromResourceOffer(resourceoffer *sharing.ResourceOffer) v1.ResourceList {
	resources := v1.ResourceList{
		v1.ResourceName(v1.ResourceCPU):     *resourceoffer.Spec.ResourceQuota.Hard.Cpu(),
		v1.ResourceName(v1.ResourceMemory):  *resourceoffer.Spec.ResourceQuota.Hard.Memory(),
		v1.ResourceName(v1.ResourceStorage): *resourceoffer.Spec.ResourceQuota.Hard.Storage(),
	}
	return resources
}

func getQuotaFromShadowPod(shadowpod *vkv1alpha1.ShadowPod) v1.ResourceList {
	resources := v1.ResourceList{
		v1.ResourceName(v1.ResourceCPU):     *shadowpod.Spec.Pod.Containers[0].Resources.Limits.Cpu(),
		v1.ResourceName(v1.ResourceMemory):  *shadowpod.Spec.Pod.Containers[0].Resources.Limits.Memory(),
		v1.ResourceName(v1.ResourceStorage): *shadowpod.Spec.Pod.Containers[0].Resources.Limits.Storage(),
	}
	return resources
}

// checkValidShadowPod checks if the shadow pod is valid.
func checkValidShadowPod(cache *peeringCache, sp *vkv1alpha1.ShadowPod, ro *sharing.ResourceOffer, clusterID string) admission.Response {
	spQuota := getQuotaFromShadowPod(sp)
	roQuota := getQuotaFromResourceOffer(ro)

	shadowpodlog.Info(quotaFormatter(spQuota, "\tShadowPod resource limits"))

	peeringInfo, found := cache.getPeeringFromCache(clusterID)
	if !found {
		webhooklog.Info(fmt.Sprintf("\t\tPeeringInfo not found for ClusterID %s", clusterID))
		resourceofferlog.Info(quotaFormatter(roQuota, "ResourceOffer resource limits"))
		peeringInfo = createPeeringInfo(clusterID, roQuota)
		webhooklog.Info(fmt.Sprintf("\t\tPeeringInfo created for ClusterID %s", clusterID))
		webhooklog.Info(quotaFormatter(peeringInfo.getQuota(), "\t\tNew PeeringInfo Quota limits"))
		webhooklog.Info(quotaFormatter(peeringInfo.getUsedQuota(), "\t\tNew PeeringInfo UsedQuota limits"))
		webhooklog.Info(quotaFormatter(peeringInfo.getFreeQuota(), "\t\tNew PeeringInfo FreeQuota limits"))
		cache.addPeeringToCache(clusterID, peeringInfo)
	}

	err := peeringInfo.testAndUpdatePeeringInfo(spQuota, admissionv1.Create)
	if err != nil {
		return admission.Denied(err.Error())
	}

	cache.updatePeeringInCache(clusterID, peeringInfo)

	return admission.Allowed("allowed")
}
