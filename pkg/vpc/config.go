package vpc

const (
	defaultSubnat string = "default"
)

type VpcCfg struct {
	// pre allocat number per node per pool
	PreAllocated int `yaml:"preAllocated,omitempty"`

	// min avaliable number per node per pool
	MinAvaliable int `yaml:"minAvaliabled,omitempty"`

	// worker pool, which is for nodemanager and poolmanager
	WorkerNumber int `yaml:"workerNumber,omitempty"`
}

type Event struct {
	// key: node instanceid. instanceid is used by Iaas.
	// value: number
	nodeAdd map[string]int

	// key: interface-id in node, id is used by Iaas.
	// value: number
	podAdd map[string]int

	// key: node instanceid. instanceid is used by Iaas.
	// value: interface-id in node array.
	nodeRemove map[string][]string

	// key: interface-id in node, id is used by Iaas.
	// value: ip list.
	podRemove map[string][]string
}

// type Event struct {
// 	// key: node instanceid. instanceid is used by Iaas.
// 	// value: number
// 	nodeAdd map[string]int

// 	// key: interface-ip in node, ip is used by Iaas.
// 	// value: number
// 	podAdd map[string]int

// 	// key: node instanceid. instanceid is used by Iaas.
// 	// value: interface-ip in the node.
// 	nodeRemove map[string][]string

// 	// key: interface-ip in node, ip is used by Iaas.
// 	// value: ip list.
// 	podRemove map[string][]string
// }
