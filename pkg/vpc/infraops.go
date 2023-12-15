package vpc

import (
	"context"
	"strings"
	"sync"

	"github.com/panjf2000/ants/v2"
	"github.com/yylt/enipam/pkg/infra"
	"github.com/yylt/enipam/pkg/node"
	"github.com/yylt/enipam/pkg/util"
	"k8s.io/klog/v2"
)

type infraops struct {
	ctx context.Context

	worker *ants.Pool

	nodes node.Manager

	snevch chan *subnatEvent

	api infra.Client
}

func NewInfra(ctx context.Context, number int, nodes node.Manager, api infra.Client) *infraops {
	ops := &infraops{
		snevch: make(chan *subnatEvent, 128),
		nodes:  nodes,
		api:    api,
		ctx:    ctx,
	}
	for i := 0; i < number; i++ {
		go ops.run()
	}
	return ops
}

func (i *infraops) run() {
	for {
		select {
		case <-i.ctx.Done():
			klog.Warningf("receive context exit")
			return
		case v, ok := <-i.snevch:
			if ok {
				i.processSubnatWork(v)
			} else {
				klog.Warningf("infra event channel closed")
				return
			}
		}
	}
}

func (i *infraops) HandleSubnatEvent(ev *subnatEvent) {
	select {
	case i.snevch <- ev:
	default:
		klog.Warningf("infra event channel is full")
	}
}

func (i *infraops) processSubnatWork(ev *subnatEvent) {
	if ev == nil {
		return
	}
	var (
		// key: nodename
		nodes = map[string]*infra.Instance{}
		// key: id
		ifs = map[string]*infra.Interface{}

		pairs = map[string][]infra.AddressPair{}

		wg sync.WaitGroup
	)
	for _, node := range ev.nodes {
		nodes[node.nodeName] = &infra.Instance{
			Id:        node.nodeId,
			NodeName:  node.nodeName,
			DefaultIp: node.defaultIp,
			Interface: map[string]*infra.Interface{},
		}
		if node.add != 0 {
			instopt := infra.Opt{
				Instance:     node.nodeId,
				InstanceName: node.nodeName,
				Subnet:       ev.subnatId,
				Project:      ev.projectId,
				Vpc:          ev.vpcId,
			}
			wg.Add(1)
			i.api.UpdatePort(instopt, node.add, util.CreateE, false, func(ifinfo *infra.Interface) {
				defer wg.Done()
			})
		}
		for _, po := range node.pools {
			ifs[po.mainId] = &infra.Interface{
				Id:       po.mainId,
				Ip:       node.defaultIp,
				SubnatId: ev.subnatId,
				Second:   map[string]*infra.Interface{},
			}
			if po.add != 0 {
				intopt := infra.Opt{
					Port:    po.mainId,
					Subnet:  ev.subnatId,
					Project: ev.projectId,
					Vpc:     ev.vpcId,
				}
				wg.Add(1)
				i.api.UpdatePort(intopt, po.add, util.CreateE, false, func(ifinfo *infra.Interface) {
					defer wg.Done()
				})
			}
		}
	}
	// wait create done.
	wg.Wait()
	opt := infra.Opt{Project: ev.projectId, Vpc: ev.vpcId, Subnet: ev.subnatId}

	err := i.api.EachInterface(opt, func(ifinfo *infra.Interface) (bool, error) {
		if ifinfo == nil || ifinfo.Ip == "" {
			return true, nil
		}
		// TODO filter by device-id, make sure it is created by our.
		if strings.HasPrefix(ifinfo.Name, infra.VMInterfaceName) {
			name := strings.TrimPrefix(ifinfo.Name, infra.VMInterfaceName)
			no, ok := nodes[name]
			if !ok {
				return true, nil
			}
			old, ok := no.Interface[ifinfo.Id]
			if ok {
				klog.Warningf("subnat %v, found 2 interface (%s, %s) on node %v", ifinfo.SubnatId, old.Name, ifinfo.Name, name)
				return true, nil
			}
			wg.Add(1)
			i.api.UpdateInstancePort(infra.Opt{
				Instance: no.Id,
				Port:     ifinfo.Id,
			}, util.CreateE, false, func(i *infra.Interface) {
				wg.Done()
			})
		}
		if strings.HasPrefix(ifinfo.Name, infra.PodInterfaceName) {
			mainid := strings.TrimPrefix(ifinfo.Name, infra.PodInterfaceName)
			_, ok := ifs[mainid]
			if !ok {
				return true, nil
			}
			pairs[mainid] = append(pairs[mainid], infra.AddressPair{
				Ip:  ifinfo.Ip,
				Mac: ifinfo.Mac,
			})
		}
		return true, nil
	})
	if err != nil {
		klog.Errorf("iter interface failed: %v", err)
		return
	}

	for id, ps := range pairs {
		wg.Add(1)
		i.api.UpdateInterfacePort(id, ps, util.CreateE, false, func(i *infra.Interface) {
			clear(ps)
			wg.Done()
		})
	}
	clear(pairs)

	// remove
	for _, no := range ev.nodes {
		for _, mid := range no.remove.Values() {
			id := mid.(string)
			wg.Add(1)
			i.api.UpdateInstancePort(infra.Opt{
				Instance: no.nodeId,
				Port:     id,
			}, util.DeleteE, false, func(i *infra.Interface) {
				wg.Done()
			})
		}
		for _, po := range no.pools {
			for _, im := range po.remove.Values() {
				pa := im.(infra.AddressPair)
				wg.Add(1)
				pairs[no.nodeId] = append(pairs[no.nodeId], pa)
			}
		}
	}
	for id, ps := range pairs {
		wg.Add(1)
		i.api.UpdateInterfacePort(id, ps, util.DeleteE, false, func(i *infra.Interface) {
			wg.Done()
		})
	}
	wg.Wait()
	return
}

func (i *infraops) processPodWork(ev *subnatEvent) {
	if ev == nil {
		return
	}

}
