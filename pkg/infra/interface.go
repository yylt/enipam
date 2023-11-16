package infra

import (
	sync "github.com/yylt/enipam/pkg/lock"
)

type Client interface {
	// retcode true:
	// 1 had avaliable num for instance id
	// 2 number had attached on instance id
	HadPortOnInstance(id string, num int) bool

	// create port by instance id
	CreateInstancePort(id string, num int) error

	// retcode true:
	// 1 had avaliable num for interface id
	// 2 number had assign on interface id
	HadPortOnInterface(id string, num int) bool

	CreateInterfacePort(id string, num int) error

	// true: had attach num for instance id
	HadAttached(id string, num int) bool

	AttachPort(id string, num int) error

	HadDetach(noid string, ifidlist []string) bool

	DetachPort(noid string, newifidlist []string) error

	// true: had assign number on interface
	HadAssign(id string, num int) bool

	AssignPort(ifid string, num int) error

	// true: had unssign ip list from interface id
	HadUnssign(ifid string, iplist []string) bool

	UnssignPort(ifid string, newiplist []string) error

	// get
	GetInterface(opt FilterOpt) *Interface
	GetInstance(opt FilterOpt) *Instance

	// iter
	// IterInstance(opt FilterOpt, fn func(*Instance) error)
	// IterInterface(opt FilterOpt, fn func(*Interface) error)

	// metadata
	GetMetadata(*Metadata)

	// add instance
	AddInstance(n *Instance) error
}

type Interface struct {
	Id string

	Ip string

	SubnatId string

	Mac string
	// owner id, instanceid or mainPortId
	sync.RWMutex

	// interface ip : ip
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

	// for interface update
	sync.RWMutex

	// mainIfId
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
	Vpc  string
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

type FilterOpt struct {
	Subnet string `json:"subnet-id"`

	// vpc-id alias is network-id
	Vpc string `json:"vpc-id"`

	// project-id alias is tenant-id
	Project string `json:"project-id"`

	// port-id alias is eni-id
	Port string `json:"port-id"`

	// instance-id alias is computer-id
	Instance string `json:"instance-id"`

	// ip alias is instance Ip or interface IP
	Ip string `json:"ip"`
}
