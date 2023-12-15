package namespace

import (
	"github.com/yylt/enipam/pkg/util"
)

type Manager interface {
	// callback on pool event
	RegistCallback(util.CallbackFn)
}
