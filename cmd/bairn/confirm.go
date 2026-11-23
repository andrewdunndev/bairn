package main

import (
	"fmt"
	"log/slog"
	"os"

	"gitlab.com/dunn.dev/bairn/api/immich"
	"gitlab.com/dunn.dev/bairn/internal/config"
	"gitlab.com/dunn.dev/bairn/internal/sink"
)

// configuredSink is the confirming sink the environment configures, or
// nil. Immich is the one that confirms today.
func configuredSink(cfg *config.Config, logger *slog.Logger) sink.Sink {
	if cfg.ImmichBaseURL == "" || cfg.ImmichAPIKey == "" {
		return nil
	}
	return sink.NewImmich(immich.New(cfg.ImmichBaseURL, cfg.ImmichAPIKey, immich.WithLogger(logger)))
}

// confirmerOf returns the sink's Confirmer, or prints one line and
// reports false. Callers check it before opening state.
func confirmerOf(cmd string, s sink.Sink) (sink.Confirmer, bool) {
	if cf, ok := s.(sink.Confirmer); ok {
		return cf, true
	}
	fmt.Fprintf(os.Stderr, "%s: verify/prune need a sink that can confirm copies; none is configured\n", cmd)
	return nil, false
}
