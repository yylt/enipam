package ippool

import (
	"github.com/emirpasic/gods/sets/hashset"
	"github.com/yylt/enipam/pkg/util"
)

const (
	// value is ifconfig struct
	SubnatAnnotationsKey = "pool.eni.io/subnatname"

	FinializerController = "controller.eni.io"
)

type CallbackFn func(util.Event, *Pool)

type Manager interface {
	// now ippool implement in spiderpool/ciliumippool/calicoippool
	// update pool and insert/delete ip, call by node/vpc controller.
	// create pool if not exist.
	// different pool maybe use different zone.
	UpdatePool(pl *Pool, e util.Event) error

	// get pool from node mainip
	GetPoolByIp(mainip string) *Pool

	// get pool by subnat
	GetPoolBySubnat(name string) []*Pool

	// callback on pool event
	// 1 delete event: true-noderemove done; false noderemove notready
	// 1 update event: TODO
	RegistCallback(CallbackFn)
}

// node - multi pool
// NOTICE. pool must support ip or ip/32
// controller spilit one subnat to multipool
type Pool struct {
	// node name
	NodeName string

	// Deleted setted by nodeController.
	Deleted bool

	// interface ip, it is not nodeip
	MainIp string

	// subnat
	Subnat string

	// namespace list
	Namespace *hashset.Set

	// capacity ip
	CapIp map[string]struct{}

	// used ip
	UsedIp map[string]struct{}
}

// only compare mainip,nodename, subnatns
func (p *Pool) PartEqual(dst *Pool) bool {
	if dst == nil {
		return false
	}
	if dst.MainIp != p.MainIp {
		return false
	}
	if dst.NodeName != p.NodeName {
		return false
	}
	if dst.Subnat != p.Subnat {
		return false
	}

	diffset := p.Namespace.Difference(dst.Namespace)

	return diffset.Empty()
}

func (p *Pool) DeepCopy() *Pool {
	newp := &Pool{
		NodeName:  p.NodeName,
		MainIp:    p.MainIp,
		CapIp:     map[string]struct{}{},
		UsedIp:    map[string]struct{}{},
		Namespace: hashset.New(p.Namespace.Values()...),
		Subnat:    p.Subnat,
	}
	for ip := range p.CapIp {
		newp.CapIp[ip] = struct{}{}
	}
	for ip := range p.UsedIp {
		newp.UsedIp[ip] = struct{}{}
	}
	return newp
}
