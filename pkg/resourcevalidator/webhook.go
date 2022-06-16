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
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/utils/pointer"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/webhook/admission"

	vkv1alpha1 "github.com/liqotech/liqo/apis/virtualkubelet/v1alpha1"
	"github.com/liqotech/liqo/pkg/consts"
	liqogetters "github.com/liqotech/liqo/pkg/utils/getters"
	liqolabels "github.com/liqotech/liqo/pkg/utils/labels"
	pod "github.com/liqotech/liqo/pkg/utils/pod"
	"github.com/liqotech/liqo/pkg/virtualKubelet/forge"
)

// Manifests
// flag on controller manager

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
		PeeringCache: &peeringCache{ready: false},
	}
}

// Handle is the funcion in charge of handling the request.
// nolint:gocritic // the signature of this method is imposed by controller runtime.
func (spv *ShadowPodValidator) Handle(ctx context.Context, req admission.Request) admission.Response {
	webhooklog.Info(fmt.Sprintf("\t\tOperation: %s", req.Operation))

	if !spv.PeeringCache.ready {
		webhooklog.Info("PeeringCache not ready")
		return admission.Errored(http.StatusInternalServerError, fmt.Errorf("PeeringCache not ready"))
	}

	var obj runtime.RawExtension

	switch req.Operation {
	case admissionv1.Create:
		obj = req.Object
	case admissionv1.Delete:
		obj = req.OldObject
	case admissionv1.Update:
		return spv.HandleUpdate(req)
	default:
		return admission.Errored(http.StatusBadRequest, fmt.Errorf("unsupported operation %s", req.Operation))
	}

	// Decode the shadow pod
	shadowpod, err := spv.DecodeShadowPod(obj)
	if err != nil {
		return admission.Errored(http.StatusBadRequest, err)
	}

	// Check existence and get shadow pod origin Cluster ID label
	spClusterID, found := shadowpod.Labels[forge.LiqoOriginClusterIDKey]
	if !found {
		return admission.Denied("missing origin Cluster ID label")
	}

	// Get ShadowPod Namespace
	namespace := &corev1.Namespace{}
	if err := spv.Client.Get(ctx, client.ObjectKey{Name: shadowpod.GetNamespace()}, namespace); err != nil {
		return admission.Errored(http.StatusBadRequest, err)
	}

	// Get Cluster ID origin label of the namespace
	nsClusterID, found := namespace.Labels[consts.RemoteClusterID]
	if !found || nsClusterID != spClusterID {
		webhooklog.Info(fmt.Sprintf("\t\tNamespace Cluster ID [ %s ] does not match the ShadowPod Cluster ID [ %s ]", nsClusterID, spClusterID))
		return admission.Denied("Namespace Cluster ID label does not match the ShadowPod Cluster ID label")
	}

	switch req.Operation {
	case admissionv1.Create:
		return spv.HandleCreate(ctx, req, shadowpod, nsClusterID, pointer.BoolDeref(req.DryRun, false))
	case admissionv1.Delete:
		return spv.HandleDelete(ctx, req, shadowpod, nsClusterID, pointer.BoolDeref(req.DryRun, false))
	default:
		return admission.Denied("Unsupported operation")
	}
}

// HandleCreate is the function in charge of handling Creation requests.
//nolint:gocritic // the signature of this method is imposed by controller runtime.
func (spv *ShadowPodValidator) HandleCreate(ctx context.Context, req admission.Request, shadowpod *vkv1alpha1.ShadowPod,
	clusterID string, dryRun bool) admission.Response {
	spQuota := getQuotaFromShadowPod(shadowpod)

	shadowpodlog.Info(fmt.Sprintf("\tShadowPod %s decoded: UID: %s - clusterID %s", shadowpod.Name, shadowpod.GetUID(), clusterID))

	// Check existence and get resource offer by Cluster ID label

	resourceoffer, err := liqogetters.GetResourceOfferByLabel(ctx, spv.Client, corev1.NamespaceAll,
		liqolabels.LocalLabelSelector(clusterID))
	//resourceoffer, err := spv.getResourceOfferByLabel(ctx, clusterID)
	if err != nil {
		error := fmt.Errorf("error getting resource offer by label: %w", err)
		resourceofferlog.Info(error.Error())
		return admission.Errored(http.StatusInternalServerError, error)
	}

	// Get ResourceOffer Quota
	roQuota := getQuotaFromResourceOffer(resourceoffer)

	resourceofferlog.Info(fmt.Sprintf("ResourceOffer found for clusterID %s with %s ", clusterID, quotaFormatter(roQuota, "Quota")))

	peeringInfo := getOrCreatePeeringInfo(spv.PeeringCache, clusterID, resourceoffer.OwnerReferences[0].Name, roQuota)

	spd, found := peeringInfo.getShadowPodDescription(shadowpod.GetName())
	if !found {
		// Create a new ShadowPodDescription for caching purposes
		spd = createShadowPodDescription(shadowpod.GetName(), string(shadowpod.GetUID()), spQuota)
	} else if spd.running {
		return admission.Denied("ShadowPod already exists")
	}

	shadowpodlog.Info(quotaFormatter(spd.getQuota(), "\tShadowPod resource limits"))

	err = peeringInfo.testAndUpdatePeeringInfo(spd, admissionv1.Create, dryRun)
	if err != nil {
		return admission.Denied(err.Error())
	}

	return admission.Allowed("")
}

// HandleUpdate is the function in charge of handling Update requests.
func (spv *ShadowPodValidator) HandleUpdate(req admission.Request) admission.Response {
	obj := req.Object
	oldObj := req.OldObject
	shadowpod, err := spv.DecodeShadowPod(obj)
	oldShadowpod, oldErr := spv.DecodeShadowPod(oldObj)
	if err != nil || oldErr != nil {
		return admission.Errored(http.StatusBadRequest, err)
	}
	if pod.CheckShadowPodUpdate(&shadowpod.Spec.Pod, &oldShadowpod.Spec.Pod) {
		return admission.Allowed("")
	}
	return admission.Denied("")
}

// HandleDelete is the function in charge of handling Deletion requests.
//nolint:gocritic // the signature of this method is imposed by controller runtime.
func (spv *ShadowPodValidator) HandleDelete(ctx context.Context, req admission.Request, shadowpod *vkv1alpha1.ShadowPod,
	clusterID string, dryRun bool) admission.Response {
	peeringInfo, foundPI := spv.PeeringCache.getPeeringFromCache(clusterID)
	if !foundPI {
		webhooklog.Info(fmt.Sprintf("PeeringInfo not found for clusterID %s. Creating...", clusterID))

		resourceoffer, err := liqogetters.GetResourceOfferByLabel(ctx, spv.Client, corev1.NamespaceAll,
			liqolabels.LocalLabelSelector(clusterID))
		//resourceoffer, err := spv.getResourceOfferByLabel(ctx, clusterID)
		if err != nil {
			error := fmt.Errorf("error getting resource offer by label: %w", err)
			resourceofferlog.Info(error.Error())
			return admission.Errored(http.StatusInternalServerError, error)
		}
		roQuota := getQuotaFromResourceOffer(resourceoffer)
		resourceofferlog.Info(fmt.Sprintf("ResourceOffer found for clusterID %s with %s ", clusterID, quotaFormatter(roQuota, "Quota")))

		peeringInfo = createPeeringInfo(clusterID, resourceoffer.OwnerReferences[0].Name, roQuota)
		spv.PeeringCache.addPeeringToCache(clusterID, peeringInfo)
	}

	spd, foundSPD := peeringInfo.getShadowPodDescription(shadowpod.GetName())
	check := spd.getUID() == string(shadowpod.GetUID())

	if !foundSPD {
		spd = createShadowPodDescription(shadowpod.GetName(), string(shadowpod.GetUID()), getQuotaFromShadowPod(shadowpod))
		if foundPI {
			spd.terminate()
			peeringInfo.Lock()
			peeringInfo.updateShadowPod(spd)
			peeringInfo.Unlock()
			error := fmt.Errorf("cannot delete: ShadowPod %s not found (Maybe Cache problem)", shadowpod.GetName())
			webhooklog.Info(error.Error())
			return admission.Errored(http.StatusBadRequest, error)
		}
	} else if !check {
		error := fmt.Errorf("ShadowPod %s: UID mismatch", shadowpod.GetName())
		webhooklog.Info(error.Error())
		return admission.Errored(http.StatusBadRequest, error)
	}

	if err := peeringInfo.testAndUpdatePeeringInfo(spd, admissionv1.Delete, dryRun); err != nil {
		cachelog.Info(err.Error())
		return admission.Denied(err.Error())
	}

	return admission.Allowed("")
}

// InjectDecoder injects the decoder.
func (spv *ShadowPodValidator) InjectDecoder(d *admission.Decoder) error {
	spv.decoder = d
	return nil
}
