package vpc

import (
	"context"
	"fmt"

	"github.com/panjf2000/ants/v2"
	"github.com/yylt/enipam/pkg/infra"
	"github.com/yylt/enipam/pkg/node"
	"k8s.io/klog/v2"
)

type infraops struct {
	ctx context.Context

	worker *ants.Pool

	nodes node.Manager

	evch chan *Event

	api infra.Client
}

func NewInfra(ctx context.Context, number int, nodes node.Manager, api infra.Client) (*infraops, error) {
	p, err := ants.NewPool(number, ants.WithDisablePurge(true), ants.WithNonblocking(true))
	if err != nil {
		return nil, err
	}
	ops := &infraops{
		worker: p,
		evch:   make(chan *Event, 128),
		nodes:  nodes,
		api:    api,
		ctx:    ctx,
	}

	go ops.run()
	return ops, nil
}

func (i *infraops) run() {
	for {
		select {
		case <-i.ctx.Done():
			klog.Warningf("receive context exit")
		case v, ok := <-i.evch:
			if ok {
				i.processWork(v)
			} else {
				klog.Warningf("infra event channel closed")
			}
		}
	}

}

func (i *infraops) TriggerEvent(ev *Event) {
	select {
	case i.evch <- ev:
	default:
		klog.Warningf("infra event channel is full")
	}
}

// hanlder create and attach/assign handler, can not concurrent execute.
// 1 check avaliable port for nodeid and nodeifid
// 2 create interface
// 3 attach/assign
func (i *infraops) processWork(ev *Event) {
	if ev == nil {
		return
	}
	var (
		count int32

		err error

		nodeevent = map[*node.NodeInfo]int{}

		tmp = &node.NodeInfo{}
	)
	// create logic, which is used in node and pod
	for nodeid, num := range ev.nodeAdd {
		if num <= 0 {
			continue
		}
		tmp.NodeId = nodeid
		tmp.Name = ""
		i.nodes.GetNode(tmp)
		if tmp.Name == "" {
			continue
		}
		nodeevent[tmp.DeepCopy()] = num
	}
	// 1 node port
	for info, num := range nodeevent {
		if !i.api.HadPortOnInstance(info.NodeId, num) {
			ninfo := info.DeepCopy()
			err = i.worker.Submit(func() {
				err := i.api.CreateInstancePort(ninfo.NodeId, num)
				if err != nil {
					klog.Errorf("create port for node %s failed: %v", ninfo.NodeId, err)
				}
			})
			if err != nil {
				klog.Errorf("submit work on CreateNodePort failed: %v, had count %d", err, count)
				return
			}
			count++
		}
		klog.Infof("submit workCount %d on create node port", count)
	}

	// 2 node attach
	count, err = i.handlerNodeEvent(ev, nodeevent)
	klog.Infof("submit workCount %d on attach/detach node port, msg: %v", count, err)
	if err != nil {
		return
	}

	// 3 port port
	count = 0
	for info, num := range ev.podAdd {
		if !i.api.HadPortOnInterface(info, num) {
			newinfo := info
			newnum := num
			err = i.worker.Submit(func() {
				err := i.api.CreateInterfacePort(newinfo, newnum)
				if err != nil {
					klog.Errorf("create port for interface %s failed: %v", newinfo, err)
				}
			})
			if err != nil {
				klog.Errorf("submit work on CreatePodPort failed: %v, had count %d", err, count)
				return
			}
			count++
		}
		klog.Infof("after create pod port, submit work number  %d", count)
	}

	// 4 port assign
	count, err = i.handlerPodEvent(ev)
	klog.Infof("submit workCount %d on port assign/unssign, msg: %v", count, err)
}

// attach event handler
func (i *infraops) handlerNodeEvent(ev *Event, noev map[*node.NodeInfo]int) (int32, error) {
	var (
		count int32
		err   error
	)
	//  detach node
	for noid, ifidlist := range ev.nodeRemove {
		if !i.api.HadDetach(noid, ifidlist) {
			var newifidlist = make([]string, len(ifidlist))
			copy(newifidlist, ifidlist)
			err = i.worker.Submit(func() {
				err := i.api.DetachPort(noid, newifidlist)
				if err != nil {
					klog.Errorf("detach port for node id %s failed: %v", noid, err)
				}
			})
			if err != nil {
				klog.Errorf("submit detach worker failed %v", err)
				return count, fmt.Errorf("submit detach worker failed %v", err)
			}
			count++
		}
	}
	//  attach node
	for info, num := range noev {
		if !i.api.HadAttached(info.NodeId, num) {
			did := info.NodeId
			err = i.worker.Submit(func() {
				err := i.api.AttachPort(did, num)
				if err != nil {
					klog.Errorf("attach port for node %s failed: %v", did, err)
				}
			})
			if err != nil {
				klog.Errorf("submit attach worker failed %v", err)
				return count, fmt.Errorf("submit attach worker failed %v", err)
			}
			count++
		}
	}
	return count, nil
}

// othen event handler
func (i *infraops) handlerPodEvent(ev *Event) (int32, error) {
	var (
		count int32
		err   error
	)
	// 2. assign pod
	for ifid, num := range ev.podAdd {
		if !i.api.HadAssign(ifid, num) {
			err = i.worker.Submit(func() {
				err := i.api.AssignPort(ifid, num)
				if err != nil {
					klog.Errorf("assign port for ifid %s failed: %v", ifid, err)
				}
			})
			if err != nil {
				klog.Errorf("submit assign port failed %v", err)
				return count, fmt.Errorf("submit assign port failed %v", err)
			}
			count++
		}
	}

	// 3. unassign pod
	for ifid, iplist := range ev.podRemove {
		if !i.api.HadUnssign(ifid, iplist) {
			var newiplist = make([]string, len(iplist))
			copy(newiplist, iplist)

			err = i.worker.Submit(func() {
				err := i.api.UnssignPort(ifid, newiplist)
				if err != nil {
					klog.Errorf("unassign port for ifid %s failed: %v", ifid, err)
				}
			})
			if err != nil {
				klog.Errorf("submit unassign port failed %v", err)
				return count, fmt.Errorf("submit unassign port failed %v", err)
			}
			count++
		}
	}

	return count, nil
}
