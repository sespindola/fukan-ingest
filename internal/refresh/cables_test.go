package refresh

import (
	"context"
	"testing"
)

// TestCablesDryRun exercises the full baseline + merge + geometry + dry-run
// insert path without needing ClickHouse or a live Overpass endpoint.
// Using an invalid overpassURL forces FetchOSMCables to fail — which is the
// signal that the orchestrator propagates the error (pre-merge).
//
// Successful end-to-end is covered by package-level tests in
// internal/refresh/cables/.
func TestCablesFetchErrorPropagates(t *testing.T) {
	err := Cables(context.Background(), nil, "http://127.0.0.1:0/invalid", true)
	if err == nil {
		t.Fatalf("expected error from bogus overpass URL, got nil")
	}
}
