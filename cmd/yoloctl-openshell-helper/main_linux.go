// The helper deliberately has no Cobra/host/controller initialization path.
// It is compiled and qualified separately with CGO_ENABLED=0 for both Linux
// guest architectures. Normal CLI builds consume its pinned immutable bytes.
package main

import (
	"fmt"
	"os"

	"github.com/leogr/yoloctl/internal/openshell"
)

func main() {
	if len(os.Args) < 3 || os.Args[1] != openshell.HelperCommand {
		_, _ = fmt.Fprintln(os.Stderr, "OpenShell helper requires its fixed private runtime role")
		os.Exit(1)
	}
	os.Exit(openshell.ProjectedRuntimeMain(os.Args[2:]))
}
