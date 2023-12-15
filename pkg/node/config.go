package node

import (
	"github.com/emirpasic/gods/sets/hashset"
	"github.com/yylt/enipam/pkg/util"
)

type Manager interface {
	// iter by name, if name is "", will iter all
	EachNode(fn func(*NodeInfo) error)
	CouldRemove(no string) bool

	// callback on pool event
	RegistCallback(util.CallbackFn)
}

type NodeInfo struct {
	// nodename
	Name string

	// instance id
	NodeId string

	// default ip, usually is kubernetes.node.InternelIP
	NodeIp string

	// deleted
	deleted bool
}

// only id / ip / name copy
func (ni *NodeInfo) DeepCopy() *NodeInfo {
	return &NodeInfo{
		NodeId: ni.NodeId,
		NodeIp: ni.NodeIp,
		Name:   ni.Name,
	}
}

func (ni *NodeInfo) IsDeleted() bool {
	return ni.deleted
}

func MapDeepCopy(src map[string]*hashset.Set, dest map[string]*hashset.Set) {
	if src == nil || dest == nil {
		return
	}
	for k, set := range src {
		dest[k] = hashset.New(set.Values()...)
	}
}
