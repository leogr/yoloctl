package main

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/leogr/yoloctl/internal/controller"
	"github.com/leogr/yoloctl/internal/spaceconfig"
	"github.com/spf13/cobra"
	"github.com/spf13/pflag"
)

type completionRequest struct {
	Command string `json:"command"`
	Flag    string `json:"flag,omitempty"`
	Prefix  string `json:"prefix"`
}
type completionResult struct {
	Values    []string                 `json:"values"`
	Directive cobra.ShellCompDirective `json:"directive"`
}

var selectCompletionTarget = selectControllerTarget

func controllerCompletion(flag string, local cobra.CompletionFunc) cobra.CompletionFunc {
	return func(cmd *cobra.Command, args []string, prefix string) ([]string, cobra.ShellCompDirective) {
		if cmd.Root().Annotations["controller-client"] != "true" {
			return local(cmd, args, prefix)
		}
		none := cobra.ShellCompDirectiveNoFileComp
		operator, err := loadOperatorClient()
		if err != nil {
			return nil, none
		}
		if flag == "space" || flag == "from-space" {
			if operator != nil {
				return []string{operator.Space}, none
			}
			return local(cmd, args, prefix)
		}
		method := commandMethod(cmd)
		if operator == nil && (flag == "mount" || flag == "workdir" && method != "box.list") {
			// Host filesystem completion uses the host-owned grants. It needs
			// neither a controller boot nor access to native declarations.
			return local(cmd, args, prefix)
		}
		if method == "space.mount.remove" && flag == "" && len(args) == 1 {
			boot, err := spaceconfig.LoadBootstrap(args[0])
			if err != nil {
				return nil, none
			}
			paths := make([]string, 0, len(boot.Mounts))
			for _, m := range boot.Mounts {
				paths = append(paths, m.Source)
			}
			return projectLiteralPaths(paths, prefix), none
		}
		_, routed := controllerOperations[method]
		routed = routed || method == "box.edit" || method == "account.edit"
		if !routed || flag == "file" || strings.HasPrefix(method, "space.") {
			return local(cmd, args, prefix)
		}
		if operator != nil && !strings.HasPrefix(method, "box.") && !operatorChatMethod(method) {
			return nil, none
		}
		previous := cmd.Context()
		ctx, cancel := context.WithTimeout(previous, 2*time.Second)
		defer cancel()
		cmd.SetContext(ctx)
		defer cmd.SetContext(previous)
		var targets []controllerTarget
		all, _ := cmd.Flags().GetBool("all-spaces")
		if all {
			if operator != nil || flagChanged(cmd, "space") {
				return nil, none
			}
			names, err := spaceconfig.List()
			if err != nil {
				return nil, none
			}
			for _, name := range names {
				boot, err := checkControllerSpace(ctx, name)
				if err != nil {
					continue
				}
				targets = append(targets, controllerTarget{Space: name, Call: func(ctx context.Context, req controller.Request, streams controller.ClientIO) error {
					return callHostController(ctx, boot, req, streams)
				}})
			}
		} else {
			target, err := selectCompletionTarget(ctx, cmd)
			if err != nil {
				return nil, none
			}
			targets = append(targets, target)
		}
		// Copy's source positional and inherited Feature options both read the
		// source declaration. Other flags (for example --account) still refer
		// to the destination, so do not redirect every copy completion.
		copySource := flag == "" && len(args) == 0 || flag == "feature-option" && len(args) > 0
		if method == "box.copy" && copySource && len(targets) == 1 {
			target := &targets[0]
			if from, _ := cmd.Flags().GetString("from-space"); from != "" && from != target.Space {
				if target.Operator {
					return nil, none
				}
				source, err := checkControllerSpace(ctx, from)
				if err != nil {
					return nil, none
				}
				target.Space = from
				target.Call = func(ctx context.Context, req controller.Request, io controller.ClientIO) error {
					return callHostController(ctx, source, req, io)
				}
			}
		}
		data, err := json.Marshal(completionRequest{Command: strings.ReplaceAll(method, ".", " "), Flag: flag, Prefix: prefix})
		if err != nil {
			return nil, none
		}
		req := controller.Request{Method: "control.complete", Args: args, Dash: -1, Payload: data, Flags: map[string][]string{}}
		req.Cwd, _ = os.Getwd()
		cmd.Flags().Visit(func(f *pflag.Flag) {
			if slice, ok := f.Value.(pflag.SliceValue); ok {
				req.Flags[f.Name] = slice.GetSlice()
			} else {
				req.Flags[f.Name] = []string{f.Value.String()}
			}
		})
		delete(req.Flags, "all-spaces") // each controller sees only its own scope
		var values []string
		directive := none
		for _, target := range targets {
			req.ID, req.Space = rand.Text(), target.Space
			raw := boundedBuffer{Limit: controller.MaxDocument}
			if err := target.Call(ctx, req, controller.ClientIO{Out: &raw, Err: io.Discard}); err != nil {
				continue
			}
			var result completionResult
			if err := decodeBoundedJSON(&raw.Buffer, &result, controller.MaxDocument); err != nil {
				continue
			}
			values = append(values, result.Values...)
			directive |= result.Directive
		}
		if len(targets) > 1 {
			slices.Sort(values)
			values = slices.Compact(values)
		}
		return values, directive
	}
}

func runControllerCompletion(ctx context.Context, p controller.Principal, req controller.Request) error {
	return runControllerCompletionInto(ctx, p, req, os.Stdout)
}

func runControllerCompletionInto(ctx context.Context, p controller.Principal, req controller.Request, out io.Writer) error {
	var query completionRequest
	if err := decodeBoundedJSON(bytes.NewReader(req.Payload), &query, controller.MaxDocument); err != nil {
		return err
	}
	root := newRootCmd()
	root.SetOut(out)
	// Chat and audit use typed client commands, so they are absent from the ordinary
	// native execution root. Completion only runs the local read callbacks;
	// it never executes these commands' mutation/stream handlers.
	root.AddCommand(newChatCmd(), newAuditCmd())
	registerCompletions(root)
	cmd := findByPath(root, strings.Fields(query.Command)...)
	if cmd == nil {
		return errors.New("unknown completion command")
	}
	method := commandMethod(cmd)
	op, exists := controllerOperations[method]
	if method == "box.edit" {
		op, exists = controller.Operation{BoxAllowed: true}, true
	}
	if method == "account.edit" {
		exists = true
	}
	if !exists || !p.Admin() && !op.BoxAllowed {
		return errors.New("completion command is not authorized")
	}
	if !p.Admin() && (query.Flag == "mount" || query.Flag == "workdir" && method != "box.list") {
		return completeOperatorPaths(ctx, p, req, query)
	}
	flags := pflag.NewFlagSet("completion", pflag.ContinueOnError)
	flags.AddFlagSet(cmd.Flags())
	flags.AddFlagSet(cmd.InheritedFlags())
	if req.Flags == nil {
		req.Flags = map[string][]string{}
	}
	req.Flags["space"] = []string{req.Space}
	delete(req.Flags, "all-spaces")
	argv, err := controllerFlagArgs(flags, req.Flags)
	if err != nil {
		return err
	}
	if err := cmd.ParseFlags(argv); err != nil {
		return err
	}
	cmd.SetContext(ctx)
	complete := cmd.ValidArgsFunction
	if query.Flag != "" {
		var ok bool
		complete, ok = cmd.GetFlagCompletionFunc(query.Flag)
		if !ok {
			return errors.New("flag has no dynamic completion")
		}
	}
	result := completionResult{Directive: cobra.ShellCompDirectiveNoFileComp}
	if complete != nil {
		result.Values, result.Directive = complete(cmd, req.Args, query.Prefix)
	}
	return json.NewEncoder(root.OutOrStdout()).Encode(result)
}

func completeOperatorPaths(ctx context.Context, p controller.Principal, req controller.Request, query completionRequest) error {
	result := completionResult{Directive: cobra.ShellCompDirectiveNoFileComp}
	details, err := operatorRuntime(ctx, p)
	if err != nil {
		return err
	}
	grants, err := spaceconfig.Load(req.Space)
	if err != nil {
		return err
	}
	if strings.Contains(query.Prefix, ":") || strings.HasPrefix(query.Prefix, "~") {
		return json.NewEncoder(os.Stdout).Encode(result)
	}
	var roots []rootCandidate
	for _, m := range grants.Mounts {
		if query.Flag == "workdir" && !m.Writable {
			continue
		}
		roots = append(roots, rootCandidate{path: m.Source, desc: "host mount grant"})
	}
	prefix := query.Prefix
	translated := ""
	if prefix != "" {
		translated, err = mapOperatorPath(prefix, req.Cwd, details.Mounts, grants)
		if err != nil {
			return json.NewEncoder(os.Stdout).Encode(result)
		}
		if strings.HasSuffix(prefix, "/") {
			translated += "/"
		}
	}
	result.Values, result.Directive = listPathCandidates(translated, query.Flag == "workdir", roots)
	if prefix != "" {
		// Preserve the caller's spelling, including relative paths. Only
		// mapped descendants are offered; a helper cannot complete VM roots.
		var projected []string
		for _, candidate := range result.Values {
			value, desc, _ := strings.Cut(candidate, "\t")
			if !strings.HasPrefix(value, translated) {
				continue
			}
			value = prefix + strings.TrimPrefix(value, translated)
			if !filepath.IsAbs(prefix) && strings.HasPrefix(value, "/") {
				continue
			}
			if desc != "" {
				value += "\t" + desc
			}
			projected = append(projected, value)
		}
		result.Values = projected
	}
	return json.NewEncoder(os.Stdout).Encode(result)
}
