# chain 对比 ipam

## 背景

- 对比 chain 和 ipam 之前，需要先了解 cni 规则，当前 cni 标准中有如下配置，

```
# netConf
CNIVersion string `json:"cniVersion,omitempty"`

Name         string          `json:"name,omitempty"`
Type         string          `json:"type,omitempty"`
Capabilities map[string]bool `json:"capabilities,omitempty"`
IPAM         IPAM            `json:"ipam,omitempty"`
DNS          DNS             `json:"dns"`

RawPrevResult map[string]interface{} `json:"prevResult,omitempty"`
PrevResult    Result                 `json:"-"`
```

- 在标准配置中，已经定义 ipam ，但并不会强制要求 cni 都实现该字段的解析，事实上只做 ipam 的插件也是有很多，包括有
    - [containernetworking](https://github.com/containernetworking/plugins/tree/main/plugins/ipam): 包括 host-local, static
    - [whereabouts](https://github.com/k8snetworkplumbingwg/whereabouts): 集群级别子网

- 通常 cni conf 是单 cni 配置，如文件 05-flannel.conf, 但也允许使用 conflist 格式，具体格式内容如下

```
# netConfList
CNIVersion string `json:"cniVersion,omitempty"`

Name         string     `json:"name,omitempty"`
DisableCheck bool       `json:"disableCheck,omitempty"`
Plugins      []*NetConf `json:"plugins,omitempty"`
```

- conflist 格式在 containerd 侧会依次执行，在执行下一个 plugin 前会将 result 贴上，并通过 stdin  传入，这里要求 cni 需要识别 preResult 信息并做出动作

## chain
- 对比 calico，cilium 和 terway 项目，当前只有 cilium cni 对上一个cni的结果进行处理，具体代码如下

```
if len(n.NetConf.RawPrevResult) != 0 {
    if chainAction, err := getChainedAction(n, logger); chainAction != nil {
        var (
            res *cniTypesV1.Result
            ctx = chainingapi.PluginContext{
                Logger:  logger,
                Args:    args,
                CniArgs: cniArgs,
                NetConf: n,
            }
        )

        res, err = chainAction.Add(context.TODO(), ctx, c)
        if err != nil {
            logger.WithError(err).Warn("Chained ADD failed")
            return err
        }
        logger.Debugf("Returning result %#v", res)
        return cniTypes.PrintResult(res, n.CNIVersion)
    } else if err != nil {
        logger.WithError(err).Error("Invalid chaining mode")
        return err
    } else {
        // no chained action supplied; this is an error
        logger.Error("CNI PrevResult supplied, but not in chaining mode -- this is invalid, please set chaining-mode in CNI configuration")
        return fmt.Errorf("CNI PrevResult supplied, but not in chaining mode -- this is invalid, please set chaining-mode in CNI configuration")
    }
}
```

- 如上面所见，当 cilium 不是第一个 cni 时，会使用内置 chain 模式的逻辑去处理，跳过后续的 vethpair 和路由准备等过程
- 当前对比 calico 和 terway，在实现上，没有对 preResult 进行处理，因此也无法理解 chain 模式

## ipam
- ipam 作为标准的字段，大部分 cni 会进行处理，当前的 calico，cilium 和 flannel 支持配置 ipam 字段
- 该功能一般是第一个cni插件的功能，主要是准备 地址和其相关内容
- 不同 cni 版本的 ipam 返回的格式不同，下面是 1.0.0 版本的返回内容

```
# Result
CNIVersion string         `json:"cniVersion,omitempty"`
Interfaces []*Interface   `json:"interfaces,omitempty"`
IPs        []*IPConfig    `json:"ips,omitempty"`
Routes     []*types.Route `json:"routes,omitempty"`
DNS        types.DNS      `json:"dns,omitempty"`
```

- 当前网络环境下，有很多不同功能的 cni，常见如下
    - [rdma](https://github.com/k8snetworkplumbingwg/rdma-cni)
    - [ovs](https://github.com/k8snetworkplumbingwg/ovs-cni)
    - [sriov](https://github.com/k8snetworkplumbingwg/sriov-cni)
    - [bond](https://github.com/k8snetworkplumbingwg/bond-cni)
    - [ib-sriov](https://github.com/k8snetworkplumbingwg/ib-sriov-cni)


## 总结
- chian 模式：要求 cni 处理 preResult 数据，同时要求完成 路由，dns和网关配置
- ipam 模式：要求 cni 返回固定的数据格式，不需要额外的操作
- 现在的网络环境复杂且多变，为保证尽量适配多种网络环境，因此该项目只专注于 ipam ，将更多的路由，设备管理交给其他 cni


# 参考
- https://github.com/containerd/containerd
- https://github.com/flannel-io/cni-plugin
- https://github.com/containernetworking/plugins
- https://github.com/containernetworking/cni
- https://github.com/projectcalico/calico
- https://github.com/cilium/cilium 
- https://github.com/aliyunContainerService/terway
- https://github.com/k8snetworkplumbingwg
