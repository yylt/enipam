package infra

import (
	"fmt"
)

type infra struct {
	Client
}

func NewInfra(cfg *InfraCfg) (Client, error) {
	var (
		err error
		cli Client
	)
	if cfg == nil {
		return nil, fmt.Errorf("not found infra config")
	}
	switch cfg.InfraType {
	case OpenstackType:
		cli, err = NewOpenstackClient(cfg)
	default:
		err = fmt.Errorf("not support infra type %s", cfg.InfraType)
	}
	if err != nil {
		return nil, err
	}
	return &infra{
		Client: cli,
	}, nil
}

func GetInstancePortName(o Opt) string {
	if o.InstanceName != "" {
		return fmt.Sprintf("%s%s", VMInterfaceName, o.InstanceName)
	}
	return fmt.Sprintf("%s%s", VMInterfaceName, o.Instance)
}

func GetInterfacePortName(o Opt) string {
	if o.PortName != "" {
		return fmt.Sprintf("%s%s", PodInterfaceName, o.PortName)
	}
	return fmt.Sprintf("%s%s", PodInterfaceName, o.Port)
}
