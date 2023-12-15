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

type Manager interface {
	// update
	UpdatePool(pl *Pool, ev util.Event) error

	// for-each pool
	EachPool(func(*Pool) error)

	// callback on pool event
	RegistCallback(util.CallbackFn)
}

// NOTICE. pool CRD should support ip or ip/32
type Pool struct {
	// node name
	NodeName string

	// interface id, also name
	MainId string

	// subnat cr name
	Subnat string

	// namespace list
	Namespace *hashset.Set

	// capacity ip
	CapIp *hashset.Set

	// used ip
	UsedIp *hashset.Set
}

// compare mainip, nodename, subnat, namespaces
func (p *Pool) PartEqual(dst *Pool) bool {
	if dst == nil {
		return false
	}
	if dst.MainId != p.MainId {
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
		Subnat:    p.Subnat,
		NodeName:  p.NodeName,
		MainId:    p.MainId,
		CapIp:     hashset.New(p.CapIp.Values()...),
		UsedIp:    hashset.New(p.UsedIp.Values()...),
		Namespace: hashset.New(p.Namespace.Values()...),
	}

	return newp
}
