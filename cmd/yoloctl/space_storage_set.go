package main

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"io"

	"github.com/leogr/yoloctl/internal/controller"
	"github.com/leogr/yoloctl/internal/spaceconfig"
	"github.com/leogr/yoloctl/internal/spacelock"
	"github.com/leogr/yoloctl/internal/storagegc"
	"github.com/spf13/cobra"
)

const spaceStorageSetMethod = "space.storage.set"

type storageReserveSet struct {
	Max string `json:"max"`
}

type storageReserveSetResult struct {
	BuildReserve *storageBuildReserve `json:"build_reserve"`
	Warning      string               `json:"warning,omitempty"`
}

var storageSetAcquire = spacelock.Acquire
var storageSetDisk = storagegc.RootDisk

func runControllerStorageSet(ctx context.Context, req controller.Request, out io.Writer) (_ bool, result error) {
	if req.Method != spaceStorageSetMethod {
		return false, nil
	}
	if !currentControllerPrincipal(ctx).Admin() {
		return true, errors.New("space storage settings are restricted to the host controller endpoint")
	}
	if len(req.Args) != 0 || len(req.Flags) != 0 {
		return true, errors.New("space storage set accepts only its typed request")
	}
	var input storageReserveSet
	if err := decodeBoundedJSON(bytes.NewReader(req.Payload), &input, 1024); err != nil {
		return true, err
	}
	reserve, err := storagegc.ParseReserve(&input.Max)
	if err != nil {
		return true, err
	}
	release, err := storageSetAcquire(ctx, req.Space, true)
	if err != nil {
		return true, err
	}
	defer release()
	defer completeBeforeUnlock(ctx, &result, nil)
	cfg, err := spaceconfig.Load(req.Space)
	if err != nil {
		return true, err
	}
	if err := ctx.Err(); err != nil {
		return true, err
	}
	if err := controller.BeginEffects(ctx); err != nil {
		return true, err
	}
	if cfg.Storage == nil {
		cfg.Storage = &spaceconfig.StorageSettings{}
	}
	cfg.Storage.BuildReserveMax = &input.Max
	if err := cfg.Save(); err != nil {
		return true, err
	}
	var response storageReserveSetResult
	if disk, err := storageSetDisk(); err != nil {
		response.Warning = "setting saved; current VM storage measurement unavailable: " + err.Error()
	} else {
		response.BuildReserve = (buildReserveRead{Reserve: reserve}).report(disk)
	}
	return true, json.NewEncoder(out).Encode(response)
}

func newSpaceStorageSetCmd() *cobra.Command {
	var value string
	cmd := &cobra.Command{
		Use:   "set [SPACE]",
		Short: "Set the cap on free space required before image builds",
		Long: `Set storage.build_reserve_max on the running space's native data disk.

Build admission requires the smaller of 20% of the system disk and this cap.
The default cap is 10GiB; none restores uncapped 20%, and 0GiB removes the
free-space admission reserve. Sizes must be whole GiB. Cleanup still starts
below 20%; its errors/cancellation and BuildKit's targets are unchanged.
This setting is not a quota and never stops a process writing inside a box.
It takes effect at the next build/pull preflight; no VM restart is needed.`,
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if operator, err := loadOperatorClient(); err != nil {
				return err
			} else if operator != nil {
				return asUserError(errors.New("box operator cannot change space storage settings"))
			}
			if _, err := storagegc.ParseReserve(&value); err != nil {
				return asUserError(err)
			}
			space, err := resolveSpacePositional(args)
			if err != nil {
				return err
			}
			boot, err := checkControllerSpace(cmd.Context(), space)
			if err != nil {
				return err
			}
			raw, err := json.Marshal(storageReserveSet{Max: value})
			if err != nil {
				return err
			}
			output := boundedBuffer{Limit: 4096}
			req := controller.Request{ID: rand.Text(), Method: spaceStorageSetMethod, Space: space, Dash: -1, Payload: raw}
			if err := callHostController(cmd.Context(), boot, req, controller.ClientIO{Out: &output, Err: cmd.ErrOrStderr()}); err != nil {
				return err
			}
			var response storageReserveSetResult
			if err := decodeBoundedJSON(&output.Buffer, &response, output.Limit); err != nil {
				return err
			}
			if response.Warning != "" {
				_, _ = fmt.Fprintln(cmd.ErrOrStderr(), response.Warning)
			}
			_, err = fmt.Fprintln(cmd.OutOrStdout(), "build reserve: "+describeBuildReserve(response.BuildReserve))
			return err
		},
	}
	cmd.Flags().StringVar(&value, "build-reserve-max", "", "Free-space cap: whole GiB, 0GiB, or none (uncapped 20%)")
	_ = cmd.MarkFlagRequired("build-reserve-max")
	return cmd
}
