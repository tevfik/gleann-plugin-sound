// Package plugin implements the HashiCorp go-plugin gRPC server that allows
// the main gleann application to communicate with gleann-sound over a local
// Unix socket or TCP port.
//
// The main gleann binary acts as the plugin HOST; gleann-sound acts as the
// plugin SERVER.  Communication uses gRPC for streaming transcription events.
package plugin

import (
	"context"
	"fmt"
	"log"
	"net"
	"sync"
	"time"

	"github.com/tevfik/gleann-sound/internal/core"
	"google.golang.org/grpc"
)

// ---------------------------------------------------------------------------
// Protobuf-like types (hand-rolled to avoid a proto compilation step for now)
// ---------------------------------------------------------------------------
//
// In a production setup you would generate these from a .proto file.  For the
// initial iteration we define simple types and a hand-wired gRPC service to
// keep the dependency surface small.

// SegmentProto mirrors core.Segment for wire transport.
type SegmentProto struct {
	StartMs int64  `json:"start_ms"`
	EndMs   int64  `json:"end_ms"`
	Text    string `json:"text"`
}

// ---------------------------------------------------------------------------
// GRPCServer
// ---------------------------------------------------------------------------

// GRPCServer manages the lifecycle of the gRPC listener and allows the host
// (main gleann app) to start/stop transcription and receive streaming events.
type GRPCServer struct {
	mu          sync.Mutex
	grpcServer  *grpc.Server
	capturer    core.AudioCapturer
	transcriber core.Transcriber
	listener    net.Listener
	onEvent     core.TranscriptionHandler
}

// NewGRPCServer creates a new plugin server wired to the given audio capturer
// and transcriber.
func NewGRPCServer(capturer core.AudioCapturer, transcriber core.Transcriber, handler core.TranscriptionHandler) *GRPCServer {
	return &GRPCServer{
		capturer:    capturer,
		transcriber: transcriber,
		onEvent:     handler,
	}
}

// Serve starts listening on the given address (e.g. "localhost:50051" or a
// Unix socket path prefixed with "unix://").
func (s *GRPCServer) Serve(addr string) error {
	s.mu.Lock()

	lis, err := net.Listen("tcp", addr)
	if err != nil {
		s.mu.Unlock()
		return fmt.Errorf("plugin: failed to listen on %s: %w", addr, err)
	}
	s.listener = lis

	s.grpcServer = grpc.NewServer()
	// TODO: Register the generated protobuf service here once the .proto
	// file is created and compiled:
	//   pb.RegisterGleannSoundServer(s.grpcServer, s)

	log.Printf("[plugin] gRPC server listening on %s", addr)

	// Release the lock BEFORE the blocking Serve call so that Stop() can
	// acquire it and call GracefulStop().
	srv := s.grpcServer
	s.mu.Unlock()

	return srv.Serve(lis)
}

// Stop gracefully shuts down the gRPC server.
func (s *GRPCServer) Stop() {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.grpcServer != nil {
		s.grpcServer.GracefulStop()
		log.Println("[plugin] gRPC server stopped")
	}
}

// StartTranscription begins live capture and streams transcription events to
// the registered handler.  This is the server-side implementation of the
// streaming RPC.
func (s *GRPCServer) StartTranscription(ctx context.Context) error {
	// Accumulation buffer — collects PCM chunks and periodically flushes
	// them through the transcriber.
	var (
		bufMu sync.Mutex
		buf   []int16
	)

	// Start audio capture; each chunk is appended to the buffer.
	err := s.capturer.Start(ctx, func(pcmData []int16) {
		bufMu.Lock()
		buf = append(buf, pcmData...)
		bufMu.Unlock()
	})
	if err != nil {
		return fmt.Errorf("plugin: failed to start audio capture: %w", err)
	}

	// Background goroutine that flushes the buffer every ~2 seconds.
	go func() {
		ticker := time.NewTicker(2 * time.Second)
		defer ticker.Stop()

		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				bufMu.Lock()
				if len(buf) == 0 {
					bufMu.Unlock()
					continue
				}
				chunk := make([]int16, len(buf))
				copy(chunk, buf)
				buf = buf[:0]
				bufMu.Unlock()

				segments, err := s.transcriber.TranscribeStreamSegments(ctx, chunk)
				if err != nil {
					log.Printf("[plugin] transcription error: %v", err)
					continue
				}
				if len(segments) > 0 && s.onEvent != nil {
					s.onEvent(core.TranscriptionEvent{
						Segments: segments,
						Final:    false,
					})
				}
			}
		}
	}()

	return nil
}

// ---------------------------------------------------------------------------
// Helpers for converting domain types to/from proto-like types
// ---------------------------------------------------------------------------

// SegmentToProto converts a domain Segment to the wire-format proto type.
func SegmentToProto(s core.Segment) SegmentProto {
	return SegmentProto{
		StartMs: s.Start.Milliseconds(),
		EndMs:   s.End.Milliseconds(),
		Text:    s.Text,
	}
}
