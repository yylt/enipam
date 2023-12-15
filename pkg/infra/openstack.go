package infra

import (
	"errors"
	"fmt"

	"net/http"
	"os"
	"time"

	sync "github.com/yylt/enipam/pkg/lock"

	"github.com/emirpasic/gods/sets/hashset"
	"github.com/gophercloud/gophercloud/openstack/compute/v2/extensions/attachinterfaces"
	"github.com/gophercloud/gophercloud/openstack/compute/v2/extensions/availabilityzones"
	"github.com/gophercloud/gophercloud/pagination"
	"github.com/panjf2000/ants/v2"
	"github.com/yylt/enipam/pkg/util"
	"golang.org/x/sync/singleflight"
	"k8s.io/klog/v2"

	"github.com/gophercloud/gophercloud"
	"github.com/gophercloud/gophercloud/openstack"
	"github.com/gophercloud/gophercloud/openstack/networking/v2/ports"
	"github.com/gophercloud/gophercloud/openstack/networking/v2/subnets"
)

const (
	VMInterfaceName  = "cilium-vm-port-"
	PodInterfaceName = "cilium-pod-port-"

	VMDeviceOwner  = "compute:"
	PodDeviceOwner = "network:secondary"
)

type FixedIPOpt struct {
	SubnetID  string `json:"subnet_id,omitempty"`
	IPAddress string `json:"ip_address,omitempty"`
}
type FixedIPOpts []FixedIPOpt

// Client an OpenStack API client
type OpenstackClient struct {
	neutronV2  *gophercloud.ServiceClient
	novaV2     *gophercloud.ServiceClient
	keystoneV3 *gophercloud.ServiceClient

	cfg *InfraCfg

	createPool *ants.Pool

	attPool *ants.Pool

	// single flight
	gp *singleflight.Group
}

// NewOpenstackClient create the OpenstackClient
func NewOpenstackClient(cfg *InfraCfg) (Client, error) {
	mintimeout := 10
	OpenstackClientTimeout := cfg.InfraTimeout
	if OpenstackClientTimeout < mintimeout {
		OpenstackClientTimeout = mintimeout
	}

	provider, err := newProviderClientOrDie(false, OpenstackClientTimeout)
	if err != nil {
		return nil, err
	}
	domainTokenProvider, err := newProviderClientOrDie(true, OpenstackClientTimeout)
	if err != nil {
		return nil, err
	}

	netV2, err := newNetworkV2ClientOrDie(provider)
	if err != nil {
		return nil, err
	}

	computeV2, err := newComputeV2ClientOrDie(provider)
	if err != nil {
		return nil, err
	}

	idenV3, err := newIdentityV3ClientOrDie(domainTokenProvider)
	if err != nil {
		return nil, err
	}
	// TODO could be config
	p1, err := ants.NewPool(50, ants.WithDisablePurge(true), ants.WithNonblocking(true))
	if err != nil {
		return nil, err
	}
	p2, err := ants.NewPool(50, ants.WithDisablePurge(true), ants.WithNonblocking(true))
	if err != nil {
		return nil, err
	}

	c := &OpenstackClient{
		neutronV2:  netV2,
		novaV2:     computeV2,
		keystoneV3: idenV3,
		cfg:        cfg,
		createPool: p1,
		attPool:    p2,
		gp:         &singleflight.Group{},
	}
	return c, c.probe()
}

func fromPorts(p *ports.Port) *Interface {
	info := &Interface{
		Name:     p.Name,
		Id:       p.ID,
		Ip:       p.FixedIPs[0].IPAddress,
		Mac:      p.MACAddress,
		SubnatId: p.FixedIPs[0].SubnetID,
	}
	// only first
	if len(p.FixedIPs) > 0 {
		info.Ip = p.FixedIPs[0].IPAddress
		info.SubnatId = p.FixedIPs[0].SubnetID
	}
	return info
}

func newProviderClientOrDie(domainScope bool, timeout int) (*gophercloud.ProviderClient, error) {
	opt, err := openstack.AuthOptionsFromEnv()
	if err != nil {
		return nil, err
	}
	// with OS_PROJECT_NAME in env, AuthOptionsFromEnv return project scope token
	// which can not list projects, we need a domain scope token here
	if domainScope {
		opt.TenantName = ""
		opt.Scope = &gophercloud.AuthScope{
			DomainName: os.Getenv("OS_DOMAIN_NAME"),
		}
	}
	p, err := openstack.AuthenticatedClient(opt)
	if err != nil {
		return nil, err
	}
	p.HTTPClient = http.Client{
		Transport: http.DefaultTransport,
		Timeout:   time.Second * time.Duration(timeout),
	}
	p.ReauthFunc = func() error {
		newprov, err := openstack.AuthenticatedClient(opt)
		if err != nil {
			return err
		}
		p.CopyTokenFrom(newprov)
		return nil
	}
	return p, nil
}

func newNetworkV2ClientOrDie(p *gophercloud.ProviderClient) (*gophercloud.ServiceClient, error) {
	client, err := openstack.NewNetworkV2(p, gophercloud.EndpointOpts{})
	if err != nil {
		return nil, err
	}
	return client, nil
}

// Create a ComputeV2 service client using the AKSK provider
func newComputeV2ClientOrDie(p *gophercloud.ProviderClient) (*gophercloud.ServiceClient, error) {
	client, err := openstack.NewComputeV2(p, gophercloud.EndpointOpts{})
	if err != nil {
		return nil, err
	}
	return client, nil
}

func newIdentityV3ClientOrDie(p *gophercloud.ProviderClient) (*gophercloud.ServiceClient, error) {
	client, err := openstack.NewIdentityV3(p, gophercloud.EndpointOpts{})
	if err != nil {
		return nil, err
	}
	return client, nil
}

func (oc *OpenstackClient) UpdatePort(o Opt, number int, ev util.Event, wait bool, postfn func(*Interface)) error {
	var (
		wg  sync.StoppableWaitGroup
		err error
	)
	switch ev {
	case util.CreateE:
		fallthrough
	case util.UpdateE:
		if o.Vpc == "" || o.Subnet == "" {
			return fmt.Errorf("must set vpc and subnet id")
		}
		if o.Instance == "" && o.Port == "" {
			return fmt.Errorf("must set one of instance or port id")
		}
		if wait {
			for i := 0; i < number; i++ {
				wg.Add()
				oc.createPool.Submit(func() {
					wg.Done()
					result := oc.createPort(o, i)
					p, err1 := result.Extract()
					if err1 != nil {
						err = errors.Join(err, err1)
					} else {
						if postfn != nil {
							postfn(fromPorts(p))
						}
					}
				})
			}
			wg.Wait()
			return err
		}
		for i := 0; i < number; i++ {
			oc.createPool.Submit(func() {
				oc.createPort(o, i)
			})
		}
		return nil

	case util.DeleteE:
		if wait {
			return oc.deletePort(o.Port)
		}
		oc.createPool.Submit(func() {
			oc.deletePort(o.Port)
		})
		return nil
	}
	return fmt.Errorf("not support event %v", ev)
}

func (oc *OpenstackClient) EachInterface(o Opt, fn func(*Interface) (bool, error)) error {
	if o.Subnet == "" {
		return fmt.Errorf("must set subnat id")
	}
	opt := ports.ListOpts{
		FixedIPs: []ports.FixedIPOpts{
			{
				SubnetID: o.Subnet,
			},
		},
	}
	if o.Project != "" {
		opt.ProjectID = o.Project
	}
	if o.Vpc != "" {
		opt.NetworkID = o.Vpc
	}
	if o.Instance != "" {
		opt.DeviceID = o.Instance
	}
	if o.Port != "" {
		opt.DeviceID = o.Port
	}

	err := ports.List(oc.neutronV2, opt).EachPage(func(page pagination.Page) (bool, error) {
		result, err := ports.ExtractPorts(page)
		if err != nil {
			return false, err
		}
		for _, p := range result {
			// only one ip
			if len(p.FixedIPs) != 1 {
				continue
			}
			fixip := p.FixedIPs[0].IPAddress

			return fn(&Interface{
				Name:     p.Name,
				Id:       p.ID,
				Ip:       fixip,
				Mac:      p.MACAddress,
				SubnatId: p.FixedIPs[0].SubnetID,
			})
		}
		return true, nil
	})
	return err
}

// attach/detach
// in Opt, the Instance, Port and xx must not null
func (oc *OpenstackClient) UpdateInstancePort(o Opt, ev util.Event, wait bool, fn func(*Interface)) error {
	if o.Instance == "" || o.Port == "" {
		return fmt.Errorf("update instance port must set instance and port")
	}
	switch ev {
	case util.CreateE:
		fallthrough
	case util.UpdateE:
		if wait {
			_, err := oc.attachPort(o.Instance, o.Port, "", fn)
			return err
		} else {
			oc.attPool.Submit(func() {
				oc.attachPort(o.Instance, o.Port, "", fn)
			})
			return nil
		}
	case util.DeleteE:
		if wait {
			return oc.detachPort(o.Instance, o.Port)
		} else {
			oc.attPool.Submit(func() {
				oc.detachPort(o.Instance, o.Port)
			})
			return nil
		}
	}
	return fmt.Errorf("not support event %v", ev)
}

// assign/unsign
func (oc *OpenstackClient) UpdateInterfacePort(id string, pair []AddressPair, ev util.Event, wait bool, postfn func(*Interface)) error {
	if id == "" || pair == nil {
		return fmt.Errorf("update interface port must set id and pair")
	}
	var (
		portsp = make([]ports.AddressPair, len(pair))
		err    error
	)
	for i, p := range pair {
		portsp[i] = ports.AddressPair{
			IPAddress:  p.Ip,
			MACAddress: p.Mac,
		}
	}
	switch ev {
	case util.CreateE:
		fallthrough
	case util.UpdateE:
		if wait {
			_, err := oc.assignPort(id, portsp, postfn)
			return err
		} else {
			oc.attPool.Submit(func() {
				oc.assignPort(id, portsp, postfn)
			})
		}
		return err
	case util.DeleteE:
		if wait {
			err = oc.ussignPort(id, portsp)
		} else {
			oc.attPool.Submit(func() {
				oc.ussignPort(id, portsp)
			})
		}
		return err
	}
	return fmt.Errorf("not support event %v", ev)
}

func (c *OpenstackClient) probe() error {
	return nil
}

// get by ip
func (c *OpenstackClient) GetInterface(o Opt) *Interface {
	if o.Port == "" {
		klog.Errorf("get interface, port-id must set")
		return nil
	}

	po, err := ports.Get(c.neutronV2, o.Port).Extract()
	if err != nil {
		klog.Errorf("get interface failed: %v", err)
		return nil
	}
	return fromPorts(po)
}

func (c *OpenstackClient) GetInstance(opt Opt) *Instance {
	var (
		inst = &Instance{
			Id:        opt.Instance,
			DefaultIp: opt.Ip,
			NodeName:  opt.InstanceName,
		}
	)
	if inst.Id == "" && inst.DefaultIp == "" {
		klog.Errorf("get instance, must set ip and id")
		return nil
	}
	err := attachinterfaces.List(c.neutronV2, opt.Instance).EachPage(
		func(page pagination.Page) (bool, error) {
			result, err := attachinterfaces.ExtractInterfaces(page)
			if err != nil {
				klog.Errorf("attachinterface list failed: %v", err)
				return false, err
			}

			for _, main := range result {
				if len(main.FixedIPs) == 0 {
					//TODO list can get fixips or not ?
					klog.Infof("not found fixedips on interface %s", main.PortID)
					continue
				}
				if main.FixedIPs[0].IPAddress == inst.DefaultIp {
					continue
				}
				//Notice. port name not set.
				inst.Interface[main.FixedIPs[0].IPAddress] = &Interface{
					Id:       main.PortID,
					Mac:      main.MACAddr,
					Ip:       main.FixedIPs[0].IPAddress,
					SubnatId: main.FixedIPs[0].SubnetID,
				}
			}

			return true, nil
		},
	)
	if err != nil {
		klog.Errorf("get instance %s failed: %v", inst.NodeName, err)
		return nil
	}
	return inst
}

// describeSubnets lists all subnets
func (c *OpenstackClient) GetSubnat(o Opt) *Subnat {
	if o.Subnet == "" {
		klog.Errorf("get subnat, id must set")
		return nil
	}

	sn, err := subnets.Get(c.neutronV2, o.Subnet).Extract()
	if err != nil {
		klog.Errorf("get subnat failed: %v", err)
		return nil
	}
	return &Subnat{
		Id:    sn.ID,
		Name:  sn.Name,
		Cidr:  sn.CIDR,
		GwIp:  sn.GatewayIP,
		VpcId: sn.NetworkID,
	}
}

// GetAzs retrieves azlist
func (c *OpenstackClient) getDefaultAz(ops Opt) (string, error) {
	allPages, err := availabilityzones.List(c.novaV2).AllPages()
	if err != nil {
		return "", err
	}
	availabilityZoneInfo, err := availabilityzones.ExtractAvailabilityZones(allPages)
	if err != nil {
		return "", err
	}

	for _, zoneInfo := range availabilityZoneInfo {
		if zoneInfo.ZoneName != "internal" {
			//TODO return first
			return zoneInfo.ZoneName, nil
		}
	}
	return "", fmt.Errorf("not found non-internal az")
}

// create neturon port for both CreateNetworkInterface and AssignIpAddress
func (oc *OpenstackClient) createPort(o Opt, n int) ports.CreateResult {
	var (
		copt = ports.CreateOpts{
			NetworkID: o.Vpc,
			FixedIPs: FixedIPOpts{
				{
					SubnetID: o.Subnet,
				},
			},
		}
	)
	if o.Instance != "" {
		copt.DeviceID = o.Instance
		copt.Name = GetInstancePortName(o)
		copt.DeviceOwner = fmt.Sprintf("%s%s", VMDeviceOwner, o.Instance)
	} else {
		copt.DeviceID = o.Port
		copt.Name = GetInterfacePortName(o)
		copt.DeviceOwner = fmt.Sprintf("%s%s", PodDeviceOwner, o.Port)
	}
	key := fmt.Sprintf("%s%d", copt.Name, n)
	result, _, share := oc.gp.Do(key, func() (interface{}, error) {
		return ports.Create(oc.neutronV2, copt), nil
	})
	rs := result.(ports.CreateResult)
	klog.Infof("create port %s, retmsg: %v, shared %v", copt.Name, rs.Err, share)
	return rs
}

func (oc *OpenstackClient) deletePort(id string) error {
	key := fmt.Sprintf("dp-%s", id)
	_, err, share := oc.gp.Do(key, func() (interface{}, error) {
		return nil, ports.Delete(oc.neutronV2, id).ExtractErr()
	})
	klog.Infof("delete port %s, retmsg: %v, shared %v", id, err, share)
	return err
}

// maini and networkid can only set one.
func (oc *OpenstackClient) attachPort(instanceid, mainid, networkid string, postfn func(*Interface)) (*ports.Port, error) {
	if mainid != "" && networkid != "" {
		return nil, fmt.Errorf("only one of mainid or networkid can be set")
	}
	createOpts := attachinterfaces.CreateOpts{}
	var id string
	if mainid != "" {
		createOpts.PortID = mainid
		id = mainid
	} else {
		createOpts.NetworkID = networkid
		id = networkid
	}
	key := fmt.Sprintf("ap-%s", id)
	result, _, share := oc.gp.Do(key, func() (interface{}, error) {
		return attachinterfaces.Create(oc.novaV2, instanceid, createOpts), nil
	})
	rs, err := result.(ports.CreateResult).Extract()
	if postfn != nil {
		postfn(fromPorts(rs))
	}
	klog.Infof("attach port %s, retmsg: %v, shared %v", id, err, share)
	return rs, err
}

func (oc *OpenstackClient) detachPort(instanceid, mainid string) error {
	key := fmt.Sprintf("dp-%s%s", instanceid, mainid)
	_, err, share := oc.gp.Do(key, func() (interface{}, error) {
		return nil, attachinterfaces.Delete(oc.novaV2, instanceid, mainid).ExtractErr()
	})
	klog.Infof("detach port %s for %s, retmsg: %v, shared %v", mainid, instanceid, err, share)
	return err
}

func (oc *OpenstackClient) assignPort(mainid string, pair []ports.AddressPair, postfn func(*Interface)) (*ports.Port, error) {
	var (
		ips = hashset.New()
	)
	for _, p := range pair {
		ips.Add(p.IPAddress)
	}
	opts := ports.UpdateOpts{
		AllowedAddressPairs: &pair,
	}

	key := fmt.Sprintf("asp-%s", mainid)
	ret, err, share := oc.gp.Do(key, func() (interface{}, error) {
		return ports.AddAllowedAddressPair(oc.neutronV2, mainid, opts).Extract()
	})
	retraw := ret.(*ports.Port)
	if postfn != nil {
		postfn(fromPorts(retraw))
	}
	klog.Infof("assign port %s for %s, retmsg: %v, shared %v", ips.String(), mainid, err, share)
	return retraw, err

}

func (oc *OpenstackClient) ussignPort(mainid string, pair []ports.AddressPair) error {
	var (
		ips = hashset.New()
	)
	for _, p := range pair {
		ips.Add(p.IPAddress)
	}
	opts := ports.UpdateOpts{
		AllowedAddressPairs: &pair,
	}

	key := fmt.Sprintf("usp-%s", mainid)
	_, err, share := oc.gp.Do(key, func() (interface{}, error) {
		return nil, ports.RemoveAllowedAddressPair(oc.neutronV2, mainid, opts).Err
	})
	klog.Infof("ussign port %s for %s, retmsg: %v, shared %v", ips.String(), mainid, err, share)
	return err
}
