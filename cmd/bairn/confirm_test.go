package main

import (
	"context"
	"path/filepath"
	"testing"

	"gitlab.com/dunn.dev/bairn/internal/sink"
)

// plainSink is a sink that cannot confirm copies, like the disk sink.
type plainSink struct{}

func (plainSink) Put(context.Context, sink.PutInput) (sink.Receipt, error) {
	return sink.Receipt{}, nil
}
func (plainSink) Name() string { return "plain" }

// TestVerifyAndPruneNeedAConfirmer pins that without a sink that can
// confirm, verify and prune exit nonzero before touching state or files.
func TestVerifyAndPruneNeedAConfirmer(t *testing.T) {
	var _ sink.Sink = plainSink{}
	if _, ok := any(&sink.Disk{}).(sink.Confirmer); ok {
		t.Fatal("the disk sink must not be a Confirmer")
	}
	cmds := map[string]func(sink.Sink, *pEnv) int{
		"verify": func(s sink.Sink, e *pEnv) int {
			return runVerifyOn(context.Background(), s, e.cfg(""), quiet, nil)
		},
		"prune": func(s sink.Sink, e *pEnv) int {
			return runPruneOn(context.Background(), s, e.cfg(""), quiet, []string{"--apply"})
		},
	}
	for name, run := range cmds {
		for label, s := range map[string]sink.Sink{"nil": nil, "plain": plainSink{}} {
			t.Run(name+"/"+label, func(t *testing.T) {
				e := decisionEnv(t)
				dirs := []string{e.root, filepath.Dir(e.state)}
				before := snapshot(t, dirs...)
				if code := run(s, e); code == 0 {
					t.Fatal("exit 0, want nonzero")
				}
				if after := snapshot(t, dirs...); !mapsEqual(before, after) {
					t.Fatalf("tree changed: before %d paths, after %d", len(before), len(after))
				}
			})
		}
	}
}
