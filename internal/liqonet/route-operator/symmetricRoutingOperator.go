package routeoperator

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"sync"

	"golang.org/x/sys/unix"
	corev1 "k8s.io/api/core/v1"
	k8sApiErrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/klog/v2"
	"k8s.io/kubernetes/pkg/api/v1/pod"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/predicate"

	liqoerrors "github.com/liqotech/liqo/pkg/liqonet/errors"
	"github.com/liqotech/liqo/pkg/liqonet/overlay"
	"github.com/liqotech/liqo/pkg/liqonet/routing"
)

// SymmetricRoutingController reconciles pods objects, in our case all the existing pods.
type SymmetricRoutingController struct {
	client.Client
	vxlanDev       overlay.VxlanDevice
	nodeName       string
	routingTableID int
	nodesLock      *sync.RWMutex
	vxlanNodes     map[string]string
	routes         map[string]string
}

// Reconcile for a given pod, based on the node where the pod is running, configures a route to send the traffic
// through the overlay network. The route is used only for the traffic generated on remote clusters with
// destination address a pod running on the local cluster. The traffic for local pods should be handled by
// the local CNI but if the reverse path filtering is in strict mode (default) the traffic coming from a
// network interface where the source address is not routable by the same interface is dropped. To solve the
// issue it requires to set the reverse path filtering to loose mode, but some times it is not possible. To
// overcome the problem all the traffic sent to or coming from a peering cluster has to be routed using the
// overlay network.
func (src *SymmetricRoutingController) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	var p corev1.Pod
	var err error
	err = src.Get(ctx, req.NamespacedName, &p)
	if err != nil && !k8sApiErrors.IsNotFound(err) {
		klog.Errorf("an error occurred while getting pod {%s}: %v", req.NamespacedName, err)
		return ctrl.Result{}, err
	}
	if k8sApiErrors.IsNotFound(err) {
		// Remove the peer.
		deleted, err := src.delRoute(req)
		if err != nil {
			return ctrl.Result{}, err
		}
		if deleted {
			klog.Infof("successfully removed route for pod {%s} from vxlan overlay network", req.String())
		}
		return ctrl.Result{}, nil
	}
	added, err := src.addRoute(req, &p)
	if err != nil {
		klog.Errorf("an error occurred while adding route for pod {%s} with IP address {{%s}} to the vxlan overlay network: %v",
			req.String(), p.Status.PodIP, err)
		return ctrl.Result{}, err
	}
	if added {
		klog.Errorf("successfully added route for pod {%s} with IP address {%s} to the vxlan overlay network: %v",
			req.String(), p.Status.PodIP)
	}
	return ctrl.Result{}, nil
}

// NewSymmetricRoutingOperator returns a new controller ready to be setup and started with the controller manager.
func NewSymmetricRoutingOperator(nodeName string, routingTableID int, vxlanDevice overlay.VxlanDevice,
	nodesLock *sync.RWMutex, vxlanNodes map[string]string, cl client.Client) (*SymmetricRoutingController, error) {
	// Check the validity of input parameters.
	if routingTableID > unix.RT_TABLE_MAX {
		return nil, &liqoerrors.WrongParameter{Parameter: "routingTableID", Reason: liqoerrors.MinorOrEqual + strconv.Itoa(unix.RT_TABLE_MAX)}
	}
	if routingTableID < 0 {
		return nil, &liqoerrors.WrongParameter{Parameter: "routingTableID", Reason: liqoerrors.GreaterOrEqual + strconv.Itoa(0)}
	}
	if vxlanDevice.Link == nil {
		return nil, &liqoerrors.WrongParameter{Parameter: "vxlanDevice.Link", Reason: liqoerrors.NotNil}
	}
	return &SymmetricRoutingController{
		Client:         cl,
		vxlanDev:       vxlanDevice,
		nodeName:       nodeName,
		routingTableID: routingTableID,
		nodesLock:      nodesLock,
		vxlanNodes:     vxlanNodes,
		routes:         map[string]string{},
	}, nil
}

// addPeer adds route for the given pod. It return true when the route dose not exists
// and is added, false if the route does already exist. An error is return if something
// goes wrong.
func (src *SymmetricRoutingController) addRoute(req ctrl.Request, p *corev1.Pod) (bool, error) {
	src.nodesLock.RLock()
	defer src.nodesLock.RUnlock()
	nodeIP, ok := src.vxlanNodes[p.Spec.NodeName]
	if !ok {
		return false, fmt.Errorf("ip for node {%s} has not been set yet", p.Spec.NodeName)
	}
	gwIP := overlay.GetOverlayIP(nodeIP)
	dstNet := strings.Join([]string{p.Status.PodIP, "32"}, "/")
	added, err := routing.AddRoute(dstNet, gwIP, src.vxlanDev.Link.Attrs().Index, src.routingTableID)
	if err != nil {
		return added, err
	}
	src.routes[req.String()] = dstNet
	return added, err
}

// delRoute removes route for the given pod. It returns true when the route exists
// and is removed. False if the route does not exist. An error if something goes
// wrong.
func (src *SymmetricRoutingController) delRoute(req ctrl.Request) (bool, error) {
	dstNet, ok := src.routes[req.String()]
	if !ok {
		return false, nil
	}
	deleted, err := routing.DelRoute(dstNet, "", src.vxlanDev.Link.Attrs().Index, src.routingTableID)
	if err != nil {
		return deleted, err
	}
	delete(src.routes, req.String())
	return deleted, nil
}

// podFilter used to filter out all the pods that are running on the same node
// as the operator or pods that are not ready. We do not need to configure routes
// for pods running on same node.
func (src *SymmetricRoutingController) podFilter(obj client.Object) bool {
	// Check if the object is a pod.
	p, ok := obj.(*corev1.Pod)
	if !ok {
		klog.Infof("object {%s} is not of type corev1.Pod", obj.GetName())
		return false
	}
	// Check if pod is running on same node as the operator.
	if p.Spec.NodeName == src.nodeName {
		klog.V(4).Infof("skipping pod {%s} running on our same node {%s}", p.Name, p.Spec.NodeName)
		return false
	}
	// If pod is not ready return false.
	if !pod.IsPodReady(p) {
		return false
	}
	return true
}

// SetupWithManager used to set up the controller with a given manager.
func (src *SymmetricRoutingController) SetupWithManager(mgr ctrl.Manager) error {
	p := predicate.NewPredicateFuncs(src.podFilter)
	return ctrl.NewControllerManagedBy(mgr).For(&corev1.Pod{}).WithEventFilter(p).
		Complete(src)
}
