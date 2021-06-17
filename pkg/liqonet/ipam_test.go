package liqonet_test

import (
	"context"
	"errors"
	"fmt"
	"math/rand"
	"strings"

	. "github.com/onsi/ginkgo"
	. "github.com/onsi/gomega"
	k8serrors "k8s.io/apimachinery/pkg/api/errors"
	v1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/dynamic/fake"

	liqonetapi "github.com/liqotech/liqo/apis/net/v1alpha1"
	"github.com/liqotech/liqo/pkg/consts"
	"github.com/liqotech/liqo/pkg/liqonet"
	liqoneterrors "github.com/liqotech/liqo/pkg/liqonet/errors"
	"github.com/liqotech/liqo/pkg/liqonet/natmappinginflater"
	"github.com/liqotech/liqo/pkg/liqonet/utils"
)

var ipam *liqonet.IPAM
var dynClient *fake.FakeDynamicClient

const (
	clusterID1           = "cluster1"
	clusterID2           = "cluster2"
	clusterID3           = "cluster3"
	homePodCIDR          = "10.0.0.0/24"
	localNATPodCIDR      = "10.0.1.0/24"
	localNATExternalCIDR = "192.168.30.0/24"
	remotePodCIDR        = "10.0.4.0/24"
	remoteExternalCIDR   = "192.168.4.0/24"
	externalEndpointIP   = "10.0.50.6"
	internalEndpointIP   = "10.0.0.6"
	invalidValue         = "invalid value"
)

func fillNetworkPool(pool string, ipam *liqonet.IPAM) error {

	// Get halves mask length
	mask := utils.GetMask(pool)
	mask += 1

	// Get first half CIDR
	halfCidr, err := utils.SetMask(pool, mask)
	if err != nil {
		return err
	}

	err = ipam.AcquireReservedSubnet(halfCidr)
	if err != nil {
		return err
	}

	// Get second half CIDR
	halfCidr, err = utils.Next(halfCidr)
	if err != nil {
		return err
	}
	err = ipam.AcquireReservedSubnet(halfCidr)

	return err
}

func setDynClient() error {
	scheme := runtime.NewScheme()
	scheme.AddKnownTypeWithName(schema.GroupVersionKind{
		Group:   "net.liqo.io",
		Version: "v1alpha1",
		Kind:    "ipamstorages",
	}, &liqonetapi.IpamStorage{})
	scheme.AddKnownTypeWithName(schema.GroupVersionKind{
		Group:   "net.liqo.io",
		Version: "v1alpha1",
		Kind:    "natmappings",
	}, &liqonetapi.NatMapping{})

	var m = make(map[schema.GroupVersionResource]string)

	m[schema.GroupVersionResource{
		Group:    "net.liqo.io",
		Version:  "v1alpha1",
		Resource: "ipamstorages",
	}] = "ipamstoragesList"

	m[schema.GroupVersionResource{
		Group:    "net.liqo.io",
		Version:  "v1alpha1",
		Resource: "natmappings",
	}] = "natmappingsList"

	// Init fake dynamic client with objects in order to avoid errors in InitNatMappings func
	// due to the lack of support of fake.dynamicClient for creation of more than 2 resources of the same Kind.
	nm1, err := natmappinginflater.ForgeNatMapping(clusterID1, remotePodCIDR, localNATExternalCIDR, make(map[string]string))
	if err != nil {
		return err
	}
	nm2, err := natmappinginflater.ForgeNatMapping(clusterID2, remotePodCIDR, localNATExternalCIDR, make(map[string]string))
	if err != nil {
		return err
	}
	// The following loop guarrantees resource have different names.
	for nm2.GetName() == nm1.GetName() {
		nm2, err = natmappinginflater.ForgeNatMapping(clusterID2, remotePodCIDR, localNATExternalCIDR, make(map[string]string))
		if err != nil {
			return err
		}
	}

	dynClient = fake.NewSimpleDynamicClientWithCustomListKinds(scheme, m, nm1, nm2)
	return nil
}

var _ = Describe("Ipam", func() {
	rand.Seed(1)

	BeforeEach(func() {
		ipam = liqonet.NewIPAM()
		err := setDynClient()
		Expect(err).To(BeNil())
		err = ipam.Init(liqonet.Pools, dynClient, 2000+rand.Intn(2000))
		Expect(err).To(BeNil())
	})
	AfterEach(func() {
		ipam.Terminate()
	})

	Describe("AcquireReservedSubnet", func() {
		Context("When the reserved network equals a network pool", func() {
			It("Should successfully reserve the subnet", func() {
				// Reserve network
				err := ipam.AcquireReservedSubnet("10.0.0.0/8")
				Expect(err).To(BeNil())
				// Try to get a cluster network in that pool
				p, _, err := ipam.GetSubnetsPerCluster("10.0.2.0/24", "192.168.0.0/24", clusterID1)
				Expect(err).To(BeNil())
				// p should have been mapped to a new network belonging to a different pool
				Expect(p).ToNot(HavePrefix("10."))
			})
		})
		Context("When the reserved network belongs to a pool", func() {
			It("Should not be possible to acquire the same network for a cluster", func() {
				err := ipam.AcquireReservedSubnet("10.244.0.0/24")
				Expect(err).To(BeNil())
				p, e, err := ipam.GetSubnetsPerCluster("10.244.0.0/24", "192.168.0.0/24", clusterID1)
				Expect(err).To(BeNil())
				Expect(p).ToNot(Equal("10.0.2.0/24"))
				Expect(e).To(Equal("192.168.0.0/24"))
			})
			It("Should not be possible to acquire a larger network that contains it for a cluster", func() {
				err := ipam.AcquireReservedSubnet("10.0.0.0/24")
				Expect(err).To(BeNil())
				p, e, err := ipam.GetSubnetsPerCluster("10.0.0.0/16", "192.168.0.0/24", clusterID1)
				Expect(err).To(BeNil())
				Expect(p).ToNot(Equal("10.0.0.0/16"))
				Expect(e).To(Equal("192.168.0.0/24"))
			})
			It("Should not be possible to acquire a smaller network contained by it for a cluster", func() {
				err := ipam.AcquireReservedSubnet("10.0.2.0/24")
				Expect(err).To(BeNil())
				p, e, err := ipam.GetSubnetsPerCluster("10.0.2.0/25", "192.168.0.0/24", clusterID1)
				Expect(err).To(BeNil())
				Expect(p).ToNot(Equal("10.0.2.0/25"))
				Expect(e).To(Equal("192.168.0.0/24"))
			})
		})
	})

	Describe("GetSubnetsPerCluster", func() {
		Context("When the remote cluster asks for subnets not belonging to any pool", func() {
			Context("and the subnets have not already been assigned to any other cluster", func() {
				It("Should allocate the subnets without mapping", func() {
					p, e, err := ipam.GetSubnetsPerCluster("11.0.0.0/16", "11.1.0.0/16", clusterID1)
					Expect(err).To(BeNil())
					Expect(p).To(Equal("11.0.0.0/16"))
					Expect(e).To(Equal("11.1.0.0/16"))
				})
			})
			Context("and the subnets have already been assigned to another cluster", func() {
				Context("and there are available networks with the same mask length in one pool", func() {
					It("should map the requested networks", func() {
						p, e, err := ipam.GetSubnetsPerCluster("11.0.0.0/16", "11.1.0.0/16", clusterID1)
						Expect(err).To(BeNil())
						Expect(p).To(Equal("11.0.0.0/16"))
						Expect(e).To(Equal("11.1.0.0/16"))
						p, e, err = ipam.GetSubnetsPerCluster("11.0.0.0/16", "11.1.0.0/16", clusterID2)
						Expect(err).To(BeNil())
						Expect(p).ToNot(HavePrefix("11."))
						Expect(p).To(HaveSuffix("/16"))
						Expect(e).ToNot(HavePrefix("11."))
						Expect(e).To(HaveSuffix("/16"))
					})
				})
			})
		})
		Context("When the remote cluster asks for a subnet which is equal to a pool", func() {
			Context("and remaining network pools are not filled", func() {
				It("should map it to another network", func() {
					p, _, err := ipam.GetSubnetsPerCluster("172.16.0.0/12", "10.0.0.0/24", clusterID1)
					Expect(err).To(BeNil())
					Expect(p).ToNot(Equal("172.16.0.0/12"))
				})
			})
			Context("and remaining network pools are filled", func() {
				Context("and one of the 2 halves of the pool cannot be reserved", func() {
					It("should not allocate any network", func() {
						// Fill pool #1
						err := fillNetworkPool(liqonet.Pools[0], ipam)
						Expect(err).To(BeNil())

						// Fill pool #2
						err = fillNetworkPool(liqonet.Pools[1], ipam)
						Expect(err).To(BeNil())

						// Acquire a portion of the network pool
						p, e, err := ipam.GetSubnetsPerCluster("172.16.0.0/24", "172.16.1.0/24", clusterID1)
						Expect(err).To(BeNil())
						Expect(p).To(Equal("172.16.0.0/24"))
						Expect(e).To(Equal("172.16.1.0/24"))

						// Acquire network pool
						_, _, err = ipam.GetSubnetsPerCluster("172.16.0.0/12", "10.0.0.0/24", clusterID2)
						Expect(err).ToNot(BeNil())
					})
				})
			})
		})
		Context("When the remote cluster asks for a subnet belonging to a network in the pool", func() {
			Context("and all pools are full", func() {
				It("should not allocate the network (externalCidr not available: podCidr requested should be available after the call)", func() {
					// Fill pool #2
					err := fillNetworkPool(liqonet.Pools[1], ipam)
					Expect(err).To(BeNil())

					// Fill pool #3
					err = fillNetworkPool(liqonet.Pools[2], ipam)
					Expect(err).To(BeNil())

					// Fill 1st half of pool #1
					err = ipam.AcquireReservedSubnet("10.0.0.0/9")
					Expect(err).To(BeNil())

					// Cluster network request
					_, _, err = ipam.GetSubnetsPerCluster("10.128.0.0/9", "192.168.1.0/24", clusterID1)
					Expect(err).ToNot(BeNil())

					// Check if requested podCidr is available
					err = ipam.AcquireReservedSubnet("10.128.0.0/9")
					Expect(err).To(BeNil())
				})
				It("should not allocate the network (both)", func() {
					// Fill pool #1
					err := fillNetworkPool(liqonet.Pools[0], ipam)
					Expect(err).To(BeNil())

					// Fill pool #2
					err = fillNetworkPool(liqonet.Pools[1], ipam)
					Expect(err).To(BeNil())

					// Fill pool #3
					err = fillNetworkPool(liqonet.Pools[2], ipam)
					Expect(err).To(BeNil())

					// Cluster network request
					_, _, err = ipam.GetSubnetsPerCluster("10.1.0.0/16", "10.0.0.0/24", clusterID1)
					Expect(err).ToNot(BeNil())
				})
			})
			Context("and the subnet has not already been assigned to any other cluster", func() {
				It("Should allocate the subnet itself, without mapping", func() {
					p, e, err := ipam.GetSubnetsPerCluster("10.0.0.0/16", "10.1.0.0/16", clusterID1)
					Expect(err).To(BeNil())
					Expect(p).To(Equal("10.0.0.0/16"))
					Expect(e).To(Equal("10.1.0.0/16"))
				})
			})
			Context("and the subnet has already been assigned to another cluster", func() {
				Context("and there is an available network with the same mask length in one pool", func() {
					It("should map the requested network to another network taken by the pool", func() {
						p, e, err := ipam.GetSubnetsPerCluster("10.0.0.0/16", "10.1.0.0/16", clusterID1)
						Expect(err).To(BeNil())
						Expect(p).To(Equal("10.0.0.0/16"))
						Expect(e).To(Equal("10.1.0.0/16"))
						p, e, err = ipam.GetSubnetsPerCluster("10.0.0.0/16", "10.1.0.0/16", clusterID2)
						Expect(err).To(BeNil())
						Expect(p).ToNot(Equal("10.0.0.0/16"))
						Expect(e).ToNot(Equal("10.1.0.0/16"))
					})
				})
				Context("and there is not an available network with the same mask length in any pool", func() {
					It("should fail to allocate the network", func() {

						p, _, err := ipam.GetSubnetsPerCluster("10.0.0.0/9", "10.1.0.0/16", clusterID1)
						Expect(err).To(BeNil())
						Expect(p).To(Equal("10.0.0.0/9"))

						_, _, err = ipam.GetSubnetsPerCluster("10.0.0.0/9", "10.3.0.0/16", clusterID2)
						Expect(err).ToNot(BeNil())
					})
				})
			})
		})
	})
	Describe("RemoveClusterConfig", func() {
		BeforeEach(func() {
			err := ipam.SetPodCIDR(homePodCIDR)
			Expect(err).To(BeNil())
			_, err = ipam.GetExternalCIDR(uint8(24))
			Expect(err).To(BeNil())
		})
		Context("Remove config for a configured cluster", func() {
			It("Should successfully remove the configuration", func() {
				_, _, err := ipam.GetSubnetsPerCluster(remotePodCIDR, remoteExternalCIDR, clusterID1)
				Expect(err).To(BeNil())
				err = ipam.AddLocalSubnetsPerCluster(localNATPodCIDR, localNATExternalCIDR, clusterID1)
				Expect(err).To(BeNil())
				err = ipam.RemoveClusterConfig(clusterID1)
				Expect(err).To(BeNil())

				// Check if config has been removed in IpamStorage resource
				ipamStorage, err := getIpamStorageResource()
				Expect(err).To(BeNil())
				Expect(ipamStorage.Spec.ClusterSubnets).ToNot(HaveKey(clusterID1))

				// Check if network have been freed
				Expect(ipamStorage.Spec.Prefixes).ToNot(HaveKey(remotePodCIDR))
				Expect(ipamStorage.Spec.Prefixes).ToNot(HaveKey(remoteExternalCIDR))

				// Check if NatMapping resource has been deleted
				_, err = getNatMappingResourcePerCluster(clusterID1)
				Expect(k8serrors.IsNotFound(err)).To(BeTrue())
			})
		})
		Context("Call for a non-configured cluster", func() {
			It("Should be a nop", func() {
				// Get config before call
				ipamStorage, err := getIpamStorageResource()
				Expect(err).To(BeNil())
				// In BeforeEach resources for clusterID1 and clusterID2
				// are created. So the following call should
				// return a NotFound
				_, err = getNatMappingResourcePerCluster(clusterID3)
				Expect(k8serrors.IsNotFound(err)).To(BeTrue())

				err = ipam.RemoveClusterConfig(clusterID3)
				Expect(err).To(BeNil())

				// Get config after call
				newIpamStorage, err := getIpamStorageResource()
				Expect(err).To(BeNil())
				_, err = getNatMappingResourcePerCluster(clusterID3)
				Expect(k8serrors.IsNotFound(err)).To(BeTrue())

				Expect(ipamStorage).To(Equal(newIpamStorage))
			})
		})
		Context("Call twice for a configured cluster", func() {
			It("Second call should be a nop", func() {
				_, _, err := ipam.GetSubnetsPerCluster(remotePodCIDR, remoteExternalCIDR, clusterID1)
				Expect(err).To(BeNil())
				err = ipam.AddLocalSubnetsPerCluster(localNATPodCIDR, localNATExternalCIDR, clusterID1)
				Expect(err).To(BeNil())
				err = ipam.RemoveClusterConfig(clusterID1)
				Expect(err).To(BeNil())

				// Get config before second call
				ipamStorage, err := getIpamStorageResource()
				Expect(err).To(BeNil())
				_, err = getNatMappingResourcePerCluster(clusterID3)
				Expect(k8serrors.IsNotFound(err)).To(BeTrue())

				// Second call
				err = ipam.RemoveClusterConfig(clusterID1)
				Expect(err).To(BeNil())

				// Get config after call
				newIpamStorage, err := getIpamStorageResource()
				Expect(err).To(BeNil())
				_, err = getNatMappingResourcePerCluster(clusterID3)
				Expect(k8serrors.IsNotFound(err)).To(BeTrue())

				Expect(ipamStorage).To(Equal(newIpamStorage))
			})
		})
		Context("Passing an empty cluster ID", func() {
			It("Should return a WrongParameter error", func() {
				err := ipam.RemoveClusterConfig("")
				Expect(err).To(MatchError(fmt.Sprintf("%s must be %s", consts.ClusterIDLabelName, liqoneterrors.StringNotEmpty)))
			})
		})
		Context("Remove config when the cluster has an active mapping and"+
			"the endpoint is not reflected in any other cluster", func() {
			It("should delete the endpoint mapping", func() {
				_, _, err := ipam.GetSubnetsPerCluster(remotePodCIDR, remoteExternalCIDR, clusterID1)
				Expect(err).To(BeNil())

				// Remote cluster has not remapped local ExternalCIDR
				err = ipam.AddLocalSubnetsPerCluster(consts.DefaultCIDRValue, consts.DefaultCIDRValue, clusterID1)
				Expect(err).To(BeNil())

				response, err := ipam.MapEndpointIP(context.Background(),
					&liqonet.MapRequest{
						ClusterID: clusterID1,
						Ip:        externalEndpointIP,
					})
				Expect(err).To(BeNil())
				// It should have mapped the IP
				newIP := response.GetIp()
				Expect(newIP).ToNot(Equal(externalEndpointIP))

				// Add mapping to resource NatMapping
				natMappingResource, err := getNatMappingResourcePerCluster(clusterID1)
				Expect(err).To(BeNil())
				natMappingResource.Spec.ClusterMappings = map[string]string{
					externalEndpointIP: newIP,
				}
				err = updateNatMappingResource(natMappingResource)
				Expect(err).To(BeNil())

				// Terminate mappings with active mapping
				err = ipam.RemoveClusterConfig(clusterID1)
				Expect(err).To(BeNil())

				// Check if resource exists
				natMappingResource, err = getNatMappingResourcePerCluster(clusterID1)
				Expect(k8serrors.IsNotFound(err)).To(BeTrue())

				// Check if cluster has been deleted from cluster list of endpoint
				ipamStorage, err := getIpamStorageResource()

				// Since the endpoint had only one mapping, the terminate should have deleted it.
				Expect(ipamStorage.Spec.EndpointMappings).ToNot(HaveKey(externalEndpointIP))
			})
		})
		Context("Remove config when the cluster has an active mapping and"+
			"the endpoint is reflected in more clusters", func() {
			It("should not remove the mapping", func() {
				_, _, err := ipam.GetSubnetsPerCluster(remotePodCIDR, remoteExternalCIDR, clusterID1)
				Expect(err).To(BeNil())
				_, _, err = ipam.GetSubnetsPerCluster(remotePodCIDR, remoteExternalCIDR, clusterID2)
				Expect(err).To(BeNil())

				err = ipam.AddLocalSubnetsPerCluster(consts.DefaultCIDRValue, consts.DefaultCIDRValue, clusterID1)
				Expect(err).To(BeNil())
				err = ipam.AddLocalSubnetsPerCluster(consts.DefaultCIDRValue, consts.DefaultCIDRValue, clusterID2)
				Expect(err).To(BeNil())

				response, err := ipam.MapEndpointIP(context.Background(),
					&liqonet.MapRequest{
						ClusterID: clusterID1,
						Ip:        externalEndpointIP,
					})
				Expect(err).To(BeNil())
				// It should have mapped the IP
				newIPInCluster1 := response.GetIp()
				Expect(newIPInCluster1).ToNot(Equal(externalEndpointIP))

				response, err = ipam.MapEndpointIP(context.Background(),
					&liqonet.MapRequest{
						ClusterID: clusterID2,
						Ip:        externalEndpointIP,
					})
				Expect(err).To(BeNil())
				// It should have mapped the IP
				newIPInCluster2 := response.GetIp()
				Expect(newIPInCluster2).ToNot(Equal(externalEndpointIP))

				// Add mapping to resource NatMapping
				natMappingResource, err := getNatMappingResourcePerCluster(clusterID1)
				Expect(err).To(BeNil())
				natMappingResource.Spec.ClusterMappings = map[string]string{
					externalEndpointIP: newIPInCluster1,
				}
				err = updateNatMappingResource(natMappingResource)
				Expect(err).To(BeNil())

				// Cluster2
				natMappingResource, err = getNatMappingResourcePerCluster(clusterID2)
				Expect(err).To(BeNil())
				natMappingResource.Spec.ClusterMappings = map[string]string{
					externalEndpointIP: newIPInCluster2,
				}
				err = updateNatMappingResource(natMappingResource)
				Expect(err).To(BeNil())

				// Terminate mappings with active mapping
				err = ipam.RemoveClusterConfig(clusterID1)
				Expect(err).To(BeNil())

				// Check if resource exists
				natMappingResource, err = getNatMappingResourcePerCluster(clusterID1)
				Expect(k8serrors.IsNotFound(err)).To(BeTrue())

				// Get IPAM configuration
				ipamStorage, err := getIpamStorageResource()
				Expect(err).To(BeNil())

				// Since the endpoint had more than one mapping, the terminate should not have deleted it.
				Expect(ipamStorage.Spec.EndpointMappings).To(HaveKey(externalEndpointIP))

				// Get endpoint
				endpointMapping := ipamStorage.Spec.EndpointMappings[externalEndpointIP]
				// Check if cluster exists in clusterMappings
				clusterMappings := endpointMapping.ClusterMappings
				Expect(clusterMappings).ToNot(HaveKey(clusterID1))
			})
		})
	})

	Describe("FreeReservedSubnet", func() {
		Context("Freeing a network that has been reserved previously", func() {
			It("Should successfully free the subnet", func() {
				err := ipam.AcquireReservedSubnet("10.0.1.0/24")
				Expect(err).To(BeNil())
				err = ipam.FreeReservedSubnet("10.0.1.0/24")
				Expect(err).To(BeNil())
				err = ipam.AcquireReservedSubnet("10.0.1.0/24")
				Expect(err).To(BeNil())
			})
		})
		Context("Freeing a cluster network that does not exists", func() {
			It("Should return no errors", func() {
				err := ipam.FreeReservedSubnet("10.0.1.0/24")
				Expect(err).To(BeNil())
			})
		})
		Context("Freeing a reserved subnet equal to a network pool", func() {
			It("Should make available the network pool", func() {
				err := ipam.AcquireReservedSubnet("10.0.0.0/8")
				Expect(err).To(BeNil())
				err = ipam.FreeReservedSubnet("10.0.0.0/8")
				Expect(err).To(BeNil())
				p, e, err := ipam.GetSubnetsPerCluster("10.0.0.0/16", "10.2.0.0/16", clusterID1)
				Expect(err).To(BeNil())
				Expect(p).To(Equal("10.0.0.0/16"))
				Expect(e).To(Equal("10.2.0.0/16"))
			})
		})
	})
	Describe("Re-scheduling of network manager", func() {
		It("ipam should retrieve configuration by resource", func() {
			// Assign networks to cluster
			p, e, err := ipam.GetSubnetsPerCluster("10.0.1.0/24", "10.0.2.0/24", clusterID1)
			Expect(err).To(BeNil())
			Expect(p).To(Equal("10.0.1.0/24"))
			Expect(e).To(Equal("10.0.2.0/24"))

			// Simulate re-scheduling
			ipam.Terminate()
			ipam = liqonet.NewIPAM()
			err = ipam.Init(liqonet.Pools, dynClient, 2000+rand.Intn(2000))
			Expect(err).To(BeNil())

			// Another cluster asks for the same networks
			p, e, err = ipam.GetSubnetsPerCluster("10.0.1.0/24", "10.0.2.0/24", clusterID2)
			Expect(err).To(BeNil())
			Expect(p).ToNot(Equal("10.0.1.0/24"))
			Expect(e).ToNot(Equal("10.0.2.0/24"))
		})
	})
	Describe("AddNetworkPool", func() {
		Context("Trying to add a default network pool", func() {
			It("Should generate an error", func() {
				err := ipam.AddNetworkPool("10.0.0.0/8")
				Expect(err).ToNot(BeNil())
			})
		})
		Context("Trying to add twice the same network pool", func() {
			It("Should generate an error", func() {
				err := ipam.AddNetworkPool("11.0.0.0/8")
				Expect(err).To(BeNil())
				err = ipam.AddNetworkPool("11.0.0.0/8")
				Expect(err).ToNot(BeNil())
			})
		})
		Context("After adding a new network pool", func() {
			It("Should be possible to use that pool for cluster networks", func() {
				// Reserve default network pools
				for _, network := range liqonet.Pools {
					err := fillNetworkPool(network, ipam)
					Expect(err).To(BeNil())
				}

				// Add new network pool
				err := ipam.AddNetworkPool("11.0.0.0/8")
				Expect(err).To(BeNil())

				// Reserve a given network
				err = ipam.AcquireReservedSubnet("12.0.0.0/24")
				Expect(err).To(BeNil())

				// Reserve a given network
				err = ipam.AcquireReservedSubnet("12.0.1.0/24")
				Expect(err).To(BeNil())

				// IPAM should use 11.0.0.0/8 to map the cluster network
				p, e, err := ipam.GetSubnetsPerCluster("12.0.0.0/24", "12.0.1.0/24", clusterID1)
				Expect(err).To(BeNil())
				Expect(p).To(HavePrefix("11"))
				Expect(p).To(HaveSuffix("/24"))
				Expect(e).To(HavePrefix("11"))
				Expect(e).To(HaveSuffix("/24"))
			})
		})
		Context("Trying to add a network pool that overlaps with a reserved network", func() {
			It("Should generate an error", func() {
				err := ipam.AcquireReservedSubnet("11.0.0.0/8")
				Expect(err).To(BeNil())
				err = ipam.AddNetworkPool("11.0.0.0/16")
				Expect(err).ToNot(BeNil())
			})
		})
	})
	Describe("RemoveNetworkPool", func() {
		Context("Remove a network pool that does not exist", func() {
			It("Should return an error", func() {
				err := ipam.RemoveNetworkPool("11.0.0.0/8")
				Expect(err).ToNot(BeNil())
			})
		})
		Context("Remove a network pool that exists", func() {
			It("Should successfully remove the network pool", func() {
				// Reserve default network pools
				for _, network := range liqonet.Pools {
					err := ipam.AcquireReservedSubnet(network)
					Expect(err).To(BeNil())
				}

				// Add new network pool
				err := ipam.AddNetworkPool("11.0.0.0/8")
				Expect(err).To(BeNil())

				// Remove network pool
				err = ipam.RemoveNetworkPool("11.0.0.0/8")
				Expect(err).To(BeNil())

				// Reserve a given network
				err = ipam.AcquireReservedSubnet("12.0.0.0/24")
				Expect(err).To(BeNil())

				// Reserve a given network
				err = ipam.AcquireReservedSubnet("12.0.1.0/24")
				Expect(err).To(BeNil())

				// Should fail to assign a network to cluster
				_, _, err = ipam.GetSubnetsPerCluster("12.0.0.0/24", "12.0.1.0/24", clusterID1)
				Expect(err).ToNot(BeNil())
			})
		})
		Context("Remove a network pool that is a default one", func() {
			It("Should generate an error", func() {
				err := ipam.RemoveNetworkPool(liqonet.Pools[0])
				Expect(err).ToNot(BeNil())
			})
		})
		Context("Remove a network pool that is used for a cluster", func() {
			It("Should generate an error", func() {
				// Reserve default network pools
				for _, network := range liqonet.Pools {
					err := ipam.AcquireReservedSubnet(network)
					Expect(err).To(BeNil())
				}

				// Add new network pool
				err := ipam.AddNetworkPool("11.0.0.0/8")
				Expect(err).To(BeNil())

				// Reserve a network
				err = ipam.AcquireReservedSubnet("12.0.0.0/24")
				Expect(err).To(BeNil())

				// Reserve a network
				err = ipam.AcquireReservedSubnet("12.0.1.0/24")
				Expect(err).To(BeNil())

				// IPAM should use 11.0.0.0/8 to map the cluster network
				p, e, err := ipam.GetSubnetsPerCluster("12.0.0.0/24", "12.0.1.0/24", clusterID1)
				Expect(err).To(BeNil())
				Expect(p).To(HavePrefix("11"))
				Expect(p).To(HaveSuffix("/24"))
				Expect(e).To(HavePrefix("11"))
				Expect(e).To(HaveSuffix("/24"))

				err = ipam.RemoveNetworkPool("11.0.0.0/8")
				Expect(err).ToNot(BeNil())
			})
		})
	})

	Describe("AddLocalSubnetsPerCluster", func() {
		var externalCIDR string
		BeforeEach(func() {
			// Set PodCIDR
			err := ipam.SetPodCIDR("10.0.0.0/24")
			Expect(err).To(BeNil())

			// Set ExternalCIDR
			externalCIDR, err = ipam.GetExternalCIDR(24)
			Expect(err).To(BeNil())
			Expect(externalCIDR).To(HaveSuffix("/24"))
		})
		Context("Passing an empty clusterID", func() {
			It("should return a WrongParameter error", func() {
				_, _, err := ipam.GetSubnetsPerCluster(remotePodCIDR, remoteExternalCIDR, clusterID1)
				Expect(err).To(BeNil())
				err = ipam.AddLocalSubnetsPerCluster(localNATPodCIDR, localNATExternalCIDR, "")
				Expect(err).To(MatchError(fmt.Sprintf("%s must be %s", consts.ClusterIDLabelName, liqoneterrors.StringNotEmpty)))
			})
		})
		Context("Call before GetSubnetsPerCluster", func() {
			It("should return an error", func() {
				err := ipam.AddLocalSubnetsPerCluster(localNATPodCIDR, localNATExternalCIDR, clusterID1)
				Expect(err).To(MatchError(fmt.Sprintf("remote subnets for cluster %s do not exist yet. "+
					"Call first GetSubnetsPerCluster", clusterID1)))
			})
		})
		Context("Call function", func() {
			It("should update IpamStorage and create NatMappings resource", func() {
				_, _, err := ipam.GetSubnetsPerCluster(remotePodCIDR, remoteExternalCIDR, clusterID1)
				Expect(err).To(BeNil())
				err = ipam.AddLocalSubnetsPerCluster(localNATPodCIDR, localNATExternalCIDR, clusterID1)
				Expect(err).To(BeNil())

				ipamStorage, err := getIpamStorageResource()
				Expect(err).To(BeNil())

				// Check IpamStorage
				Expect(ipamStorage.Spec.ClusterSubnets).To(HaveKey(clusterID1))
				subnets := ipamStorage.Spec.ClusterSubnets[clusterID1]
				Expect(subnets.LocalNATPodCIDR).To(Equal(localNATPodCIDR))
				Expect(subnets.LocalNATExternalCIDR).To(Equal(localNATExternalCIDR))

				natMappings, err := getNatMappingResourcePerCluster(clusterID1)
				Expect(err).To(BeNil())
				Expect(natMappings.Spec.ClusterID).To(Equal(clusterID1))
				Expect(natMappings.Spec.PodCIDR).To(Equal(remotePodCIDR))
				Expect(natMappings.Spec.ExternalCIDR).To(Equal(localNATExternalCIDR))
			})
		})
		Context("Call func twice", func() {
			It("second call should be a nop", func() {
				_, _, err := ipam.GetSubnetsPerCluster(remotePodCIDR, remoteExternalCIDR, clusterID1)
				Expect(err).To(BeNil())
				err = ipam.AddLocalSubnetsPerCluster(localNATPodCIDR, localNATExternalCIDR, clusterID1)
				Expect(err).To(BeNil())

				// Get config before second call
				ipamStorage, err := getIpamStorageResource()
				Expect(err).To(BeNil())
				natMappings, err := getNatMappingResourcePerCluster(clusterID1)
				Expect(err).To(BeNil())

				// Second call
				err = ipam.AddLocalSubnetsPerCluster(localNATPodCIDR, localNATExternalCIDR, clusterID1)
				Expect(err).To(BeNil())

				// Get config after second call
				newIpamStorage, err := getIpamStorageResource()
				Expect(err).To(BeNil())
				newNatMappings, err := getNatMappingResourcePerCluster(clusterID1)
				Expect(err).To(BeNil())

				Expect(ipamStorage).To(Equal(newIpamStorage))
				Expect(natMappings).To(Equal(newNatMappings))
			})
		})
	})

	Describe("GetExternalCIDR", func() {
		Context("Invoking it twice", func() {
			It("should return no errors", func() {
				e, err := ipam.GetExternalCIDR(24)
				Expect(err).To(BeNil())
				Expect(e).To(HaveSuffix("/24"))
				_, err = ipam.GetExternalCIDR(24)
				Expect(err).To(BeNil())
			})
		})
		Context("Using a valid mask length", func() {
			It("should return no errors", func() {
				e, err := ipam.GetExternalCIDR(24)
				Expect(err).To(BeNil())
				Expect(e).To(HaveSuffix("/24"))
			})
		})
		Context("Using an invalid mask length", func() {
			It("should return an error", func() {
				_, err := ipam.GetExternalCIDR(33)
				Expect(err).ToNot(BeNil())
			})
		})
		Context("Call after SetPodCIDR", func() {
			It("should return no errors", func() {
				err := ipam.SetPodCIDR(homePodCIDR)
				Expect(err).To(BeNil())
				externalCIDR, err := ipam.GetExternalCIDR(24)
				Expect(err).To(BeNil())
				Expect(externalCIDR).To(Equal("10.0.1.0/24"))
			})
		})
		Context("Call before SetPodCIDR", func() {
			It("should produce an error in SetPodCIDR", func() {
				externalCIDR, err := ipam.GetExternalCIDR(24)
				Expect(err).To(BeNil())
				Expect(externalCIDR).To(Equal(homePodCIDR))
				// ExternalCIDR has been assigned "10.0.0.0/24", so the network
				// is not available anymore.
				err = ipam.SetPodCIDR(homePodCIDR)
				Expect(err).ToNot(BeNil())
			})
		})
	})

	Describe("SetPodCIDR", func() {
		Context("Invoking func for the first time", func() {
			It("should return no errors", func() {
				err := ipam.SetPodCIDR(homePodCIDR)
				Expect(err).To(BeNil())
			})
		})
		Context("Later invocation with the same PodCIDR", func() {
			It("should return no errors", func() {
				err := ipam.SetPodCIDR(homePodCIDR)
				Expect(err).To(BeNil())
				err = ipam.SetPodCIDR(homePodCIDR)
				Expect(err).To(BeNil())
			})
		})
		Context("Later invocation with a different PodCIDR", func() {
			It("should return no errors", func() {
				err := ipam.SetPodCIDR(homePodCIDR)
				Expect(err).To(BeNil())
				err = ipam.SetPodCIDR("18.0.0.0/24")
				Expect(err).ToNot(BeNil())
			})
		})
		Context("Using a reserved network", func() {
			It("should return an error", func() {
				err := ipam.AcquireReservedSubnet("10.0.1.0/24")
				Expect(err).To(BeNil())
				err = ipam.SetPodCIDR("10.0.1.0/24")
				Expect(err).ToNot(BeNil())
			})
		})
	})
	Describe("SetServiceCIDR", func() {
		Context("Invoking func for the first time", func() {
			It("should return no errors", func() {
				err := ipam.SetServiceCIDR("10.0.0.0/24")
				Expect(err).To(BeNil())
			})
		})
		Context("Later invocation with the same ServiceCIDR", func() {
			It("should return no errors", func() {
				err := ipam.SetServiceCIDR("10.0.0.0/24")
				Expect(err).To(BeNil())
				err = ipam.SetServiceCIDR("10.0.0.0/24")
				Expect(err).To(BeNil())
			})
		})
		Context("Later invocation with a different ServiceCIDR", func() {
			It("should return no errors", func() {
				err := ipam.SetServiceCIDR("10.0.0.0/24")
				Expect(err).To(BeNil())
				err = ipam.SetServiceCIDR("10.0.1.0/24")
				Expect(err).ToNot(BeNil())
			})
		})
		Context("Using a reserved network", func() {
			It("should return an error", func() {
				err := ipam.AcquireReservedSubnet("10.0.1.0/24")
				Expect(err).To(BeNil())
				err = ipam.SetServiceCIDR("10.0.1.0/24")
				Expect(err).ToNot(BeNil())
			})
		})
	})
	Describe("MapEndpointIP", func() {
		Context("If the endpoint IP belongs to local PodCIDR", func() {
			Context("and the remote cluster has not remapped the local PodCIDR", func() {
				It("should return the same IP address", func() {
					// Set PodCIDR
					err := ipam.SetPodCIDR(homePodCIDR)
					Expect(err).To(BeNil())

					// Get ExternalCIDR
					externalCIDR, err := ipam.GetExternalCIDR(24)
					Expect(err).To(BeNil())
					Expect(externalCIDR).To(HaveSuffix("/24"))

					// Assign networks to cluster
					_, _, err = ipam.GetSubnetsPerCluster(remotePodCIDR, remoteExternalCIDR, clusterID1)
					Expect(err).To(BeNil())

					// Set ExternalCIDR
					_, err = ipam.GetExternalCIDR(24)
					Expect(err).To(BeNil())

					_, _, err = ipam.GetSubnetsPerCluster("10.0.1.0/24", "10.0.2.0/24", "cluster1")
					Expect(err).To(BeNil())

					// Remote cluster has not remapped local PodCIDR
					err = ipam.AddLocalSubnetsPerCluster("None", "None", clusterID1)
					Expect(err).To(BeNil())

					response, err := ipam.MapEndpointIP(context.Background(), &liqonet.MapRequest{
						ClusterID: clusterID1,
						Ip:        "10.0.0.1",
					})
					Expect(err).To(BeNil())
					Expect(response.GetIp()).To(Equal("10.0.0.1"))

					// Should not create a mapping in NatMapping resource
					nm, err := getNatMappingResourcePerCluster(clusterID1)
					Expect(nm.Spec.ClusterMappings).ToNot(HaveKey("10.0.0.1"))
				})
			})
			Context("and the remote cluster has remapped the local PodCIDR", func() {
				It("should map the endpoint IP using the remapped PodCIDR", func() {
					// Set PodCIDR
					err := ipam.SetPodCIDR(homePodCIDR)
					Expect(err).To(BeNil())

					// Get ExternalCIDR
					externalCIDR, err := ipam.GetExternalCIDR(24)
					Expect(err).To(BeNil())
					Expect(externalCIDR).To(HaveSuffix("/24"))

					// Assign networks to cluster
					_, _, err = ipam.GetSubnetsPerCluster(remotePodCIDR, remoteExternalCIDR, clusterID1)
					Expect(err).To(BeNil())

					// Set ExternalCIDR
					_, err = ipam.GetExternalCIDR(24)
					Expect(err).To(BeNil())

					_, _, err = ipam.GetSubnetsPerCluster("10.0.1.0/24", "10.0.2.0/24", "cluster1")
					Expect(err).To(BeNil())

					// Remote cluster has remapped local PodCIDR
					err = ipam.AddLocalSubnetsPerCluster("192.168.0.0/24", "None", clusterID1)
					Expect(err).To(BeNil())

					response, err := ipam.MapEndpointIP(context.Background(), &liqonet.MapRequest{
						ClusterID: clusterID1,
						Ip:        "10.0.0.1",
					})
					Expect(err).To(BeNil())
					Expect(response.GetIp()).To(Equal("192.168.0.1"))

					// Should not create a mapping in NatMapping resource
					nm, err := getNatMappingResourcePerCluster(clusterID1)
					Expect(nm.Spec.ClusterMappings).ToNot(HaveKey("192.168.0.1"))
				})
			})
		})
		Context("If the endpoint IP does not belong to local PodCIDR", func() {
			Context("and the remote cluster has not remapped the local ExternalCIDR", func() {
				It("should map the endpoint IP to a new IP belonging to local ExternalCIDR", func() {
					// Set PodCIDR
					err := ipam.SetPodCIDR(homePodCIDR)
					Expect(err).To(BeNil())

					// Get ExternalCIDR
					externalCIDR, err := ipam.GetExternalCIDR(24)
					Expect(err).To(BeNil())
					Expect(externalCIDR).To(HaveSuffix("/24"))

					// Assign networks to cluster
					_, _, err = ipam.GetSubnetsPerCluster(remotePodCIDR, remoteExternalCIDR, clusterID1)
					Expect(err).To(BeNil())

					// Remote cluster has not remapped local ExternalCIDR
					err = ipam.AddLocalSubnetsPerCluster("None", "None", clusterID1)
					Expect(err).To(BeNil())

					response, err := ipam.MapEndpointIP(context.Background(), &liqonet.MapRequest{
						ClusterID: clusterID1,
						Ip:        "20.0.0.1",
					})
					Expect(err).To(BeNil())
					slicedPrefix := strings.SplitN(externalCIDR, ".", 4)
					slicedPrefix = slicedPrefix[:len(slicedPrefix)-1]
					Expect(response.GetIp()).To(HavePrefix(strings.Join(slicedPrefix, ".")))

					// Should create a mapping in NatMapping resource
					nm, err := getNatMappingResourcePerCluster(clusterID1)
					Expect(nm.Spec.ClusterMappings).To(HaveKeyWithValue("20.0.0.1", response.GetIp()))
				})
				It("should return the same IP if more remote clusters ask for the same endpoint", func() {
					// Set PodCIDR
					err := ipam.SetPodCIDR(homePodCIDR)
					Expect(err).To(BeNil())

					// Get ExternalCIDR
					externalCIDR, err := ipam.GetExternalCIDR(24)
					Expect(err).To(BeNil())
					Expect(externalCIDR).To(HaveSuffix("/24"))

					// Assign networks to clusters
					_, _, err = ipam.GetSubnetsPerCluster(remotePodCIDR, remoteExternalCIDR, clusterID1)
					Expect(err).To(BeNil())
					_, _, err = ipam.GetSubnetsPerCluster(remotePodCIDR, remoteExternalCIDR, clusterID2)
					Expect(err).To(BeNil())

					err = ipam.AddLocalSubnetsPerCluster("None", "None", clusterID1)
					Expect(err).To(BeNil())
					err = ipam.AddLocalSubnetsPerCluster("None", "None", clusterID2)
					Expect(err).To(BeNil())

					// Reflection cluster1
					response, err := ipam.MapEndpointIP(context.Background(), &liqonet.MapRequest{
						ClusterID: clusterID1,
						Ip:        "20.0.0.1",
					})
					Expect(err).To(BeNil())
					slicedPrefix := strings.SplitN(externalCIDR, ".", 4)
					slicedPrefix = slicedPrefix[:len(slicedPrefix)-1]
					Expect(response.GetIp()).To(HavePrefix(strings.Join(slicedPrefix, ".")))
					expectedIp := response.GetIp()

					// Reflection cluster2
					response, err = ipam.MapEndpointIP(context.Background(), &liqonet.MapRequest{
						ClusterID: clusterID1,
						Ip:        "20.0.0.1",
					})
					Expect(err).To(BeNil())
					Expect(response.GetIp()).To(Equal(expectedIp))
				})
			})
			Context("and the remote cluster has remapped the local ExternalCIDR", func() {
				It("should map the endpoint IP to a new IP belonging to the remapped ExternalCIDR", func() {
					// Set PodCIDR
					err := ipam.SetPodCIDR(homePodCIDR)
					Expect(err).To(BeNil())

					// Get ExternalCIDR
					externalCIDR, err := ipam.GetExternalCIDR(24)
					Expect(err).To(BeNil())
					Expect(externalCIDR).To(HaveSuffix("/24"))

					// Assign networks to cluster
					_, _, err = ipam.GetSubnetsPerCluster(remotePodCIDR, remoteExternalCIDR, clusterID1)
					Expect(err).To(BeNil())

					_, _, err = ipam.GetSubnetsPerCluster("10.0.1.0/24", "10.0.2.0/24", "cluster1")
					Expect(err).To(BeNil())

					// Remote cluster has remapped local ExternalCIDR
					err = ipam.AddLocalSubnetsPerCluster("None", "192.168.0.0/24", clusterID1)
					Expect(err).To(BeNil())

					response, err := ipam.MapEndpointIP(context.Background(), &liqonet.MapRequest{
						ClusterID: clusterID1,
						Ip:        "20.0.0.1",
					})
					Expect(err).To(BeNil())
					Expect(response.GetIp()).To(HavePrefix("192.168.0."))
				})
			})
			Context("and the ExternalCIDR has not any more available IPs", func() {
				It("should return an error", func() {
					var response *liqonet.MapResponse
					var err error
					// Set PodCIDR
					err = ipam.SetPodCIDR(homePodCIDR)
					Expect(err).To(BeNil())

					// Get ExternalCIDR
					externalCIDR, err := ipam.GetExternalCIDR(24)
					Expect(err).To(BeNil())
					Expect(externalCIDR).To(HaveSuffix("/24"))
					slicedPrefix := strings.SplitN(externalCIDR, ".", 4)
					slicedPrefix = slicedPrefix[:len(slicedPrefix)-1]

					// Assign networks to cluster
					_, _, err = ipam.GetSubnetsPerCluster(remotePodCIDR, remoteExternalCIDR, clusterID1)
					Expect(err).To(BeNil())

					// Remote cluster has not remapped local ExternalCIDR
					err = ipam.AddLocalSubnetsPerCluster("None", "None", clusterID1)
					Expect(err).To(BeNil())

					// Fill up ExternalCIDR
					for i := 0; i < 254; i++ {
						response, err = ipam.MapEndpointIP(context.Background(), &liqonet.MapRequest{
							ClusterID: clusterID1,
							Ip:        fmt.Sprintf("20.0.0.%d", i),
						})
						Expect(err).To(BeNil())
						Expect(response.GetIp()).To(HavePrefix(strings.Join(slicedPrefix, ".")))
					}

					_, err = ipam.MapEndpointIP(context.Background(), &liqonet.MapRequest{
						ClusterID: clusterID1,
						Ip:        "3.100.0.9",
					})
					Expect(err).ToNot(BeNil())
				})
			})
			Context("Using an invalid endpointIP", func() {
				It("should return an error", func() {
					_, err := ipam.MapEndpointIP(context.Background(), &liqonet.MapRequest{
						ClusterID: clusterID1,
						Ip:        "30.0.9",
					})
					Expect(err).ToNot(BeNil())
				})
			})
			Context("If the local PodCIDR is not set", func() {
				It("should return an error", func() {
					_, err := ipam.MapEndpointIP(context.Background(), &liqonet.MapRequest{
						ClusterID: clusterID1,
						Ip:        "30.0.4.9",
					})
					Expect(err.Error()).To(ContainSubstring("cannot parse network"))
				})
			})
			Context("If the remote cluster has not a PodCIDR set", func() {
				It("should return an error", func() {
					// Set PodCIDR
					err := ipam.SetPodCIDR(homePodCIDR)
					Expect(err).To(BeNil())

					_, err = ipam.MapEndpointIP(context.Background(), &liqonet.MapRequest{
						ClusterID: clusterID1,
						Ip:        "10.0.0.9",
					})
					Expect(err.Error()).To(ContainSubstring("remote cluster cluster1 has not a local NAT PodCIDR"))
				})
			})
			Context("If the remote cluster has not an ExternalCIDR set", func() {
				It("should return an error", func() {
					// Set PodCIDR
					err := ipam.SetPodCIDR(homePodCIDR)
					Expect(err).To(BeNil())

					_, err = ipam.MapEndpointIP(context.Background(), &liqonet.MapRequest{
						ClusterID: clusterID1,
						Ip:        "30.0.4.9",
					})
					Expect(err.Error()).To(ContainSubstring("remote cluster cluster1 has not a Local NAT ExternalCIDR"))
				})
			})
		})
	})

	Describe("GetHomePodIP", func() {
		Context("Pass function an invalid IP address", func() {
			It("should return WrongParameter error", func() {
				_, err := ipam.GetHomePodIP(context.Background(),
					&liqonet.GetHomePodIPRequest{
						Ip:        invalidValue,
						ClusterID: clusterID1,
					})
				err = errors.Unwrap(err)
				Expect(err).To(MatchError(fmt.Sprintf("%s must be %s", invalidValue, liqoneterrors.ValidIP)))
			})
		})
		Context("Pass function an empty cluster ID", func() {
			It("should return WrongParameter error", func() {
				_, err := ipam.GetHomePodIP(context.Background(),
					&liqonet.GetHomePodIPRequest{
						Ip:        invalidValue,
						ClusterID: "",
					})
				err = errors.Unwrap(err)
				Expect(err).To(MatchError(fmt.Sprintf("%s must be %s", consts.ClusterIDLabelName, liqoneterrors.StringNotEmpty)))
			})
		})
		Context("Invoking func without subnets init", func() {
			It("should return WrongParameter error", func() {
				_, err := ipam.GetHomePodIP(context.Background(),
					&liqonet.GetHomePodIPRequest{
						Ip:        "10.0.0.1",
						ClusterID: clusterID1,
					})
				err = errors.Unwrap(err)
				Expect(err).To(MatchError(fmt.Sprintf("cluster %s subnets are not set", clusterID1)))
			})
		})
		Context(`When the remote Pod CIDR has not been remapped by home cluster
			and the call refers to a remote Pod`, func() {
			It("should return the same IP", func() {
				ip, err := utils.GetFirstIP(remotePodCIDR)
				Expect(err).To(BeNil())

				// Home cluster has not remapped remote PodCIDR
				mappedPodCIDR, _, err := ipam.GetSubnetsPerCluster(remotePodCIDR, remoteExternalCIDR, clusterID1)
				Expect(err).To(BeNil())
				Expect(mappedPodCIDR).To(Equal(remotePodCIDR))

				response, err := ipam.GetHomePodIP(context.Background(),
					&liqonet.GetHomePodIPRequest{
						Ip:        ip,
						ClusterID: clusterID1,
					})
				Expect(err).To(BeNil())
				Expect(response.GetHomeIP()).To(Equal(ip))
			})
		})
		Context(`When the remote Pod CIDR has been remapped by home cluster
			and the call refers to a remote Pod`, func() {
			It("should return the remapped IP", func() {
				// Original Pod IP
				ip, err := utils.GetFirstIP(remotePodCIDR)
				Expect(err).To(BeNil())

				// Reserve original PodCIDR so that home cluster will remap it
				err = ipam.AcquireReservedSubnet(remotePodCIDR)
				Expect(err).To(BeNil())

				// Home cluster has remapped remote PodCIDR
				mappedPodCIDR, _, err := ipam.GetSubnetsPerCluster(remotePodCIDR, remoteExternalCIDR, clusterID1)
				Expect(err).To(BeNil())
				Expect(mappedPodCIDR).ToNot(Equal(remotePodCIDR))

				response, err := ipam.GetHomePodIP(context.Background(),
					&liqonet.GetHomePodIPRequest{
						Ip:        ip,
						ClusterID: clusterID1,
					})
				Expect(err).To(BeNil())

				// IP should be mapped to remoteNATPodCIDR
				remappedIP, err := utils.MapIPToNetwork(mappedPodCIDR, ip)
				Expect(response.GetHomeIP()).To(Equal(remappedIP))
			})
		})
	})

	Describe("UnmapEndpointIP", func() {
		Context("If there are no more clusters using an endpointIP", func() {
			It("should free the relative IP", func() {
				// Set PodCIDR
				err := ipam.SetPodCIDR(homePodCIDR)
				Expect(err).To(BeNil())

				// Get ExternalCIDR
				externalCIDR, err := ipam.GetExternalCIDR(24)
				Expect(err).To(BeNil())
				Expect(externalCIDR).To(HaveSuffix("/24"))
				slicedPrefix := strings.SplitN(externalCIDR, ".", 4)
				slicedPrefix = slicedPrefix[:len(slicedPrefix)-1]

				// Assign networks to clusters
				_, _, err = ipam.GetSubnetsPerCluster(remotePodCIDR, remoteExternalCIDR, clusterID1)
				Expect(err).To(BeNil())
				_, _, err = ipam.GetSubnetsPerCluster(remotePodCIDR, remoteExternalCIDR, clusterID2)
				Expect(err).To(BeNil())

				err = ipam.AddLocalSubnetsPerCluster("None", "None", clusterID1)
				Expect(err).To(BeNil())
				err = ipam.AddLocalSubnetsPerCluster("None", "None", clusterID2)
				Expect(err).To(BeNil())

				// Reflection in cluster1
				response, err := ipam.MapEndpointIP(context.Background(), &liqonet.MapRequest{
					ClusterID: clusterID1,
					Ip:        "20.0.0.1",
				})
				Expect(err).To(BeNil())
				Expect(response.GetIp()).To(HavePrefix(strings.Join(slicedPrefix, ".")))

				// Reflection in cluster2
				_, err = ipam.MapEndpointIP(context.Background(), &liqonet.MapRequest{
					ClusterID: clusterID2,
					Ip:        "20.0.0.1",
				})
				Expect(err).To(BeNil())

				// Terminate reflection in cluster1
				_, err = ipam.UnmapEndpointIP(context.Background(), &liqonet.UnmapRequest{
					ClusterID: clusterID1,
					Ip:        "20.0.0.1",
				})
				Expect(err).To(BeNil())

				// Terminate reflection in cluster2
				_, err = ipam.UnmapEndpointIP(context.Background(), &liqonet.UnmapRequest{
					ClusterID: clusterID2,
					Ip:        "20.0.0.1",
				})
				Expect(err).To(BeNil())

				// Get Ipam configuration
				ipamConfig, err := getIpamStorageResource()
				Expect(err).To(BeNil())

				// Check if IP is freed
				Expect(ipamConfig.Spec.EndpointMappings).To(HaveLen(0))
			})
		})
		Context("If there are other clusters using an endpointIP", func() {
			It("should not free the relative IP", func() {
				endpointIP := "20.0.0.1"
				// Set PodCIDR
				err := ipam.SetPodCIDR(homePodCIDR)
				Expect(err).To(BeNil())

				// Get ExternalCIDR
				externalCIDR, err := ipam.GetExternalCIDR(24)
				Expect(err).To(BeNil())
				Expect(externalCIDR).To(HaveSuffix("/24"))
				slicedPrefix := strings.SplitN(externalCIDR, ".", 4)
				slicedPrefix = slicedPrefix[:len(slicedPrefix)-1]

				// Assign networks to clusters
				_, _, err = ipam.GetSubnetsPerCluster(remotePodCIDR, remoteExternalCIDR, clusterID1)
				Expect(err).To(BeNil())
				_, _, err = ipam.GetSubnetsPerCluster(remotePodCIDR, remoteExternalCIDR, clusterID2)
				Expect(err).To(BeNil())

				err = ipam.AddLocalSubnetsPerCluster("None", "None", clusterID1)
				Expect(err).To(BeNil())
				err = ipam.AddLocalSubnetsPerCluster("None", "None", clusterID2)
				Expect(err).To(BeNil())

				// Reflection in cluster1
				response, err := ipam.MapEndpointIP(context.Background(), &liqonet.MapRequest{
					ClusterID: clusterID1,
					Ip:        endpointIP,
				})
				Expect(err).To(BeNil())
				Expect(response.GetIp()).To(HavePrefix(strings.Join(slicedPrefix, ".")))
				ip := response.GetIp()

				// Reflection in cluster2
				_, err = ipam.MapEndpointIP(context.Background(), &liqonet.MapRequest{
					ClusterID: clusterID2,
					Ip:        endpointIP,
				})
				Expect(err).To(BeNil())

				// Terminate reflection in cluster2
				_, err = ipam.UnmapEndpointIP(context.Background(), &liqonet.UnmapRequest{
					ClusterID: clusterID2,
					Ip:        endpointIP,
				})
				Expect(err).To(BeNil())

				// Get Ipam configuration
				ipamConfig, err := getIpamStorageResource()
				Expect(err).To(BeNil())

				// Check if IP is not freed
				Expect(ipamConfig.Spec.EndpointMappings).To(HaveLen(1))
				Expect(ipamConfig.Spec.EndpointMappings[endpointIP].IP).To(Equal(ip))
			})
		})
	})
})

func getNatMappingResourcePerCluster(clusterID string) (*liqonetapi.NatMapping, error) {
	nm := &liqonetapi.NatMapping{}
	list, err := dynClient.Resource(liqonetapi.NatMappingGroupResource).List(
		context.Background(),
		v1.ListOptions{
			LabelSelector: fmt.Sprintf("%s=%s,%s=%s",
				consts.NatMappingResourceLabelKey,
				consts.NatMappingResourceLabelValue,
				consts.ClusterIDLabelName,
				clusterID),
		},
	)
	if err != nil {
		return nil, err
	}
	if len(list.Items) == 0 {
		return nil, k8serrors.NewNotFound(liqonetapi.NatMappingGroupResource.GroupResource(), "")
	}
	err = runtime.DefaultUnstructuredConverter.FromUnstructured(list.Items[0].Object, nm)
	if err != nil {
		return nil, err
	}
	return nm, nil
}

func getIpamStorageResource() (*liqonetapi.IpamStorage, error) {
	ipamConfig := &liqonetapi.IpamStorage{}
	list, err := dynClient.Resource(liqonetapi.IpamGroupResource).List(
		context.Background(),
		v1.ListOptions{
			LabelSelector: fmt.Sprintf("%s=%s",
				consts.IpamStorageResourceLabelKey,
				consts.IpamStorageResourceLabelValue),
		},
	)
	if err != nil {
		return nil, err
	}
	if len(list.Items) == 0 {
		return nil, k8serrors.NewNotFound(liqonetapi.IpamGroupResource.GroupResource(), "")
	}
	err = runtime.DefaultUnstructuredConverter.FromUnstructured(list.Items[0].Object, ipamConfig)
	if err != nil {
		return nil, err
	}
	return ipamConfig, nil
}

func updateNatMappingResource(natMapping *liqonetapi.NatMapping) error {
	unstructuredResource, err := runtime.DefaultUnstructuredConverter.ToUnstructured(natMapping)
	if err != nil {
		return err
	}
	_, err = dynClient.Resource(liqonetapi.NatMappingGroupResource).Update(
		context.Background(),
		&unstructured.Unstructured{Object: unstructuredResource},
		v1.UpdateOptions{},
	)
	if err != nil {
		return err
	}
	return nil
}
