package main

import (
	"fmt"
	"log"
	"os"
	"os/signal"
	"syscall"

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
				defer r.Close()
				srv.SetRefiner(r)
				log.Printf("[plugin-serve] /live refinement on (%s)", refineModel)
			}
			if err := srv.SetListen(host, token, maxLive); err != nil {
				return err
			}
			if cfg := config.Load(); cfg != nil {
				srv.SetCorrector(core.NewCorrector(cfg.Corrections))
			}
			defer srv.Close()

			// Graceful shutdown on SIGINT/SIGTERM.
			sigCh := make(chan os.Signal, 1)
			signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
			go func() {
				<-sigCh
				log.Println("[plugin-serve] shutting down...")
				srv.Close()
				os.Exit(0)
			}()

			return srv.Serve()
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
		"Access token required by the /live WebSocket (or env GLEANN_SOUND_TOKEN)")
	cmd.Flags().Bool("speakers", false,
		"Label /live utterances with their speaker (auto-on once voices are enrolled; --speakers=false disables)")
	cmd.Flags().String("refine-model", "",
		"GGUF model (Qwen3-ASR) that re-transcribes each /live utterance after it ends ({\"type\":\"refined\"}). Default: refine_model from sound.json; --refine-model=off disables")
	cmd.Flags().Int("max-live", httpserver.DefaultMaxLive, "Maximum concurrent /live sessions")
	cmd.Flags().String("language", "",
		"Language code for transcription (e.g. tr, en). Empty = auto-detect")

	return cmd
}
