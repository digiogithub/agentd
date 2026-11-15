package main

import (
	"log/slog"
	"testing"

	"github.com/digiogithub/agentd/internal/desktopui/backend"
	"github.com/digiogithub/agentd/internal/desktopui/remote"
)

func TestMuxAdapters(t *testing.T) {
	list := muxAdapters(&remote.LocalShell{}, backend.LocalHostID)
	if len(list) != 2 || list[0].Source() != backend.SourceZellij || list[1].Source() != backend.SourceTmux {
		t.Fatalf("adapters %v", list)
	}
}

func TestLogLevel(t *testing.T) {
	if logLevel("") != slog.LevelWarn || logLevel("DEBUG") != slog.LevelDebug || logLevel("error") != slog.LevelError {
		t.Error("log level")
	}
}
