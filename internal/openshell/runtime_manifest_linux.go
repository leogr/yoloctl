package openshell

import (
	"errors"
	"fmt"
	"io"
	"os"
	"runtime"
	"runtime/debug"
)

// ProjectedRuntimeMain belongs only to the separately built static command.
// The manifest is untrusted input until the selected root record and native
// activation independently bind its bytes, helper, process and container.
func ProjectedRuntimeMain(args []string) int {
	if err := projectedRuntimeEntry(args); err != nil {
		_, _ = fmt.Fprintln(os.Stderr, "OpenShell helper:", err)
		return 1
	}
	return 0
}

func projectedRuntimeEntry(args []string) error {
	if len(args) == 0 || os.Geteuid() != 0 || os.Getegid() != 0 {
		return errors.New("missing root private helper role")
	}
	var launch HelperLaunch
	var err error
	if args[0] == "runtime" {
		if os.Getpid() != 1 {
			return errors.New("runtime helper must be container PID 1")
		}
		launch, err = ParseHelperLaunch(args)
	} else {
		if _, err = runtimeChildFiles(runtimeChildRole(args[0])); err != nil {
			return err
		}
		launch, err = parseHelperFlags(args[1:])
		if err == nil {
			err = verifyRuntimeChildParent(launch)
			if err != nil {
				err = fmt.Errorf("%s child parent verification: %w", args[0], err)
			}
		}
	}
	if err != nil {
		return err
	}
	if launch.Protocol != HelperProtocol {
		return errors.New("helper protocol differs from the compiled contract")
	}
	release, err := readProjectedQualification(launch)
	if err != nil {
		return fmt.Errorf("%s projected qualification: %w", args[0], err)
	}
	return runtimeEntry(args, release)
}

func readProjectedQualification(launch HelperLaunch) (QualifiedRelease, error) {
	dir, err := openRuntimeDirectory(ArtifactProjection)
	if err != nil {
		return QualifiedRelease{}, err
	}
	defer func() { _ = dir.Close() }()
	file, err := runtimeRegularAt(dir, "qualification.json", 0o444, 0, 0, 1<<20)
	if err != nil {
		return QualifiedRelease{}, err
	}
	raw, err := io.ReadAll(io.LimitReader(file, (1<<20)+1))
	err = errors.Join(err, file.Close())
	if err != nil {
		return QualifiedRelease{}, err
	}
	release, err := LoadQualifiedRelease(raw, launch.Qualification)
	if err != nil {
		return QualifiedRelease{}, err
	}
	if err := release.ValidateHelperSource(); err != nil {
		return QualifiedRelease{}, err
	}
	helper, err := release.Helper(runtime.GOARCH)
	if err != nil {
		return QualifiedRelease{}, err
	}
	if launch.HelperSHA256 != helper.BinarySHA256 {
		return QualifiedRelease{}, errors.New("external qualification names a different helper")
	}
	info, ok := debug.ReadBuildInfo()
	if !ok {
		return QualifiedRelease{}, errors.New("helper producer information is unavailable")
	}
	if err := validateStaticHelperBuild(info, release.document.Pins.Helper.GoVersion, runtime.GOARCH); err != nil {
		return QualifiedRelease{}, err
	}
	return release, nil
}
