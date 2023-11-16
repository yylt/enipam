package infra

import (
	"fmt"

	"net/http"
	"os"
	"time"

	sync "github.com/yylt/enipam/pkg/lock"

	"github.com/emirpasic/gods/maps/hashmap"
	"github.com/gophercloud/gophercloud/openstack/compute/v2/extensions/attachinterfaces"
	"github.com/gophercloud/gophercloud/openstack/compute/v2/extensions/availabilityzones"
	"github.com/gophercloud/gophercloud/pagination"
	"github.com/panjf2000/ants/v2"
	"github.com/spaolacci/murmur3"
	"github.com/yylt/enipam/pkg/util"
	"golang.org/x/sync/singleflight"
	"k8s.io/klog/v2"

	"github.com/gophercloud/gophercloud"
	"github.com/gophercloud/gophercloud/openstack"
	"github.com/gophercloud/gophercloud/openstack/networking/v2/networks"
	"github.com/gophercloud/gophercloud/openstack/networking/v2/ports"
	"github.com/gophercloud/gophercloud/openstack/networking/v2/subnets"

	"k8s.io/apimachinery/pkg/util/wait"
)

const (
	ProjectID        = "project_id"
	SecurityGroupIDs = "securitygroup_ids"

	VMInterfaceName  = "cilium-vm-port"
	PodInterfaceName = "cilium-pod-port"

	FreePodInterfaceName      = "cilium-available-port"
	AvailablePoolFakeDeviceID = "cilium-free-port-"

	VMDeviceOwner  = "compute:"
	PodDeviceOwner = "network:secondary"
	CharSet        = "abcdefghijklmnopqrstuvwxyz0123456789"

	FakeAddresses = 100

	MaxCreatePortsInBulk = 100
)

const (
	PortNotFoundErr     = "port not found"
	NoFreePortAvailable = "no more free ports available"
)

var maxAttachRetries = wait.Backoff{
	Duration: 2500 * time.Millisecond,
	Factor:   1,
	Jitter:   0.1,
	Steps:    6,
	Cap:      0,
}

// Client an OpenStack API client
type OpenstackClient struct {
	neutronV2  *gophercloud.ServiceClient
	novaV2     *gophercloud.ServiceClient
	keystoneV3 *gophercloud.ServiceClient

	mu sync.RWMutex

	// instance id
	instance map[string]*Instance

	mqmu sync.RWMutex

	// deviceid: interfaceid list
	multimap map[string]*hashmap.Map

	// sync instance
	triggerInstance *util.Trigger

	// sync port
	triggerPort *util.Trigger

	cfg *InfraCfg

	createPool *ants.Pool

	attPool *ants.Pool

	// single flight
	gp *singleflight.Group

	// allocated
	alloc *util.Allocat

	project map[string]string

	subnat map[string]*Subnat

	vpc map[string]*Network

	meta *Metadata
}

// PortCreateOpts options to create port
type PortCreateOpts struct {
	Name           string
	NetworkID      string
	SubnetID       string
	IPAddress      string
	ProjectID      string
	SecurityGroups *[]string
	DeviceID       string
	DeviceOwner    string
	Tags           string
}

type BulkCreatePortsOpts struct {
	NetworkId    string
	SubnetId     string
	PoolName     string
	CreateCount  int
	AvailableIps int
}

type FixedIPOpt struct {
	SubnetID        string `json:"subnet_id,omitempty"`
	IPAddress       string `json:"ip_address,omitempty"`
	IPAddressSubstr string `json:"ip_address_subdir,omitempty"`
}
type FixedIPOpts []FixedIPOpt

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
	//
	p1, err := ants.NewPool(20, ants.WithDisablePurge(true), ants.WithNonblocking(true))
	if err != nil {
		return nil, err
	}
	p2, err := ants.NewPool(20, ants.WithDisablePurge(true), ants.WithNonblocking(true))
	if err != nil {
		return nil, err
	}
	c := &OpenstackClient{
		neutronV2:  netV2,
		novaV2:     computeV2,
		keystoneV3: idenV3,

		cfg:        cfg,
		instance:   map[string]*Instance{},
		createPool: p1,
		attPool:    p2,
		gp:         &singleflight.Group{},
		alloc:      util.NewAllocat(),
	}
	c.triggerInstance, err = util.NewTrigger(util.Parameters{
		Name: "openstack instance trigger",
		TriggerFunc: func() {
			c.syncInstances()
			c.syncPorts()
		},
		MinInterval: time.Millisecond * 500,
	})
	if err != nil {
		return nil, err
	}

	c.triggerPort, err = util.NewTrigger(util.Parameters{
		Name: "openstack port trigger",
		TriggerFunc: func() {
			c.syncAllPort()
		},
		MinInterval: time.Millisecond * 500,
	})
	if err != nil {
		return nil, err
	}
	return c, nil
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

func (c *OpenstackClient) HadPortOnInstance(id string, num int) bool {
	// create already
	c.mqmu.RLock()
	defer c.mqmu.RUnlock()
	mm, ok := c.multimap[id]
	if ok && len(mm.Keys()) >= num {
		return true
	}
	return c.HadAttached(id, num)
}

// create port by instance id
// should HadPortOnInstance before this
func (c *OpenstackClient) CreateInstancePort(id string, num int) error {
	var (
		count int = num
		start     = 0
	)

	defer c.triggerPort.Trigger()

	c.mqmu.RLock()
	set, ok := c.multimap[id]
	c.mqmu.RUnlock()
	if ok {
		if len(set.Keys()) > num {
			return nil
		}
		start = len(set.Keys())
		// fix create count
		count = num + 1 - len(set.Keys())
	}
	for i := start; i < start+count; i++ {
		suffix := c.alloc.Get(murmur3.Sum64([]byte(id))) + int64(i)
		vmname := fmt.Sprintf(VMInterfaceName+"-%d", suffix)
		c.createPool.Submit(func() {
			opt := PortCreateOpts{
				Name:        fmt.Sprintf(VMInterfaceName+"-%s", id),
				NetworkID:   c.meta.VpcId,
				SubnetID:    c.meta.SubnatId,
				DeviceID:    id,
				DeviceOwner: fmt.Sprintf(VMDeviceOwner+"%s", id),
				ProjectID:   c.meta.ProjectId,
				// TODO add sg?
			}
			_, err, shared := c.gp.Do(vmname, func() (interface{}, error) {
				err := c.createPort(opt)
				return nil, err
			})
			klog.Infof("create vm port msg: %v, shared flight %v", err, shared)
		})
	}
	return nil
}

// retcode true:
// 1 had avaliable num for interface id
// 2 number had assign on interface id
func (c *OpenstackClient) HadPortOnInterface(id string, num int) bool {
	// create already
	c.mqmu.RLock()
	defer c.mqmu.RUnlock()
	mm, ok := c.multimap[id]
	if ok && len(mm.Keys()) >= num {
		return true
	}
	return c.HadAssign(id, num)
}

func (c *OpenstackClient) CreateInterfacePort(mainIfId string, num int) error {
	var (
		count int = num
		start int
	)

	defer c.triggerPort.Trigger()

	c.mqmu.RLock()
	set, ok := c.multimap[mainIfId]
	c.mqmu.RUnlock()
	if ok {
		if len(set.Keys()) > num {
			return nil
		}
		start = len(set.Keys())
		// fix create count
		count = num - len(set.Keys())
	}
	for i := start; i < start+count; i++ {
		suffix := c.alloc.Get(murmur3.Sum64([]byte(mainIfId))) + int64(i)
		name := fmt.Sprintf(PodInterfaceName+"-%d", suffix)
		c.createPool.Submit(func() {
			opt := PortCreateOpts{
				Name:        name,
				NetworkID:   c.meta.VpcId,
				SubnetID:    c.meta.SubnatId,
				DeviceID:    mainIfId,
				DeviceOwner: fmt.Sprintf(PodDeviceOwner),
				ProjectID:   c.meta.ProjectId,
				// TODO add sg?
			}
			// TODO local var can use or not?
			klog.Infof("singlegroup key: %s", name)
			_, err, shared := c.gp.Do(name, func() (interface{}, error) {
				err := c.createPort(opt)
				return nil, err
			})
			klog.Infof("create port msg: %v, shared flight %v", err, shared)
		})
	}
	return nil
}

// true: had attach num for instance id
func (c *OpenstackClient) HadAttached(id string, num int) bool {
	defer c.triggerInstance.Trigger()
	c.mu.RLock()
	defer c.mu.RUnlock()
	mm, ok := c.instance[id]
	if !ok {
		return false
	}
	mm.RLock()
	defer mm.RUnlock()
	return len(mm.Interface) >= num
}

// check ifid not attach to id(node)
func (c *OpenstackClient) HadDetach(id string, ifidlist []string) bool {
	c.mu.RLock()
	defer c.mu.RUnlock()
	v, ok := c.instance[id]

	if !ok {
		return true
	}
	for _, id := range ifidlist {
		_, ok := v.Interface[id]
		if ok {
			return false
		}
	}
	return true
}

// should becall after HadAttachPort
func (c *OpenstackClient) AttachPort(noid string, num int) error {
	var (
		attachedid = make(map[string]struct{})
		idlist     []string
	)
	if c.cfg.InfraMutexOnCreate && c.createPool.Running() != 0 {
		return fmt.Errorf("had work %d on portCreate", c.createPool.Running())
	}
	c.mu.RLock()
	inst, ok := c.instance[noid]
	if !ok {
		c.mu.RUnlock()
		return fmt.Errorf("not sync instance %s", noid)
	}
	for _, info := range inst.Interface {
		attachedid[info.Id] = struct{}{}
	}
	c.mu.RUnlock()

	// calculate which id to attached.
	c.mqmu.RLock()
	list, ok := c.multimap[noid]
	if !ok {
		c.mqmu.RUnlock()
		return fmt.Errorf("should not be here. not found avaliable port for nodeid %s", noid)
	}

	for _, id := range list.Keys() {
		idname := id.(string)
		if _, ok := attachedid[idname]; !ok {
			idlist = append(idlist, idname)
		}
	}
	c.mqmu.RUnlock()

	i := 0
	// do attach
	for _, id := range idlist {
		i++
		if i > num {
			return nil
		}
		suffix := c.alloc.Get(murmur3.Sum64([]byte(id)))
		name := fmt.Sprintf("ap-%d", suffix)
		newid := id
		c.attPool.Submit(func() {
			createOpts := attachinterfaces.CreateOpts{
				PortID: newid,
			}

			// TODO local var can use or not?
			klog.Infof("singlegroup key: %s", name)
			_, err, shared := c.gp.Do(name, func() (interface{}, error) {
				_, err := attachinterfaces.Create(c.novaV2, noid, createOpts).Extract()
				return nil, err
			})
			klog.Infof("attach port msg: %v, shared flight %v", err, shared)
		})
	}
	return nil
}

func (c *OpenstackClient) DetachPort(noid string, newifidlist []string) error {
	var (
		attachedid = make(map[string]struct{})
		idlist     []string
	)
	if c.cfg.InfraMutexOnCreate && c.createPool.Running() != 0 {
		return fmt.Errorf("had work %d on portCreate", c.createPool.Running())
	}
	c.mu.RLock()
	inst, ok := c.instance[noid]
	if !ok {
		c.mu.RUnlock()
		return fmt.Errorf("not sync instance %s", noid)
	}
	for _, info := range inst.Interface {
		attachedid[info.Id] = struct{}{}
	}
	c.mu.RUnlock()

	// calculate which id to attached.
	for _, id := range newifidlist {
		if _, ok := attachedid[id]; ok {
			idlist = append(idlist, id)
		}
	}

	// do detach
	for _, id := range idlist {
		suffix := c.alloc.Get(murmur3.Sum64([]byte(id)))
		name := fmt.Sprintf("dp-%d", suffix)
		newid := id
		c.attPool.Submit(func() {
			// TODO local var can use or not?
			klog.Infof("singlegroup key: %s", name)
			_, err, shared := c.gp.Do(name, func() (interface{}, error) {
				err := attachinterfaces.Delete(c.novaV2, noid, newid).ExtractErr()
				return nil, err
			})
			klog.Infof("detach port msg: %v, shared flight %v", err, shared)
		})
	}
	return nil
}

// true: had assign number on interface
func (c *OpenstackClient) HadAssign(mainIfId string, num int) bool {
	defer c.triggerInstance.Trigger()
	c.mu.RLock()
	defer c.mu.RUnlock()
	for _, inst := range c.instance {
		iflist, ok := inst.Interface[mainIfId]
		if ok {
			iflist.RLock()
			defer iflist.RUnlock()
			if len(iflist.Second) >= num {
				return true
			}
			break
		}
	}
	return false
}

// true: had unssign ip list from interface id
func (c *OpenstackClient) HadUnssign(mainIfId string, iplist []string) bool {
	var (
		assignips = map[string]struct{}{}
	)
	c.mu.RLock()
	defer c.mu.RUnlock()
	for _, inst := range c.instance {
		iflist, ok := inst.Interface[mainIfId]
		if ok {
			iflist.RLock()
			for _, secIf := range iflist.Second {
				assignips[secIf.Ip] = struct{}{}
			}
			iflist.RUnlock()
			break
		}
	}
	for _, ip := range iplist {
		_, ok := assignips[ip]
		if ok {
			return false
		}
	}
	return true
}

func (c *OpenstackClient) AssignPort(mainIfId string, num int) error {
	var (
		assignid = make(map[string]struct{})
		pairs    []ports.AddressPair
	)
	if c.cfg.InfraMutexOnCreate && c.createPool.Running() != 0 {
		return fmt.Errorf("had work %d on portCreate", c.createPool.Running())
	}
	c.mu.RLock()
	defer c.mu.RUnlock()
	for _, inst := range c.instance {
		iflist, ok := inst.Interface[mainIfId]
		if ok {
			iflist.RLock()
			for _, sec := range iflist.Second {
				assignid[sec.Id] = struct{}{}
			}
			iflist.RUnlock()
			break
		}
	}
	// calculate which id to assign.
	c.mqmu.RLock()
	list, ok := c.multimap[mainIfId]

	if !ok {
		c.mqmu.RUnlock()
		return fmt.Errorf("should not be here. not found avaliable port from portid %s", mainIfId)
	}

	for _, rawif := range list.Values() {
		ifinfo := rawif.(*Interface)
		if _, ok := assignid[ifinfo.Id]; !ok {
			pairs = append(pairs, ports.AddressPair{
				MACAddress: ifinfo.Mac,
				IPAddress:  ifinfo.Ip,
			})
		}
	}
	c.mqmu.RUnlock()

	opts := ports.UpdateOpts{
		AllowedAddressPairs: &pairs,
	}
	name := fmt.Sprintf("asp-%s", mainIfId)
	c.attPool.Submit(func() {
		_, err, shared := c.gp.Do(name, func() (interface{}, error) {
			_, err := ports.AddAllowedAddressPair(c.neutronV2, mainIfId, opts).Extract()
			return nil, err
		})
		klog.Infof("singlegroup key: %s", name)
		klog.Infof("assign port msg: %v, shared flight %v", err, shared)
	})
	return nil
}

func (c *OpenstackClient) UnssignPort(mainIfId string, newiplist []string) error {
	var (
		// ip - id
		assignip = make(map[string]*ports.AddressPair)
		pairs    []ports.AddressPair
	)
	if c.cfg.InfraMutexOnCreate && c.createPool.Running() != 0 {
		return fmt.Errorf("had work %d on portCreate", c.createPool.Running())
	}
	c.mu.RLock()
	for _, inst := range c.instance {
		iflist, ok := inst.Interface[mainIfId]
		if ok {
			iflist.RLock()
			for _, sec := range iflist.Second {
				assignip[sec.Ip] = &ports.AddressPair{
					IPAddress:  sec.Ip,
					MACAddress: sec.Mac,
				}
			}
			iflist.RUnlock()
			break
		}
	}
	c.mu.RUnlock()

	for _, ip := range newiplist {
		if v, ok := assignip[ip]; ok {
			pairs = append(pairs, ports.AddressPair{
				MACAddress: v.MACAddress,
				IPAddress:  v.IPAddress,
			})
		}
	}
	opts := ports.UpdateOpts{
		AllowedAddressPairs: &pairs,
	}
	name := fmt.Sprintf("usp-%s", mainIfId)
	c.attPool.Submit(func() {
		_, err, shared := c.gp.Do(name, func() (interface{}, error) {
			_, err := ports.RemoveAllowedAddressPair(c.neutronV2, mainIfId, opts).Extract()
			return nil, err
		})
		klog.Infof("singlegroup key: %s", name)
		klog.Infof("unssign port msg: %v, shared flight %v", err, shared)
	})

	return nil
}

// get by ip
func (c *OpenstackClient) GetInterface(opt FilterOpt) *Interface {
	c.mu.RLock()
	defer c.mu.RUnlock()
	ip := opt.Ip
	if ip == "" {
		return nil
	}
	for _, info := range c.instance {
		for _, mainif := range info.Interface {
			if mainif.Ip == ip {
				return mainif.DeepCopy()
			}
		}
	}
	return nil
}

func (c *OpenstackClient) GetInstance(opt FilterOpt) *Instance {
	c.mu.RLock()
	defer c.mu.RUnlock()

	if opt.Instance == "" && opt.Ip == "" {
		return nil
	}
	if opt.Instance != "" {
		v, ok := c.instance[opt.Instance]
		if ok {
			return v.DeepCopy()
		}
		return nil
	}
	if opt.Ip != "" {
		for _, info := range c.instance {
			if info.DefaultIp == opt.Ip {
				return info.DeepCopy()
			}
		}
	}
	return nil
}

// metadata
func (c *OpenstackClient) GetMetadata(md *Metadata) {
	if md == nil {
		return
	}
	md.SubnatId = c.cfg.Openstack.DefaultSubnatId
	md.ProjectId = c.cfg.Openstack.ProjectId

	return
}

func (c *OpenstackClient) AddInstance(n *Instance) error {
	if n == nil || n.DefaultIp == "" || n.Id == "" || n.NodeName == "" {
		return fmt.Errorf("not define id/ip/name")
	}
	c.mu.RLock()
	_, ok := c.instance[n.Id]
	c.mu.RUnlock()
	if ok {
		return nil
	}

	c.mu.Lock()
	defer c.mu.Unlock()

	c.instance[n.Id] = &Instance{
		Id:        n.Id,
		DefaultIp: n.DefaultIp,
		NodeName:  n.NodeName,
		Interface: map[string]*Interface{},
	}

	return nil
}

func (c *OpenstackClient) updateSecIfOnInstIf(instid, mainif string, ev util.Event, interfaces ...*Interface) {
	for _, ifinfo := range interfaces {
		if ifinfo == nil {
			continue
		}
		c.mu.Lock()
		inst, ok := c.instance[instid]
		if !ok {
			c.mu.Unlock()
			continue
		}
		main, ok := inst.Interface[mainif]
		if !ok {
			continue
		}

		main.Lock()
		switch ev {
		case util.DeleteE:
			delete(main.Second, ifinfo.Id)
		case util.UpdateE:
			_, ok := main.Second[ifinfo.Id]
			if !ok {
				main.Second[ifinfo.Id] = ifinfo.DeepCopy()
			}
		default:
			// nothing
		}
		main.Unlock()

		c.mu.Unlock()
	}
}
func (c *OpenstackClient) updateMainIfOnInst(instid string, ev util.Event, interfaces ...*Interface) {
	for _, ifinfo := range interfaces {
		if ifinfo == nil {
			continue
		}
		c.mu.Lock()
		inst, ok := c.instance[instid]
		if !ok {
			c.mu.Unlock()
			continue
		}
		switch ev {
		case util.DeleteE:
			delete(inst.Interface, ifinfo.Id)
		case util.UpdateE:
			_, ok := inst.Interface[ifinfo.Id]
			if !ok {
				inst.Interface[ifinfo.Id] = ifinfo.DeepCopy()
			}
		default:
			// nothing
		}
		c.mu.Unlock()
	}
}

// ---------------
// for-loop cache, and show detail, it is called one by one
// NOTICE. openstack List api can only get some info not all, only Get api can get all info

// loop instance which add by nodemg
// update instance.interfaces
func (c *OpenstackClient) syncInstances() error {
	var (
		err error
		// instanceid - mainifid
		actual = map[string]*hashmap.Map{}

		ids []string
		// instanceid - mainifid
		exists = map[string]*hashmap.Map{}
	)
	c.mu.RLock()
	// TODO nodemg update should block read.
	for _, inst := range c.instance {
		exists[inst.Id] = hashmap.New()
		inst.RLock()
		for _, mainif := range inst.Interface {
			exists[inst.Id].Put(mainif.Id, mainif.DeepCopy())
		}
		inst.RUnlock()
		ids = append(ids, inst.Id)
	}
	c.mu.RUnlock()

	for _, id := range ids {
		actual[id] = hashmap.New()
		err = attachinterfaces.List(c.neutronV2, id).EachPage(
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
					actual[id].Put(main.PortID, &Interface{
						Id:       main.PortID,
						Mac:      main.MACAddr,
						Ip:       main.FixedIPs[0].IPAddress,
						SubnatId: main.FixedIPs[0].SubnetID,
					})
				}
				return true, nil
			},
		)
		if err != nil {
			return err
		}
	}

	// remove
	for id, existlist := range exists {
		actlist, ok := actual[id]
		if !ok {
			for _, info := range existlist.Values() {
				//TODO list?
				infoi := info.(*Interface)
				c.updateMainIfOnInst(id, util.DeleteE, infoi)
			}
			continue
		}
		for eid, eif := range existlist.Values() {
			_, exist := actlist.Get(eid)
			if !exist {
				eeif := eif.(*Interface)
				c.updateMainIfOnInst(id, util.DeleteE, eeif)
			}
		}
	}
	// add
	for id, actuallist := range actual {
		for _, info := range actuallist.Values() {
			//TODO list?
			infoi := info.(*Interface)
			c.updateMainIfOnInst(id, util.UpdateE, infoi)
		}
	}

	for id, actuallist := range actual {
		nowlist, ok := exists[id]
		if !ok {
			// should not be here.
			continue
		}
		// remove
		for _, nowif := range nowlist.Values() {
			ifinfo := nowif.(*Interface)
			_, ok := actuallist.Get(ifinfo.Id)
			if !ok {
				c.mu.Lock()
				delete(c.instance[id].Interface, ifinfo.Id)
				c.mu.Unlock()
			}
		}
		// add
		for _, nowif := range actuallist.Values() {
			ifinfo := nowif.(*Interface)
			_, ok := nowlist.Get(ifinfo.Id)
			if !ok {
				c.mu.Lock()
				c.instance[id].Interface[ifinfo.Id] = ifinfo.DeepCopy()
				c.mu.Unlock()
			}
		}
	}
	return nil
}

// updat multiqueue which key is deviceOwner
func (c *OpenstackClient) syncPorts() error {
	var (
		// mainif - secondif
		actual = map[string]*hashmap.Map{}

		ids []string

		// mainif - secondif
		exists      = map[string]*hashmap.Map{}
		existIfInst = map[string]string{}
	)

	// cache
	c.mu.RLock()
	for _, inst := range c.instance {
		inst.RLock()
		for _, mainif := range inst.Interface {
			existIfInst[mainif.Id] = inst.Id
			exists[mainif.Id] = hashmap.New()
			mainif.RLock()
			for _, secif := range mainif.Second {
				exists[mainif.Id].Put(secif.Id, secif.DeepCopy())
			}
			ids = append(ids, mainif.Id)
			mainif.RUnlock()
		}
		inst.RUnlock()
	}
	c.mu.RUnlock()

	for _, id := range ids {
		acp, err := ports.Get(c.neutronV2, id).Extract()
		if err != nil {
			return err
		}
		actual[acp.ID] = hashmap.New()
		for _, info := range acp.AllowedAddressPairs {
			ifinfo := c.getIf(id, info.IPAddress)
			if ifinfo == nil {
				//should not here
				continue
			}
			actual[acp.ID].Put(ifinfo.Id, ifinfo.DeepCopy())
		}
	}

	// remove
	for id, existlist := range exists {
		actlist, ok := actual[id]
		if !ok {
			for _, info := range existlist.Values() {
				//TODO list?
				infoi := info.(*Interface)
				c.updateSecIfOnInstIf(existIfInst[id], id, util.DeleteE, infoi)
			}
			continue
		}
		for eid, eif := range existlist.Values() {
			_, exist := actlist.Get(eid)
			if !exist {
				eeif := eif.(*Interface)
				c.updateSecIfOnInstIf(existIfInst[id], id, util.DeleteE, eeif)
			}
		}
	}

	// add
	for id, actuallist := range actual {
		for _, info := range actuallist.Values() {
			//TODO list?
			infoi := info.(*Interface)
			c.updateSecIfOnInstIf(existIfInst[id], id, util.UpdateE, infoi)
		}
	}

	return nil
}

func (c *OpenstackClient) getIf(deviceid, allowip string) *Interface {
	c.mqmu.RLock()
	defer c.mqmu.RUnlock()
	v, ok := c.multimap[deviceid]
	if !ok {
		return nil
	}
	for _, ifinfo := range v.Values() {
		info := ifinfo.(*Interface)
		if info.Ip == allowip {
			return info.DeepCopy()
		}
	}
	return nil
}

func (c *OpenstackClient) syncAllPort() error {
	opt := ports.ListOpts{
		ProjectID: c.cfg.Openstack.ProjectId,
	}
	var (
		// deviceid, include nodeid and mainif id
		exists = map[string]struct{}{}
	)

	// cache
	c.mu.RLock()
	for _, inst := range c.instance {
		exists[inst.Id] = struct{}{}
		inst.RLock()
		for _, mainif := range inst.Interface {
			exists[mainif.Id] = struct{}{}
		}
		inst.RUnlock()
	}
	c.mu.RUnlock()

	err := ports.List(c.neutronV2, opt).EachPage(func(page pagination.Page) (bool, error) {
		result, err := ports.ExtractPorts(page)
		if err != nil {
			return false, err
		}
		for _, p := range result {
			// only one ip
			if len(p.FixedIPs) != 1 {
				continue
			}
			_, ok := exists[p.DeviceID]
			if !ok {
				continue
			}
			fixip := p.FixedIPs[0].IPAddress
			c.mqmu.Lock()
			c.multimap[p.DeviceID].Put(p.ID, &Interface{
				Id:       p.ID,
				Ip:       fixip,
				Mac:      p.MACAddress,
				SubnatId: p.FixedIPs[0].SubnetID,
			})
			c.mqmu.Unlock()
		}
		return true, nil
	})
	return err
}

func (c *OpenstackClient) getNetwork(ops FilterOpt) ([]*Network, error) {
	opts := networks.ListOpts{
		ProjectID: ops.Project,
	}

	pages, err := networks.List(c.neutronV2, opts).AllPages()
	if err != nil {
		return nil, err
	}

	allNetworks, err := networks.ExtractNetworks(pages)
	if err != nil {
		return nil, err
	}
	var (
		vpcs = make([]*Network, len(allNetworks))
	)
	for i, nw := range allNetworks {
		vpcs[i] = &Network{
			Id:   nw.ID,
			Name: nw.Name,
		}

	}
	return vpcs, nil
}

// describeSubnets lists all subnets
func (c *OpenstackClient) getSubnat(ops FilterOpt) ([]*Subnat, error) {
	opts := subnets.ListOpts{
		ProjectID: ops.Project,
	}
	pages, err := subnets.List(c.neutronV2, opts).AllPages()
	if err != nil {
		return nil, err
	}
	allSubnets, err := subnets.ExtractSubnets(pages)
	if err != nil {
		return nil, err
	}

	var (
		sns = make([]*Subnat, len(allSubnets))
	)
	for i, sn := range allSubnets {
		sns[i] = &Subnat{
			Id:   sn.ID,
			Name: sn.Name,
			Cidr: sn.CIDR,
			GwIp: sn.GatewayIP,
			Vpc:  sn.NetworkID,
		}
	}
	return sns, nil
}

// GetAzs retrieves azlist
func (c *OpenstackClient) getDefaultAz(ops FilterOpt) (string, error) {
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
func (c *OpenstackClient) createPort(opt PortCreateOpts) error {

	copts := ports.CreateOpts{
		Name:           opt.Name,
		NetworkID:      opt.NetworkID,
		DeviceOwner:    opt.DeviceOwner,
		DeviceID:       opt.DeviceID,
		ProjectID:      opt.ProjectID,
		SecurityGroups: opt.SecurityGroups, //TODO sgs must set?
	}

	_, err := ports.Create(c.neutronV2, copts).Extract()
	if err != nil {
		return err
	}
	return nil
}

func (c *OpenstackClient) deletePort(id string) error {
	r := ports.Delete(c.neutronV2, id)
	return r.ExtractErr()
}

// // GetSecurityGroups returns all security groups as a SecurityGroupMap
// func (c *OpenstackClient) getSecurityGroups(ctx context.Context) ([]string, error) {
// 	secGroupList, err := c.describeSecurityGroups()
// 	if err != nil {
// 		return nil, err
// 	}

// 	for _, sg := range secGroupList {
// 		id := sg.ID

// 		securityGroup := &types.SecurityGroup{
// 			ID: id,
// 		}

// 		securityGroups[id] = securityGroup
// 	}

// 	return securityGroups, nil
// }
