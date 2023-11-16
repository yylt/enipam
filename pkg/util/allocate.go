package util

import (
	"sync/atomic"

	sync "github.com/yylt/enipam/pkg/lock"
)

type Allocat struct {
	mu sync.RWMutex

	vals map[uint64]int64

	intv atomic.Int64
}

func NewAllocat() *Allocat {
	return &Allocat{
		vals: make(map[uint64]int64),
		intv: atomic.Int64{},
	}
}

func (a *Allocat) Get(k uint64) int64 {
	a.mu.Lock()
	defer a.mu.Unlock()
	v, ok := a.vals[k]
	if ok {
		return v
	}
	a.vals[k] = a.intv.Add(1)
	return a.vals[k]
}
