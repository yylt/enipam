package vpc

import "github.com/emirpasic/gods/sets/hashset"

const (
	defaultSubnat string = "default"
)

type Sninfo struct {
	// metadata.name
	Name string

	// namespace set
	Namespace *hashset.Set

	// node set
	Node *hashset.Set

	allnode, allnamespace bool
	// is deleted
	Deleted bool

	// subnat id in IaaS.
	Id string

	projectId, vpcId string

	// will not released when little than PreAllocated
	// it is high level
	PreAllocated int

	// min avaliabled ip, it is low level
	MinAvaliab int
}

func (in *Sninfo) DeepCopy() *Sninfo {
	return &Sninfo{
		Name:      in.Name,
		Namespace: hashset.New(in.Namespace.Values()...),
		Node:      hashset.New(in.Node.Values()...),
		Id:        in.Id,
		Deleted:   in.Deleted,
	}
}

type VpcCfg struct {
	// pre allocat number per node per pool
	PreAllocated int `yaml:"preAllocated,omitempty"`

	// min avaliable number per node per pool
	MinAvaliable int `yaml:"minAvaliabled,omitempty"`

	// worker pool, which is for nodemanager and poolmanager
	WorkerNumber int `yaml:"workerNumber,omitempty"`

	DefaultSubnatId  string `yaml:"subneteId,omitempty"`
	DefaultProjectId string `yaml:"projectId,omitempty"`
}

type Event struct {
	// key: subnatid.
	// value: instance id.
	mainAdd map[string]*hashset.Set

	// key: subnatid.
	// value: instance id.
	mainRemove map[string]*hashset.Set

	// key: interface id.
	// value: number.
	podAdd map[string]int

	// key: interface main id.
	// value: interface id.
	podRemove map[string][]string
}

type subnatEvent struct {
	vpcId     string
	projectId string

	subnatId   string
	subnatName string

	// node event
	nodes []*nodeEvent
}

type nodeEvent struct {
	nodeName  string
	nodeId    string
	defaultIp string

	// number will add
	add int

	// value: main id.
	remove *hashset.Set

	// pool event
	pools []*poolEvent
}

type poolEvent struct {
	// name also main id
	mainId string

	// number will add
	add int

	// interface ip
	remove *hashset.Set
}
