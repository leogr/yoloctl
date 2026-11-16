package main

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path"
	"strings"
	"time"

	"github.com/leogr/yoloctl/internal/boxexport"
	"github.com/leogr/yoloctl/internal/controller"
	"github.com/leogr/yoloctl/internal/layout"
	"github.com/leogr/yoloctl/internal/spaceenv"
	"github.com/spf13/cobra"
)

type boxFilesRequest struct {
	Box    string `json:"box"`
	Source string `json:"source"`
}

func (r boxFilesRequest) validate() error {
	if err := layout.ValidateName("agentbox", r.Box); err != nil {
		return err
	}
	if r.Source == "" || strings.ContainsRune(r.Source, 0) || len(r.Source) > 4096 {
		return errors.New("box cp needs a nonempty container path of at most 4096 bytes")
	}
	return nil
}
func parseBoxFilesSource(value string) (boxFilesRequest, error) {
	box, source, ok := strings.Cut(value, ":")
	if !ok {
		return boxFilesRequest{}, errors.New("source must be BOX:PATH (container to host only)")
	}
	r := boxFilesRequest{box, source}
	return r, r.validate()
}
func decodeBoxFilesRequest(req controller.Request) (boxFilesRequest, error) {
	var r boxFilesRequest
	if req.Method != "box.files.export" || len(req.Args) != 0 || len(req.Flags) != 0 || req.Terminal || req.View || req.Session != nil || req.Cwd != "" || req.Term != "" || req.Rows != 0 || req.Cols != 0 || req.Dash > 0 || len(req.Payload) > 8192 {
		return r, errors.New("box file export accepts only typed box/source metadata")
	}
	if err := decodeBoundedJSON(bytes.NewReader(req.Payload), &r, 8192); err != nil {
		return r, errors.New("invalid box file export metadata")
	}
	return r, r.validate()
}
func boxCPHostAllowed() error {
	if spaceenv.Native() || os.Getenv(spaceenv.ContextEnv) != "" {
		return errors.New("box cp is host-only")
	}
	if _, present := os.LookupEnv("OPENSHELL_SANDBOX"); present {
		return errors.New("box cp is host-only")
	}
	op, err := loadOperatorClient()
	if err != nil {
		return err
	}
	if op != nil || installContainerSignal() != "" {
		return errors.New("box cp is host-only; box operators cannot export host files")
	}
	return nil
}

// These narrow seams let tests prove admission precedes any local write or RPC.
var boxCPAllowed = boxCPHostAllowed
var boxCPStream = func(ctx context.Context, space string, r boxFilesRequest, out, errw io.Writer) error {
	boot, err := checkControllerSpace(ctx, space)
	if err != nil {
		return err
	}
	payload, err := json.Marshal(r)
	if err != nil {
		return err
	}
	return callHostController(ctx, boot, controller.Request{ID: rand.Text(), Method: "box.files.export", Space: space, Payload: payload}, controller.ClientIO{Out: out, Err: errw, Log: controller.HostLogSink()})
}

func newAgentboxCPCmd() *cobra.Command {
	var overwrite bool
	cmd := &cobra.Command{Use: "cp BOX:PATH DEST|-", SilenceUsage: true, Short: "Copy files from a running or stopped box to the host", Long: `Copy retained container files without starting or changing the box. The selected
space and matching controller must already run. Relative source paths use the
container root; PATH/. copies directory contents. An existing host directory
receives the source basename. Existing directories merge; file collisions refuse
unless --overwrite replaces a regular file. Destination links are never followed.

Extraction stages the whole archive beside DEST before publication, requiring
proportional free space. Safe relative links and ordinary mode/mtime are retained;
VM ownership, setid bits and extended attributes are not. Absolute/escaping links,
special files and unsafe archive names are refused. A merge can publish a prefix
before a later collision; its error reports that partial result.

DEST '-' writes the producer's uncompressed tar unchanged, with diagnostics on
stderr. A failed stream can leave a partial archive. Raw container archives are
unvalidated; inspect them before manually extracting. Running files are live reads,
not snapshots. Explicit bind paths use their existing VM sources; copying a
snapshot ancestor does not recursively collect nested binds. No source credential
or account-path filtering is applied. box copy copies settings; box cp copies files.

  yoloctl box cp coder:/tmp/result.txt ./result.txt
  yoloctl box cp coder:/workdir/. ./recovery
  yoloctl box cp coder:/home/sandbox - > recovery.tar`, Args: func(cmd *cobra.Command, args []string) error {
		if err := cobra.ExactArgs(2)(cmd, args); err != nil {
			return err
		}
		if _, err := parseBoxFilesSource(args[0]); err != nil {
			return err
		}
		if args[1] == "" {
			return errors.New("select a host destination or '-'")
		}
		return nil
	}, RunE: func(cmd *cobra.Command, args []string) error {
		if err := boxCPAllowed(); err != nil {
			return asUserError(err)
		}
		r, err := parseBoxFilesSource(args[0])
		if err != nil {
			return asUserError(err)
		}
		space, err := selectedSpace(cmd)
		if err != nil {
			return err
		}
		produce := func(ctx context.Context, out io.Writer) error {
			return boxCPStream(ctx, space, r, out, cmd.ErrOrStderr())
		}
		if args[1] == "-" {
			return produce(cmd.Context(), cmd.OutOrStdout())
		}
		destination, err := expandUserPath(args[1])
		if err != nil {
			return err
		}
		return boxexport.Copy(cmd.Context(), destination, boxexport.Options{Overwrite: overwrite, Contents: path.Base(r.Source) == "."}, produce)
	}}
	cmd.Flags().String("space", "", "Space name")
	cmd.Flags().BoolVar(&overwrite, "overwrite", false, "Atomically replace regular files; never follow destination links")
	cmd.ValidArgsFunction = completeBoxCP
	return cmd
}
func completeBoxCP(cmd *cobra.Command, args []string, prefix string) ([]cobra.Completion, cobra.ShellCompDirective) {
	none := cobra.ShellCompDirectiveNoFileComp
	if boxCPAllowed() != nil {
		return nil, none
	}
	if len(args) == 1 {
		return completeHostFiles(cmd, args, prefix)
	}
	if len(args) != 0 || strings.Contains(prefix, ":") {
		return nil, none
	}
	space, err := selectedSpace(cmd)
	if err != nil {
		return nil, none
	}
	ctx, cancel := context.WithTimeout(cmd.Context(), 2*time.Second)
	defer cancel()
	boot, err := checkControllerSpace(ctx, space)
	if err != nil {
		return nil, none
	}
	payload, _ := json.Marshal(completionRequest{Command: "box show", Prefix: prefix})
	var b boundedBuffer
	b.Limit = controller.MaxDocument
	if err := callHostController(ctx, boot, controller.Request{ID: rand.Text(), Method: "control.complete", Space: space, Payload: payload, Dash: -1}, controller.ClientIO{Out: &b, Err: io.Discard}); err != nil {
		return nil, none
	}
	var result completionResult
	if decodeBoundedJSON(&b.Buffer, &result, controller.MaxDocument) != nil {
		return nil, none
	}
	for i, v := range result.Values {
		name, description, _ := strings.Cut(v, "\t")
		result.Values[i] = name + ":"
		if description != "" {
			result.Values[i] += "\t" + description
		}
	}
	return result.Values, none | cobra.ShellCompDirectiveNoSpace
}
