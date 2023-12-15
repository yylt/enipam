package infra

import (
	"sync"

	"github.com/yylt/enipam/pkg/util"
)

//sync "github.com/yylt/enipam/pkg/lock"

type Client interface {
	// detail
	GetInterface(opt Opt) *Interface
	GetInstance(opt Opt) *Instance

	// subnat
	GetSubnat(opt Opt) *Subnat

	// create/delete.
	// Opt.Instance is not null mean create for deviceowner is Instance
	// Opt.Port is not null mean create for deviceowner is Port
	// it is async when wait is false
	UpdatePort(o Opt, number int, ev util.Event, wait bool, postfn func(*Interface)) error

	// the Interface will not update second param.
	EachInterface(opt Opt, fn func(*Interface) (bool, error)) error

	// attach/detach
	// in Opt, the Instance, Port and xx must not null
	UpdateInstancePort(o Opt, ev util.Event, wait bool, postfn func(*Interface)) error

	// assign/unsign
	UpdateInterfacePort(id string, p []AddressPair, ev util.Event, wait bool, postfn func(*Interface)) error
}

type AddressPair struct {
	Ip, Mac string
}
type Interface struct {
	Name string

	Id string

	Ip string

	SubnatId string

	Mac string

	sync.RWMutex
	// ip : interfaceInfo
	Second map[string]*Interface
}

func (i *Interface) DeepCopy() *Interface {
	return &Interface{
		Id:       i.Id,
		Ip:       i.Ip,
		Mac:      i.Mac,
		SubnatId: i.SubnatId,
	}
}

type Instance struct {
	Id string

	// internalip in k8s
	DefaultIp string

	// owner name, nodename in k8s
	NodeName string

	// interface which exclude default.
	// ip - interface
	Interface map[string]*Interface
}

func (is *Instance) DeepCopy() *Instance {
	newi := &Instance{
		Id:        is.Id,
		DefaultIp: is.DefaultIp,
		NodeName:  is.NodeName,
		Interface: map[string]*Interface{},
	}
	for k, v := range is.Interface {
		newi.Interface[k] = v.DeepCopy()
	}
	return newi
}

type Network struct {
	Id   string
	Name string
}

type Subnat struct {
	Cidr string
	GwIp string
	Id   string
	Name string

	// alias network id
	VpcId string
}

type Metadata struct {
	ProjectId string
	Project   string

	Subnat   string
	SubnatId string

	Cidr string
	// vpc-id alias is network-id
	Vpc   string
	VpcId string

	SecurityGroups []string
	DefaultSgId    string
}

type Opt struct {
	Subnet string `json:"subnet-id"`

	// vpc-id alias is network-id
	Vpc string `json:"vpc-id"`

	// project-id alias is tenant-id
	Project string `json:"project-id"`

	// port-id alias is eni-id
	Port string `json:"port-id"`

	// instance-id alias is computer-id
	Instance string `json:"instance-id"`

	// port name
	PortName string `json:"port-name"`

	// instance-name
	InstanceName string `json:"instance-name"`

	// ip alias is instance Ip or interface IP
	Ip string `json:"ip"`
}
