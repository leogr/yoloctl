package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sort"
	"text/tabwriter"
	"time"

	"github.com/leogr/yoloctl/internal/agentboximage"
	"github.com/leogr/yoloctl/internal/container"
	"github.com/leogr/yoloctl/internal/features"
	"github.com/leogr/yoloctl/internal/imagejob"
	"github.com/leogr/yoloctl/internal/logging"
	"github.com/leogr/yoloctl/internal/ocifeature"
	vmhost "github.com/leogr/yoloctl/internal/vm"
	"github.com/spf13/cobra"
)

func newImageCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "image",
		Short: "Manage box container images",
		Long: `Build, list, pull, and refresh container images that boxes run.

` + "`yoloctl image build --tag REF --features F1,F2`" + ` generates a
Dockerfile that extends the yoloctl base image with each feature's
install steps and builds it inside the space's VM under REF. Point a
box at it with ` + "`--image REF`" + `.`,
	}
	cmd.AddCommand(newImageBuildCmd(), newImageListCmd(), newImagePullCmd(), newImageRefreshCmd(), newImagePruneCmd())
	return cmd
}

// newImagePruneCmd reclaims feature-composed images no box references.
func newImagePruneCmd() *cobra.Command {
	var (
		spaceName string
		dryRun    bool
	)
	cmd := &cobra.Command{
		Use:   "prune",
		Short: "Remove feature-composed images no box references",
		Long: `Removes feature-composed runtime images (` + agentboximage.ComposedImageRepo + `:*)
with yoloctl's composed-image label, when neither a declaration nor an existing
container references them. Desired images, bases, aliases and actual images of
running or stopped containers are protected. Incomplete inventories stop cleanup.

The controller performs this cleanup automatically while idle. BuildKit also
collects unused build cache. This command only removes obsolete composed tags;
--dry-run lists candidates without removing anything.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := resolveSpaceFlag(cmd, &spaceName); err != nil {
				return err
			}
			return runImagePrune(cmd.Context(), cmd.OutOrStdout(), spaceName, dryRun)
		},
	}
	cmd.Flags().StringVar(&spaceName, "space", defaultSpaceName, "Space whose composed images to prune")
	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "List what would be removed without removing")
	return cmd
}

// runImagePrune shares automatic GC's complete inventory and retention rules.
func runImagePrune(ctx context.Context, out io.Writer, spaceName string, dryRun bool) error {
	refs, err := declaredImageReferences(spaceName)
	if err != nil {
		return fmt.Errorf("inventory declarations (no images removed): %w", err)
	}
	removed, err := collectImages(ctx, container.New(spaceName), refs, out, dryRun)
	if err == nil && !dryRun {
		cliInfo(logging.Box, "pruned %d composed image(s).", removed)
	}
	return err
}

// newImageRefreshCmd is `yoloctl image refresh` and force-rebuilds the
// yoloctl-managed base image. Agent CLIs are composed
// features, so refreshing the base does not update them; use `box reconcile
// --rebuild` for a fresh feature build or `agent update` for an in-place
// update in running boxes.
// EnsureImage with Force=true reruns installers while retaining downloads.
func newImageRefreshCmd() *cobra.Command {
	var spaceName string
	cmd := &cobra.Command{
		Use:   "refresh",
		Short: "Rebuild the base agentbox image",
		Long: `Rebuilds the single yoloctl-managed base image from its embedded
Dockerfile context, rerunning installers to refresh the OS/runtime layers.
Package download caches are retained; repository indexes are refreshed.

  yoloctl image refresh

The agent CLI is not baked into an image — it is a feature composed
into a box's runtime image (image = base + resolved features). To pull a
newer agent CLI into a box, use ` + "`yoloctl box reconcile BOX --rebuild`" + `
(a fresh composed build + recreate) or ` + "`yoloctl agent update PROFILE`" + `
(in-place npm refresh of running boxes). Refresh only touches the base
image. Existing feature-composed images keep the previous base: a plain
` + "`box recreate`" + `, or a new box with the same features, reuses them. Run
` + "`yoloctl box reconcile BOX --rebuild`" + ` for each affected box to rebuild its
composed image on the refreshed base and recreate it.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := resolveSpaceFlag(cmd, &spaceName); err != nil {
				return err
			}
			if err := ensureSpaceRunning(cmd.Context(), spaceName); err != nil {
				return fmt.Errorf("ensure space %s: %w", spaceName, err)
			}
			c := container.New(spaceName)
			notice := cmd.ErrOrStderr()
			ref := agentboximage.RefAgentbox
			_, _ = fmt.Fprintf(notice, "refreshing %s\n", ref)
			if cmd.Context().Value(warmKey{}) != nil {
				prep, err := agentboximage.PrepareManagedBase(true)
				if err != nil {
					return err
				}
				if _, err := warmManualImage(cmd.Context(), prep); err != nil {
					return err
				}
			} else if err := agentboximage.EnsureImage(cmd.Context(), c, ref, agentboximage.Options{
				Force:  true,
				Notice: notice,
			}); err != nil {
				return fmt.Errorf("refresh %s: %w", ref, err)
			}
			_, _ = fmt.Fprintln(notice,
				"Existing boxes and composed images keep the previous base. "+
					"Run `yoloctl box reconcile BOX --rebuild` for each affected box to rebuild its composed image on this base and recreate it; "+
					"a plain `yoloctl box recreate BOX` reuses an existing composed image. "+
					"`yoloctl agent update PROFILE` refreshes only the agent CLI in running boxes.")
			return nil
		},
	}
	cmd.Flags().StringVar(&spaceName, "space", defaultSpaceName, "Space whose image cache to refresh")
	return cmd
}

func newImageListCmd() *cobra.Command {
	var (
		spaceName string
		showAll   bool
		allSpaces bool
		asJSON    bool
	)
	cmd := &cobra.Command{
		Use:     "list",
		Aliases: []string{"ls"},
		Short:   "List container images in a space (or across all spaces)",
		Long: `Lists images in a space VM's local image cache, one row per image
prefixed with the SPACE whose VM holds it. Each space is a separate VM
with its own cache, so the listing is scoped to one space by default
(the resolved --space / context pin / "default").

By default the listing is filtered to yoloctl/* references (the plain yoloctl
base, feature-composed images, and user-built yoloctl tags). Pass --all to
also include non-yoloctl
images — arbitrary upstream bases pulled as FROM parents, image pull
results, and custom --image refs (useful when debugging VM disk usage).

Pass --all-spaces to aggregate every space's cache into one listing
(the SPACE column then varies per row). Stopped spaces are skipped with
a notice, since the listing runs nerdctl inside each VM. --all-spaces is
mutually exclusive with --space.

  yoloctl image list                       # yoloctl/* in the current space
  yoloctl image list --all                 # + non-yoloctl images
  yoloctl image list --all-spaces          # yoloctl/* across every space
  yoloctl image list --all --all-spaces    # everything, everywhere
  yoloctl image list --json                # machine-readable output`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			if allSpaces && flagChanged(cmd, "space") {
				return asUserError(errors.New("--space and --all-spaces are mutually exclusive"))
			}
			filter := "yoloctl/*"
			if showAll {
				filter = ""
			}
			var rows []imageRow
			if allSpaces {
				var err error
				rows, err = listImagesAllSpaces(cmd.Context(), filter, cmd.ErrOrStderr())
				if err != nil {
					return err
				}
			} else {
				if err := resolveSpaceFlag(cmd, &spaceName); err != nil {
					return err
				}
				imgs, err := container.New(spaceName).ImagesList(cmd.Context(), filter)
				if err != nil {
					return err
				}
				rows = tagImageRows(spaceName, imgs)
			}
			if asJSON {
				return writeImagesJSON(cmd.OutOrStdout(), rows)
			}
			return writeImagesTable(cmd.OutOrStdout(), rows)
		},
	}
	cmd.Flags().StringVar(&spaceName, "space", defaultSpaceName, "Space whose image cache to read")
	cmd.Flags().BoolVar(&showAll, "all", false, "Include non-yoloctl images")
	cmd.Flags().BoolVar(&allSpaces, "all-spaces", false, "List across every space (skips stopped spaces; mutually exclusive with --space)")
	cmd.Flags().BoolVar(&asJSON, "json", false, "Emit JSON instead of the table view")
	return cmd
}

// imageRow pairs a cached image with the space whose VM holds it, so the
// table/JSON can carry a SPACE column — constant in single-space mode,
// varying under --all-spaces.
type imageRow struct {
	Space string
	Image container.Image
}

// tagImageRows stamps every image with its owning space.
func tagImageRows(space string, imgs []container.Image) []imageRow {
	rows := make([]imageRow, 0, len(imgs))
	for _, img := range imgs {
		rows = append(rows, imageRow{Space: space, Image: img})
	}
	return rows
}

// listImagesAllSpaces aggregates the image caches of every Running yoloctl
// space, grouped by space name. A stopped space can't answer (the listing
// shells nerdctl inside the VM), so it's skipped with a notice rather than
// failing the whole command; likewise a per-VM list error degrades to a
// notice so one broken space doesn't sink the aggregate.
func listImagesAllSpaces(ctx context.Context, filter string, notice io.Writer) ([]imageRow, error) {
	instances, err := vmhost.New().List(ctx)
	if err != nil {
		return nil, err
	}
	sort.Slice(instances, func(i, j int) bool { return instances[i].Name < instances[j].Name })
	var rows []imageRow
	for _, inst := range instances {
		if inst.Status != "Running" {
			cliNotice(logging.Box, "skipping space %s (%s) — start it to list its images", inst.Name, inst.Status)
			continue
		}
		imgs, err := container.New(inst.Name).ImagesList(ctx, filter)
		if err != nil {
			cliNotice(logging.Box, "could not list images in space %s: %v", inst.Name, err)
			continue
		}
		rows = append(rows, tagImageRows(inst.Name, imgs)...)
	}
	return rows, nil
}

func newImagePullCmd() *cobra.Command {
	var spaceName string
	cmd := &cobra.Command{
		Use:   "pull REF",
		Short: "Pull a container image into a space",
		Long: `Wraps ` + "`sudo nerdctl pull <ref>`" + ` inside the space VM. Egress goes
through the VM's normal network — NOT through the openshell-sandbox
L7 proxy, which only applies inside box containers. Useful for
prefetching a base image before creating boxes from it.

  yoloctl image pull ubuntu:24.04
  yoloctl image pull ghcr.io/some/agent:v1 --space prod-untrusted`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := resolveSpaceFlag(cmd, &spaceName); err != nil {
				return err
			}
			ref := args[0]
			if ref == "" {
				return asUserError(errors.New("REF must not be empty"))
			}
			prep := agentboximage.PreparedContext{DesiredRef: ref, BaseRef: ref, Recipe: imagejob.Recipe{Builder: "pull/1", Dockerfile: []byte("FROM " + ref)}}
			if handled, err := warmManualImage(cmd.Context(), prep); handled {
				return err
			}
			return container.New(spaceName).ImagePull(cmd.Context(), ref)
		},
	}
	cmd.Flags().StringVar(&spaceName, "space", defaultSpaceName, "Space whose image cache to pull into")
	return cmd
}

func writeImagesTable(w io.Writer, rows []imageRow) error {
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	_, _ = fmt.Fprintln(tw, "SPACE\tREPOSITORY\tTAG\tID\tSIZE\tCREATED")
	for _, r := range rows {
		_, _ = fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\t%s\n",
			r.Space, r.Image.Repository, r.Image.Tag, r.Image.ID, r.Image.Size, r.Image.CreatedAt)
	}
	return tw.Flush()
}

// writeImagesJSON emits a top-level array of image objects. Field
// names use snake_case keys, raw bytes /
// Unix seconds where possible. nerdctl doesn't expose the raw byte
// count for images, so `size` keeps the human-formatted string from
// nerdctl ("512MiB"); a follow-up could parse it but the table view
// is the consumer that cares about formatting anyway. `created_unix`
// is normalized from nerdctl's localized time.Time.String() form to
// integer seconds since the epoch, matching the `_unix` convention
// the schema doc spells out. If parsing fails (older nerdctl emits
// a different layout), `created_unix` is 0 — consumers can fall back
// to the table view in that case.
func writeImagesJSON(w io.Writer, rows []imageRow) error {
	type imageJSON struct {
		Space       string `json:"space"`
		Repository  string `json:"repository"`
		Tag         string `json:"tag"`
		ID          string `json:"id"`
		Size        string `json:"size"`
		CreatedUnix int64  `json:"created_unix"`
	}
	out := make([]imageJSON, 0, len(rows))
	for _, r := range rows {
		out = append(out, imageJSON{
			Space:       r.Space,
			Repository:  r.Image.Repository,
			Tag:         r.Image.Tag,
			ID:          r.Image.ID,
			Size:        r.Image.Size,
			CreatedUnix: parseNerdctlCreatedAt(r.Image.CreatedAt),
		})
	}
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	return enc.Encode(out)
}

// parseNerdctlCreatedAt parses the layout nerdctl uses for its
// CreatedAt field — Go's `time.Time.String()` default
// ("2006-01-02 15:04:05.999999999 -0700 MST"). Returns 0 on failure
// so JSON consumers see "unknown" rather than a wrong timestamp.
func parseNerdctlCreatedAt(s string) int64 {
	const layout = "2006-01-02 15:04:05.999999999 -0700 MST"
	t, err := time.Parse(layout, s)
	if err != nil {
		// Try a few alternates in case nerdctl switches at some point.
		for _, alt := range []string{
			"2006-01-02 15:04:05 -0700 MST",
			time.RFC3339,
		} {
			if t, err = time.Parse(alt, s); err == nil {
				return t.Unix()
			}
		}
		return 0
	}
	return t.Unix()
}

func newImageBuildCmd() *cobra.Command {
	var (
		spaceName     string
		tag           string
		featureNames  []string
		extraFeatures []string
		optionSpecs   []string
		baseImage     string
		dryRun        bool
	)
	cmd := &cobra.Command{
		Use:   "build --tag REF --feature FEATURE [--feature FEATURE...]",
		Short: "Build a feature-composed box image",
		Long: `Generates a Dockerfile that extends --base with each named feature
(looked up in the yoloctl feature registry) and builds it inside the
space's VM under --tag. Point a box at the result with --image REF.

With --dry-run, the generated Dockerfile is printed and no build runs.

  yoloctl image build --tag yoloctl/myrepo:base --features claude,go,github
  yoloctl image build --tag local/exp:base --features codex --dry-run`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			if tag == "" {
				return asUserError(errors.New("--tag REF is required"))
			}
			featureNames = append(featureNames, extraFeatures...)
			if len(featureNames) == 0 {
				return asUserError(errors.New("--feature or --features is required (built-in names or OCI Feature references)"))
			}
			if err := resolveSpaceFlag(cmd, &spaceName); err != nil {
				return err
			}
			options, err := ocifeature.ApplyOptions(featureNames, nil, optionSpecs)
			if err != nil {
				return err
			}
			plan, err := ocifeature.Resolve(cmd.Context(), ocifeature.NewRegistry(), featureNames, options, nil)
			if err != nil {
				return err
			}
			dockerfile, err := features.GenerateDockerfile(baseImage, featureNames, plan)
			if err != nil {
				return err
			}
			if dryRun {
				_, _ = fmt.Fprintf(cmd.OutOrStdout(), "# tag: %s\n%s", tag, dockerfile)
				return nil
			}
			if err := ensureSpaceRunning(cmd.Context(), spaceName); err != nil {
				return fmt.Errorf("ensure space %s: %w", spaceName, err)
			}
			files, err := plan.BuildFiles(cmd.Context())
			if err != nil {
				return err
			}
			return buildImageInVM(cmd.Context(), spaceName, tag, baseImage, dockerfile, files)
		},
	}
	cmd.Flags().StringVar(&spaceName, "space", defaultSpaceName, "Space to build the image in")
	cmd.Flags().StringVar(&tag, "tag", "", "Image tag to build (required)")
	cmd.Flags().StringArrayVar(&extraFeatures, "feature", nil, "Feature name or OCI reference (repeatable)")
	cmd.Flags().StringArrayVar(&optionSpecs, "feature-option", nil, "OCI option FEATURE.OPTION=VALUE (repeatable; non-secret)")
	cmd.Flags().StringSliceVar(&featureNames, "features", nil, "Comma-separated feature names or OCI references to compose into the image")
	cmd.Flags().StringVar(&baseImage, "base", defaultAgentboxImage, "Base image to FROM")
	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "Print the generated Dockerfile instead of building")
	return cmd
}

// buildImageInVM builds tag in env VM with the supplied Dockerfile.
// Wraps the single-file Dockerfile in a tar and delegates the
// VM-side "stage temp dir + extract + nerdctl build" dance to
// container.Client.Build, which is shared with the on-demand image
// builder in internal/agentboximage.
func buildImageInVM(ctx context.Context, env, tag, baseImage, dockerfile string, files ...map[string][]byte) error {
	var entries map[string][]byte
	if len(files) > 0 {
		entries = files[0]
	}
	prep := agentboximage.PreparedContext{DesiredRef: tag, BaseRef: baseImage, Recipe: imagejob.Recipe{Builder: "manual/1", Dockerfile: []byte(dockerfile), Files: entries}}
	if handled, err := warmManualImage(ctx, prep); handled {
		return err
	}
	tarBuf, err := agentboximage.TarDockerfile(dockerfile, entries)
	if err != nil {
		return err
	}
	return container.New(env).Build(ctx, container.BuildOptions{
		Tag:         tag,
		InputImages: []string{baseImage},
		ContextTar:  tarBuf,
	})
}
