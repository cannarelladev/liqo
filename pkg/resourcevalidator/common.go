package resourceValidator

import (
	vkv1alpha1 "github.com/liqotech/liqo/apis/virtualkubelet/v1alpha1"
	"k8s.io/apimachinery/pkg/runtime"
)

func (spv *shadowPodValidator) DecodeShadowPod(obj runtime.RawExtension) (shadowpod *vkv1alpha1.ShadowPod, err error) {
	shadowpod = &vkv1alpha1.ShadowPod{}
	err = spv.decoder.DecodeRaw(obj, shadowpod)
	return
}
