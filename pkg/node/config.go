package node

import (
	sync "github.com/yylt/enipam/pkg/lock"

	"github.com/emirpasic/gods/sets/hashset"
	"github.com/yylt/enipam/pkg/ippool"
)

const (
	// value is string type
	InstanceAnnotationKey = "node.eni.io/instance"

	FinializerController = "controller.eni.io"
)

type Sninfo struct {
	// infra subnat id
	Id string

	// vpc subnat CRD
	Name string

	// namespace in subnat CRD
	Nemaspace *hashset.Set

	// node in subnat CRD
	Node *hashset.Set

	// Deleted or not
	Deleted bool

	PreAllocated int

	MinAvaliab int
}

func (in *Sninfo) DeepCopy() *Sninfo {
	return &Sninfo{
		Name:      in.Name,
		Nemaspace: hashset.New(in.Nemaspace.Values()...),
		Node:      hashset.New(in.Node.Values()...),
		Id:        in.Id,
		Deleted:   in.Deleted,
	}
}

type NodeInfo struct {
	// nodename
	Name string

	// instance id, add by daemon in every node.
	NodeId string

	// default ip, usually is kubernetes.node.InternelIP
	NodeIp string

	// node had been delete
	deleted bool

	sync.RWMutex

	// subnatname
	subnat *hashset.Set
}

func (ni *NodeInfo) DeepCopy() *NodeInfo {
	return &NodeInfo{
		NodeId: ni.NodeId,
		NodeIp: ni.NodeIp,
		Name:   ni.Name,
	}
}

type AllocatedInfo struct {
	// mainip(ip in node) - cap/used iplist
	PodAllocated map[string]*ippool.Pool

	// nodedefaultip - subnat
	// subnat will work on the node
	NodeAllocated map[string]*hashset.Set

	// nodedefaultip
	NodeRemove map[string]struct{}
}

func MapDeepCopy(src map[string]*hashset.Set, dest map[string]*hashset.Set) {
	if src == nil || dest == nil {
		return
	}
	for k, set := range src {
		dest[k] = hashset.New(set.Values()...)
	}
}
