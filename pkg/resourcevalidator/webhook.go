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

// Package resourcevalidator contains the validating webhook logic and the cache of peering information.
package resourcevalidator

import (
	"context"
	// "encoding/json".
	"fmt"
	"net/http"

	admissionv1 "k8s.io/api/admission/v1"
	v1 "k8s.io/api/core/v1"
	kerrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/webhook/admission"

	sharing "github.com/liqotech/liqo/apis/sharing/v1alpha1"
	vkv1alpha1 "github.com/liqotech/liqo/apis/virtualkubelet/v1alpha1"
)

// Manifests
// cache data semplication
// refactoring resource check with iteration on the map
// flag on controller manager
// readiness probe

// ShadowPodValidator is the handler used by the Validating Webhook to validate shadow pods.
type ShadowPodValidator struct {
	Client       client.Client
	PeeringCache *peeringCache
	decoder      *admission.Decoder
}

// NewShadowPodValidator creates a new shadow pod validator.
func NewShadowPodValidator(c client.Client) *ShadowPodValidator {
	return &ShadowPodValidator{
		Client:       c,
		PeeringCache: &peeringCache{map[string]*peeringInfo{}},
	}
}

// Handle is the funcion in charge of handling the request.
// nolint:gocritic // the signature of this method is imposed by controller runtime.
func (spv *ShadowPodValidator) Handle(ctx context.Context, req admission.Request) admission.Response {
	webhooklog.Info(fmt.Sprintf("\t\tOperation: %s", req.Operation))

	dryRun := *req.DryRun
	var obj runtime.RawExtension

	switch req.Operation {
	case admissionv1.Create:
		obj = req.Object
	case admissionv1.Delete:
		obj = req.OldObject
	default:
		return admission.Errored(http.StatusBadRequest, fmt.Errorf("unsupported operation %s", req.Operation))
	}

	// Decode the shadow pod
	shadowpod, decodeErr := spv.DecodeShadowPod(obj)
	if decodeErr != nil {
		return admission.Errored(http.StatusBadRequest, decodeErr)
	}

	// Check existence and get shadow pod origin Cluster ID label
	clusterID, found := shadowpod.Labels["virtualkubelet.liqo.io/origin"]
	if !found {
		return admission.Denied("missing origin Cluster ID label")
	}

	ns := &v1.Namespace{}
	if err := spv.Client.Get(ctx, client.ObjectKey{Name: shadowpod.GetNamespace()}, ns); err != nil {
		return admission.Errored(http.StatusBadRequest, err)
	}

	nsClusterID, found := ns.Labels["liqo.io/remote-cluster-id"]
	if !found || nsClusterID != clusterID {
		return admission.Denied("Namespace Cluster ID label does not match with the ShadowPod Cluster ID or does not exist")
	}

	switch req.Operation {
	case admissionv1.Create:
		return spv.HandleCreate(ctx, req, shadowpod, clusterID, dryRun)
	case admissionv1.Delete:
		return spv.HandleDelete(ctx, req, shadowpod, clusterID, dryRun)
	default:
		return admission.Denied("Unsupported operation")
	}
}

// InjectDecoder injects the decoder.
func (spv *ShadowPodValidator) InjectDecoder(d *admission.Decoder) error {
	spv.decoder = d
	return nil
}

// HandleCreate is the function in charge of handling Creation requests.
//nolint:gocritic // the signature of this method is imposed by controller runtime.
func (spv *ShadowPodValidator) HandleCreate(ctx context.Context, req admission.Request, shadowpod *vkv1alpha1.ShadowPod,
	clusterID string, dryRun bool) admission.Response {
	spQuota := getQuotaFromShadowPod(shadowpod)

	shadowpodlog.Info(fmt.Sprintf("\tShadowPod %s decoded: UID: %s - clusterID %s", shadowpod.Name, shadowpod.GetUID(), clusterID))

	// Check existence and get resource offer by Cluster ID label
	resourceoffer, err := spv.getResourceOfferByLabel(ctx, clusterID)
	if err != nil {
		// TODO: to be improved
		return admission.Errored(http.StatusBadRequest, err)
	}

	// Get ResourceOffer Quota
	roQuota := getQuotaFromResourceOffer(resourceoffer)

	resourceofferlog.Info(fmt.Sprintf("ResourceOffer found for clusterID %s with %s ", clusterID, quotaFormatter(roQuota, "Quota")))

	peeringInfo := getOrCreatePeeringInfo(spv.PeeringCache, clusterID, roQuota)

	spd, found := peeringInfo.getShadowPodDescription(shadowpod.GetName())
	if !found {
		// Create a new ShadowPodDescription for caching purposes
		spd = createShadowPodDescription(shadowpod.GetName(), string(shadowpod.GetUID()), spQuota)
	} else if spd.running {
		return admission.Denied("Cannot create: ShadowPod is already up and running")
	}

	shadowpodlog.Info(quotaFormatter(spd.getQuota(), "\tShadowPod resource limits"))

	err = peeringInfo.testAndUpdatePeeringInfo(spd, admissionv1.Create, dryRun)
	if err != nil {
		return admission.Denied(err.Error())
	}

	spv.PeeringCache.updatePeeringInCache(clusterID, peeringInfo)

	return admission.Allowed("allowed")
}

// HandleDelete is the function in charge of handling Deletion requests.
//nolint:gocritic // the signature of this method is imposed by controller runtime.
func (spv *ShadowPodValidator) HandleDelete(ctx context.Context, req admission.Request, shadowpod *vkv1alpha1.ShadowPod,
	clusterID string, dryRun bool) admission.Response {
	resourceoffer, err := spv.getResourceOfferByLabel(ctx, clusterID)
	if err != nil {
		// TODO: to be improved
		return admission.Errored(http.StatusBadRequest, err)
	}

	roQuota := getQuotaFromResourceOffer(resourceoffer)

	resourceofferlog.Info(fmt.Sprintf("ResourceOffer found for clusterID %s with %s ", clusterID, quotaFormatter(roQuota, "Quota")))

	peeringInfo, foundPI := spv.PeeringCache.getPeeringFromCache(clusterID)
	if !foundPI {
		// TODO: has to be an error alert, because it should never happen
		webhooklog.Info(fmt.Sprintf("PeeringInfo not found for clusterID %s", clusterID))
		peeringInfo = createPeeringInfo(clusterID, roQuota)
		spv.PeeringCache.addPeeringToCache(clusterID, peeringInfo)
		//return admission.Allowed("allowed")
	}

	spd, foundSPD := peeringInfo.getShadowPodDescription(shadowpod.GetName())
	check := spd.getUID() == string(shadowpod.GetUID())

	if !foundSPD {
		spd = createShadowPodDescription(shadowpod.GetName(), string(shadowpod.GetUID()), getQuotaFromShadowPod(shadowpod))
		// TODO: to be improved/checked. not at all convinced that this is the right way to do it
		if foundPI {
			spd.terminate()
			peeringInfo.updateShadowPod(spd)
			return admission.Errored(http.StatusBadRequest, fmt.Errorf("cannot delete: ShadowPod %s not found (Maybe Cache problem)", shadowpod.GetName()))
		}
	} else {
		if !check {
			return admission.Errored(http.StatusBadRequest, fmt.Errorf("ShadowPod %s: UID mismatch", shadowpod.GetName()))
		}
	}

	err = peeringInfo.testAndUpdatePeeringInfo(spd, admissionv1.Delete, dryRun)
	if err != nil {
		return admission.Denied(err.Error())
	}

	spv.PeeringCache.updatePeeringInCache(clusterID, peeringInfo)

	return admission.Allowed("allowed")
}

func (spv *ShadowPodValidator) getResourceOfferByLabel(ctx context.Context, label string) (*sharing.ResourceOffer, error) {
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

// checkValidShadowPod checks if the shadow pod is valid.
/* func checkValidShadowPod(pi *peeringInfo, spd ShadowPodDescription, roQuota v1.ResourceList, clusterID string) admission.Response {

	return admission.Allowed("allowed")
} */
