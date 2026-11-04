package vm

import (
	"github.com/leogr/yoloctl/internal/firecracker"
	"github.com/leogr/yoloctl/internal/vmapi"
)

func init() { Register("firecracker", func() vmapi.Driver { return firecracker.Driver{} }) }
