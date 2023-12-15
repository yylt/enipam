package infra

const (
	OpenstackType string = "openstack"
	AwsType       string = "aws"
)

type InfraCfg struct {
	// vpc type, now support openstack.
	InfraType string `yaml:"eniType,omitempty"`

	// read timeout
	InfraTimeout int `yaml:"timeout,omitempty"`

	InfraMutexOnCreate bool `yaml:"mutex,omitempty"`

	Openstack OpenstackCfg `yaml:"openstack,omitempty"`

	Aws AwsCfg `yaml:"aws,omitempty"`
}

type OpenstackCfg struct {
	AuthUrl          string `yaml:"authUrl,omitempty"`
	CredentialId     string `yaml:"credentialId,omitempty"`
	CredentialSecret string `yaml:"credentialSecret,omitempty"`
}

// TODO
type AwsCfg struct {
}
