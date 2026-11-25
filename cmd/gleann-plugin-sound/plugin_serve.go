package main

import (
	"context"
	"fmt"
	"log"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/spf13/cobra"
	"github.com/tevfik/gleann-plugin-sound/internal/config"
	"github.com/tevfik/gleann-plugin-sound/internal/core"
	"github.com/tevfik/gleann-plugin-sound/internal/httpserver"
	"github.com/tevfik/gleann-plugin-sound/internal/tcasr"
	"github.com/tevfik/gleann-plugin-sound/internal/tui"
)

// newPluginServeCmd creates the "plugin-serve" subcommand.
//
// This runs an HTTP server that conforms to the gleann PluginManager contract
// (GET /health, GET /info, POST /convert) so that gleann build can automatically
// transcribe audio/video files during index construction.
//
// Usage:
//
//	gleann-plugin-sound plugin-serve --port 8767
func newPluginServeCmd() *cobra.Command {
	var port int

	cmd := &cobra.Command{
		Use:   "plugin-serve",
		Short: "Run HTTP server for gleann plugin integration (audio transcription)",
		Long: `Starts an HTTP server that integrates with the gleann PluginManager.

When gleann builds an index and encounters audio/video files (.mp3, .wav, etc.),
it sends them to this server for transcription. The server runs the configured
backend and returns the transcribed text as markdown.

It also serves GET /live, a WebSocket that streams audio from a phone, browser
or meeting recorder (16 kHz mono PCM) and returns words in real time; see the
README for the protocol. /live needs a streaming backend (tc or nemotron).
Off loopback (--host 0.0.0.0) a --token is mandatory.

The model is loaded lazily on the first request to keep startup fast.
Register this plugin with 'gleann-plugin-sound install' before use.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			modelPath, _ := cmd.Flags().GetString("model")
			backend, _ := cmd.Flags().GetString("backend")
			language, _ := cmd.Flags().GetString("language")

			if resolved, err := resolveModelPath(modelPath); err == nil {
				modelPath = resolved
			} else {
				log.Printf("[plugin-serve] warning: model %q not found at startup: %v (daemon will retry on first request)", modelPath, err)
			}

			// Read language from config if not specified via flag.
			if language == "" {
				if cfg := config.Load(); cfg != nil && cfg.Language != "" {
					language = cfg.Language
				}
			}

			// Engine factory — creates the transcriber on first /convert request.
			factory := func() (core.Transcriber, error) {
				resolved, err := resolveModelPath(modelPath)
				if err != nil {
					return nil, fmt.Errorf("model %q not found: %w (check ~/.gleann/models or run 'gleann-plugin-sound setup')", modelPath, err)
				}
				log.Printf("[plugin-serve] creating transcriber: backend=%s model=%s", backend, resolved)
				return core.NewTranscriber(backend, resolved)
			}

			host, _ := cmd.Flags().GetString("host")
			token, _ := cmd.Flags().GetString("token")
			if token == "" {
				token = os.Getenv("GLEANN_SOUND_TOKEN")
			}
			maxLive, _ := cmd.Flags().GetInt("max-live")

			// gleann passes a per-launch token in GLEANN_PLUGIN_TOKEN; it is
			// accepted next to --token / GLEANN_SOUND_TOKEN.
			pluginToken := os.Getenv("GLEANN_PLUGIN_TOKEN")
			maxDurStr, _ := cmd.Flags().GetString("max-duration")
			maxDurSource := "--max-duration"
			if !cmd.Flags().Changed("max-duration") {
				if v := os.Getenv("GLEANN_SOUND_MAX_DURATION"); v != "" {
					maxDurStr, maxDurSource = v, "GLEANN_SOUND_MAX_DURATION"
				}
			}
			maxDuration, err := parseMaxDuration(maxDurStr, maxDurSource)
			if err != nil {
				return err
			}

			srv := httpserver.New(factory, language, port)
			refineFlag, _ := cmd.Flags().GetString("refine-model")
			refineModel := resolveRefineModel(refineFlag)
			if sp, _ := cmd.Flags().GetBool("speakers"); sp || !cmd.Flags().Changed("speakers") {
				labeler, closeSp, lerr := newSpeakerLabeler(sp)
				if lerr != nil {
					return fmt.Errorf("speakers: %w", lerr)
				}
				defer closeSp()
				if labeler != nil {
					srv.SetSpeakers(labeler)
				}
			}
			if refineModel != "" {
				r, rerr := tcasr.NewRefiner(config.ExpandPath(refineModel), language)
				if rerr != nil {
					return fmt.Errorf("refiner: %w", rerr)
				}
				defer func() { _ = httpserver.BoundedClose("refiner", r.Close) }()
				srv.SetRefiner(r)
				log.Printf("[plugin-serve] /live refinement on (%s)", refineModel)
			}
			srv.SetPluginToken(pluginToken)
			srv.SetMaxDuration(maxDuration)
			if err := srv.SetListen(host, token, maxLive); err != nil {
				return err
			}
			if token == "" && pluginToken == "" {
				log.Printf("[plugin-serve] WARNING: no access token configured (--token, GLEANN_SOUND_TOKEN or GLEANN_PLUGIN_TOKEN); /convert, /info, /settings and /models accept any local caller")
			}
			if cfg := config.Load(); cfg != nil {
				srv.SetCorrector(core.NewCorrector(cfg.Corrections))
			}
			defer srv.Close()

			// Graceful shutdown on SIGINT/SIGTERM, aligned with the stop
			// grace the host gave us (GLEANN_PLUGIN_STOP_GRACE): drain
			// inside it with 1s of slack to release the ASR helper, and
			// exit hard right at the grace, when the host would SIGKILL
			// (which would leak the native sidecar). The old fixed 20s drain / 25s wait
			// outlived the host's 10s patience.
			grace := stopGraceFromEnv(os.Getenv("GLEANN_PLUGIN_STOP_GRACE"))
			drain, hardExit := shutdownDeadlines(grace)
			log.Printf("[plugin-serve] stop grace %s: draining %s, hard exit %s", grace, drain, hardExit)
			sigCh := make(chan os.Signal, 1)
			signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
			shutdownDone := make(chan struct{})
			go func() {
				<-sigCh
				log.Println("[plugin-serve] shutting down...")
				ctx, cancel := context.WithTimeout(context.Background(), drain)
				defer cancel()
				if err := srv.Shutdown(ctx); err != nil {
					log.Printf("[plugin-serve] shutdown: %v", err)
				}
				close(shutdownDone)
			}()

			if err := srv.Serve(); err != nil {
				return err
			}
			select {
			case <-shutdownDone:
			case <-time.After(hardExit):
				// A handler parked past the drain deadline (Shutdown already
				// cancelled its context and closed its connection). Return
				// anyway: the deferred srv.Close() releases the refiner
				// helper and the engine instead of waiting for the host's
				// SIGKILL to do it.
				log.Printf("[plugin-serve] drain unfinished at %s; exiting now", hardExit)
			}
			return nil
		},
	}

	cmd.AddCommand(
		&cobra.Command{
			Use:   "enable",
			Short: "Run the plugin server in the background and start it at login",
			Long: `Registers the server with the OS (systemd user service, launchd agent or
Windows logon task) using the settings in ~/.gleann/sound.json, and starts it now.
Run it from the installed copy, not from a build directory.`,
			RunE: func(cmd *cobra.Command, args []string) error {
				if err := tui.EnableServe(tui.ServeArgs(config.Load())); err != nil {
					return err
				}
				fmt.Printf("Plugin server enabled on :%d (starts at login).\n", tui.DefaultServePort)
				return nil
			},
		},
		&cobra.Command{
			Use:   "disable",
			Short: "Stop the background plugin server and remove it from login",
			RunE: func(cmd *cobra.Command, args []string) error {
				if err := tui.DisableServe(); err != nil {
					return err
				}
				fmt.Println("Plugin server disabled.")
				return nil
			},
		},
		&cobra.Command{
			Use:   "status",
			Short: "Show whether the background plugin server is enabled",
			RunE: func(cmd *cobra.Command, args []string) error {
				if tui.ServeEnabled() {
					fmt.Println("enabled")
					return nil
				}
				return fmt.Errorf("disabled")
			},
		},
	)
	cmd.Flags().IntVar(&port, "port", 8767, "HTTP port for the plugin server")
	cmd.Flags().String("host", "127.0.0.1",
		"Address to listen on. Use 0.0.0.0 to accept phones/other machines (requires --token)")
	cmd.Flags().String("token", "",
		"Access token required on every route except /health (or env GLEANN_SOUND_TOKEN); GLEANN_PLUGIN_TOKEN from gleann is accepted too")
	cmd.Flags().Bool("speakers", false,
		"Label /live utterances with their speaker (auto-on once voices are enrolled; --speakers=false disables)")
	cmd.Flags().String("refine-model", "",
		"GGUF model (Qwen3-ASR) that re-transcribes each /live utterance after it ends ({\"type\":\"refined\"}). Default: refine_model from sound.json; --refine-model=off disables")
	cmd.Flags().String("max-duration", httpserver.DefaultMaxDuration.String(),
		"Longest media /convert accepts (413 beyond it; 0 disables; env GLEANN_SOUND_MAX_DURATION)")
	cmd.Flags().Int("max-live", httpserver.DefaultMaxLive, "Maximum concurrent /live sessions")
	cmd.Flags().String("language", "",
		"Language code for transcription (e.g. tr, en). Empty = auto-detect")

	return cmd
}

// parseMaxDuration parses the duration cap; source names the flag or
// environment variable it came from so the error points at the right one.
func parseMaxDuration(v, source string) (time.Duration, error) {
	d, err := time.ParseDuration(v)
	if err != nil || d < 0 {
		return 0, fmt.Errorf("invalid %s %q (use e.g. 2h, 90m, or 0 to disable)", source, v)
	}
	return d, nil
}

// defaultStopGrace matches the stop grace gleann's PluginManager gives a
// plugin between SIGINT and SIGKILL when GLEANN_PLUGIN_STOP_GRACE is unset or
// invalid.
const defaultStopGrace = 10 * time.Second

// stopGraceFromEnv interprets GLEANN_PLUGIN_STOP_GRACE exactly as the host
// (gleann pkg/gleann pluginStopGraceValue) does: a positive Go duration
// ("30s", "500ms"). Empty, unparseable (including a bare number without a
// unit, which the host ignores too) and non-positive values give the default.
func stopGraceFromEnv(v string) time.Duration {
	if v == "" {
		return defaultStopGrace
	}
	if d, err := time.ParseDuration(v); err == nil && d > 0 {
		return d
	}
	log.Printf("[plugin-serve] ignoring GLEANN_PLUGIN_STOP_GRACE=%q (use a duration like 10s), using %s", v, defaultStopGrace)
	return defaultStopGrace
}

// shutdownDeadlines turns the host's stop grace into a drain deadline
// (grace - 1s: the 1s of slack is for releasing the ASR helper and returning;
// half the grace when it is shorter than 2s) and a hard-exit deadline at the
// grace itself, since the host SIGKILLs then and a later exit is dead code.
func shutdownDeadlines(grace time.Duration) (drain, hardExit time.Duration) {
	if grace <= 0 {
		grace = defaultStopGrace
	}
	drain = grace - time.Second
	if drain < grace/2 {
		drain = grace / 2
	}
	return drain, grace
}
