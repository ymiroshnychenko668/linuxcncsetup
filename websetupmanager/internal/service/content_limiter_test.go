//go:build linux

package service

import (
	"context"
	"testing"
	"time"
)

func TestRangePreviewIsIndependentFromSaturatedHeavyJobs(t *testing.T) {
	h := newLifecycleTestHarness(t, func(options *Options) { options.MaxParallelHeavyJobs = 1 })
	setup := h.createSetup(t, "Interactive preview", "content-limiter-create")
	artifactID, object, _ := h.attachProgram(t, setup.ID, "main.ngc", []byte("G90\nM2\n"), true)

	release, err := h.service.acquireHeavy(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()
	block, err := h.service.ReadArtifactRange(ctx, setup.ID, artifactID, object.Version, 0, 4)
	if err != nil {
		t.Fatalf("preview was starved by heavy work: %v", err)
	}
	if string(block.Data) != "G90\n" {
		t.Fatalf("preview block = %q", block.Data)
	}
}
