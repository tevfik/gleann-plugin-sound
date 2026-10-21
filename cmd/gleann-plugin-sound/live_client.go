package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"os/exec"
	"time"

	"github.com/gorilla/websocket"
	"github.com/spf13/cobra"
)

// newLiveClientCmd streams an audio file to a running 'plugin-serve' /live
// endpoint at real-time speed and prints the events. It doubles as a reference
// client for the protocol (see internal/httpserver/live.go) and a way to test
// a deployment from another machine.
func newLiveClientCmd() *cobra.Command {
	var serverURL, token, lang string
	var realtime bool

	cmd := &cobra.Command{
		Use:   "live-client <audio-file>",
		Short: "Stream a file to a plugin-serve /live endpoint (protocol test client)",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			u, err := url.Parse(serverURL)
			if err != nil {
				return err
			}
			u.Path = "/live"
			q := u.Query()
			if token != "" {
				q.Set("token", token)
			}
			if cmd.Flags().Changed("language") {
				q.Set("lang", lang)
			}
			u.RawQuery = q.Encode()

			raw, err := exec.CommandContext(cmd.Context(), "ffmpeg", "-v", "error", "-i", args[0],
				"-ar", "16000", "-ac", "1", "-f", "s16le", "-").Output()
			if err != nil {
				return fmt.Errorf("ffmpeg: %w", err)
			}

			conn, resp, err := websocket.DefaultDialer.DialContext(cmd.Context(), u.String(), nil)
			if err != nil {
				if resp != nil {
					return fmt.Errorf("connect: %w (HTTP %d)", err, resp.StatusCode)
				}
				return fmt.Errorf("connect: %w", err)
			}
			defer conn.Close()

			ctx, cancel := context.WithCancel(cmd.Context())
			defer cancel()
			done := make(chan error, 1)
			start := time.Now()
			go func() {
				enc := json.NewEncoder(os.Stdout)
				for {
					var m map[string]any
					if err := conn.ReadJSON(&m); err != nil {
						done <- err
						return
					}
					m["t"] = float64(time.Since(start).Milliseconds()) / 1000 // wall clock, for latency
					_ = enc.Encode(m)
					if m["type"] == "done" || m["type"] == "error" {
						done <- nil
						return
					}
				}
			}()

			// 100 ms frames, paced like a microphone.
			const frame = 1600 * 2
			tick := time.NewTicker(100 * time.Millisecond)
			defer tick.Stop()
			for off := 0; off < len(raw); off += frame {
				end := min(off+frame, len(raw))
				if err := conn.WriteMessage(websocket.BinaryMessage, raw[off:end]); err != nil {
					return err
				}
				if realtime {
					select {
					case <-tick.C:
					case <-ctx.Done():
						return ctx.Err()
					}
				}
			}
			// Trailing silence lets the server close the last utterance by itself;
			// "end" then flushes whatever is left.
			_ = conn.WriteMessage(websocket.BinaryMessage, make([]byte, 2*16000))
			_ = conn.WriteJSON(map[string]string{"type": "end"})

			select {
			case err := <-done:
				if websocket.IsCloseError(err, websocket.CloseNormalClosure) {
					err = nil
				}
				return err
			case <-time.After(60*time.Second + time.Duration(len(raw)/2/16000)*time.Second):
				return fmt.Errorf("timed out waiting for the server to finish")
			}
		},
	}
	cmd.Flags().StringVar(&serverURL, "url", "ws://127.0.0.1:8767", "plugin-serve address")
	cmd.Flags().StringVar(&token, "token", os.Getenv("GLEANN_SOUND_TOKEN"), "access token")
	cmd.Flags().StringVar(&lang, "language", "", "language code for this session (default: the server's)")
	cmd.Flags().BoolVar(&realtime, "realtime", true, "pace the upload like a microphone (false = as fast as possible)")
	return cmd
}
