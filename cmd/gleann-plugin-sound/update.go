package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/spf13/cobra"

	"github.com/tevfik/gleann-plugin-sound/internal/buildinfo"
	"github.com/tevfik/gleann-plugin-sound/internal/httpserver"
	"github.com/tevfik/gleann-plugin-sound/internal/selfupdate"
)

const (
	updateOwner = "tevfik"
	updateRepo  = "gleann-plugin-sound"
)

func newUpdateCmd() *cobra.Command {
	var check, force bool
	cmd := &cobra.Command{
		Use:     "update",
		Aliases: []string{"upgrade"},
		Short:   "Update this binary from the latest GitHub release",
		Long: `Replaces this binary with the latest GitHub release of the same build
flavour (stub, whisper, onnx or whisper-onnx), verified against the release's
checksums.txt. A flavour with no published build is never swapped for another one:
rebuild it instead (make build-all).

Models, ~/.gleann/sound.json, enrolled voices and the gleann-asr-helper sidecar are
not touched. Restart running servers afterwards.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx, cancel := context.WithTimeout(cmd.Context(), 10*time.Minute)
			defer cancel()
			return runUpdate(ctx, check, force)
		},
	}
	cmd.Flags().BoolVar(&check, "check", false, "only report whether a newer release exists")
	cmd.Flags().BoolVar(&force, "force", false, "reinstall even when already up to date")
	return cmd
}

func runUpdate(ctx context.Context, check, force bool) error {
	rel, err := selfupdate.Latest(ctx, updateOwner, updateRepo)
	if err != nil {
		return fmt.Errorf("update: %w", err)
	}
	current := httpserver.PluginVersion
	newer, ok := selfupdate.IsNewer(current, rel.Tag)
	if ok && !newer && !force {
		fmt.Printf("%s %s (%s build) is up to date (latest %s)\n", updateRepo, current, buildinfo.Flavor, rel.Tag)
		return nil
	}
	if check {
		fmt.Printf("update available: %s -> %s (%s build)\n", current, rel.Tag, buildinfo.Flavor)
		return nil
	}
	dest, err := os.Executable()
	if err != nil {
		return fmt.Errorf("update: %w", err)
	}
	if resolved, err := filepath.EvalSymlinks(dest); err == nil {
		dest = resolved
	}
	verified, err := selfupdate.Install(ctx, rel, updateRepo, buildinfo.Flavor, dest)
	if err != nil {
		return fmt.Errorf("update: %w", err)
	}
	if !verified {
		fmt.Fprintln(os.Stderr, "warning: no checksum published for this release; archive not verified")
	}
	fmt.Printf("updated %s to %s. Restart running servers (plugin-serve, dictate daemon) to use it.\n", dest, rel.Tag)
	return nil
}
