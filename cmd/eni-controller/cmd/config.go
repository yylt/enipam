// Copyright 2023 Authors of enipam
// SPDX-License-Identifier: Apache-2.0

package cmd

import (
	"context"
	"fmt"
	"os"
	"strconv"
	"strings"
	"sync/atomic"

	"github.com/spf13/pflag"
	"gopkg.in/yaml.v2"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/kubernetes"
	"k8s.io/klog/v2"
	ctrl "sigs.k8s.io/controller-runtime"

	"github.com/yylt/enipam/pkg/infra"
	"github.com/yylt/enipam/pkg/ippool"
	"github.com/yylt/enipam/pkg/node"
	"github.com/yylt/enipam/pkg/vpc"
)

var controllerContext = new(ControllerContext)

const (
	SpiderPoolType string = "spiderpool"
	CiliumPoolType string = "cilium"
	CalicoPoolType string = "calico"
)

type envConf struct {
	envName          string
	defaultValue     string
	required         bool
	associateStrKey  *string
	associateBoolKey *bool
	associateIntKey  *int
}

// EnvInfo collects the env and relevant agentContext properties.
var envInfo = []envConf{
	{"GIT_COMMIT_VERSION", "", false, &controllerContext.Cfg.CommitVersion, nil, nil},
	{"GIT_COMMIT_TIME", "", false, &controllerContext.Cfg.CommitTime, nil, nil},
	{"VERSION", "", false, &controllerContext.Cfg.AppVersion, nil, nil},
	{"GOLANG_ENV_MAXPROCS", "8", false, nil, nil, &controllerContext.Cfg.GoMaxProcs},

	{"ENI_LOG_LEVEL", "info", true, &controllerContext.Cfg.LogLevel, nil, nil},
	{"ENI_GOPS_LISTEN_PORT", "5724", false, &controllerContext.Cfg.GopsListenPort, nil, nil},
	{"ENI_PYROSCOPE_SERVER_ADDRESS", "", false, &controllerContext.Cfg.PyroscopeAddress, nil, nil},
	{"ENI_ENABLED_METRIC", "false", false, nil, &controllerContext.Cfg.EnableMetric, nil},
	{"ENI_LEADER", "false", false, nil, &controllerContext.Cfg.EnableLease, nil},

	{"ENI_TYPE", infra.OpenstackType, true, &controllerContext.Cfg.InfraType, nil, nil},
	{"ENI_INFRA_TIMEOUT", "60", false, nil, nil, &controllerContext.Cfg.InfraTimeout},

	{"ENI_INFRAMUTEX", "true", false, nil, &controllerContext.Cfg.InfraMutexOnCreate, nil},

	{"ENI_OPENSTACK_AUTH_URL", "", false, &controllerContext.Cfg.Openstack.AuthUrl, nil, nil},
	{"ENI_OPENSTACK_APPLICATION_CREDENTIAL_ID", "", false, &controllerContext.Cfg.Openstack.AuthUrl, nil, nil},
	{"ENI_OPENSTACK_APPLICATION_CREDENTIAL_SECRET", "", false, &controllerContext.Cfg.Openstack.AuthUrl, nil, nil},
	{"ENI_OPENSTACK_DEFAULT_SUBNAT_ID", "", false, &controllerContext.Cfg.Openstack.AuthUrl, nil, nil},
	{"ENI_OPENSTACK_DEFAULT_PROJECT_ID", "", false, &controllerContext.Cfg.Openstack.AuthUrl, nil, nil},
	{"ENI_OPENSTACK_DEFAULT_REGION", "", false, &controllerContext.Cfg.Openstack.AuthUrl, nil, nil},
}

type Config struct {
	CommitVersion string
	CommitTime    string
	AppVersion    string
	GoMaxProcs    int

	// flags
	ConfigPath       string
	GopsListenPort   string
	PyroscopeAddress string

	// configmap
	LogLevel     string `yaml:"logLevel,omitempty"`
	EnableMetric bool   `yaml:"enableMetric,omitempty"`
	EnableLease  bool   `yaml:"enableLease,omitempty"`

	vpc.VpcCfg `yaml:",inline"`

	infra.InfraCfg `yaml:",inline"`
}

type ControllerContext struct {
	Cfg Config

	// InnerCtx is the context that can be used during shutdown.
	// It will be cancelled after receiving an interrupt or termination signal.
	InnerCtx    context.Context
	InnerCancel context.CancelFunc

	// kubernetes Clientset
	ClientSet     *kubernetes.Clientset
	DynamicClient *dynamic.DynamicClient

	// manager
	CRDManager    ctrl.Manager
	IPPoolManager ippool.Manager
	NodeManager   node.Manager
	VpcManager    *vpc.Manager

	// probe
	IsStartupProbe atomic.Bool
}

// BindControllerDaemonFlags bind controller cli daemon flags
func (cc *ControllerContext) BindControllerDaemonFlags(flags *pflag.FlagSet) {
	flags.StringVar(&cc.Cfg.ConfigPath, "config-path", "", "controller configmap file")
	flags.StringVar(&cc.Cfg.GopsListenPort, "gops-port", "5724", "gops listen port")
	flags.StringVar(&cc.Cfg.PyroscopeAddress, "pyroscope-address", "", "pyroscope address")
}

// ParseConfiguration set the env to AgentConfiguration
func ParseConfiguration() error {
	var result string

	for i := range envInfo {
		env, ok := os.LookupEnv(envInfo[i].envName)
		if ok {
			result = strings.TrimSpace(env)
		} else {
			// if no env and required, set it to default value.
			result = envInfo[i].defaultValue
		}
		if len(result) == 0 {
			if envInfo[i].required {
				klog.Exitf(fmt.Sprintf("empty value of %s", envInfo[i].envName))
			} else {
				// if no env and none-required, just use the empty value.
				continue
			}
		}

		if envInfo[i].associateStrKey != nil {
			*(envInfo[i].associateStrKey) = result
		} else if envInfo[i].associateBoolKey != nil {
			b, err := strconv.ParseBool(result)
			if nil != err {
				return fmt.Errorf("error: %s require a bool value, but get %s", envInfo[i].envName, result)
			}
			*(envInfo[i].associateBoolKey) = b
		} else if envInfo[i].associateIntKey != nil {
			intVal, err := strconv.Atoi(result)
			if nil != err {
				return fmt.Errorf("error: %s require a int value, but get %s", envInfo[i].envName, result)
			}
			*(envInfo[i].associateIntKey) = intVal
		} else {
			return fmt.Errorf("error: %s doesn't match any controller context", envInfo[i].envName)
		}
	}

	return nil
}

// verify after retrieve all config
func (cc *ControllerContext) Verify() {
	// loglevel
}

// LoadConfigmap reads configmap data from cli flag config-path
func (cc *ControllerContext) LoadConfigmap() error {
	configmapBytes, err := os.ReadFile(cc.Cfg.ConfigPath)
	if nil != err {
		return fmt.Errorf("failed to read config file %s, error: %v", cc.Cfg.ConfigPath, err)
	}

	err = yaml.Unmarshal(configmapBytes, &cc.Cfg)
	if nil != err {
		return fmt.Errorf("failed to parse configmap, error: %v", err)
	}

	return nil
}
