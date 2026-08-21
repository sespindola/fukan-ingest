package batcher

import (
	"testing"

	"github.com/sespindola/fukan-ingest/internal/model"
)

func TestUpsertLatestKeepsNewestTimestamp(t *testing.T) {
	pending := make(map[string]model.FukanEvent)
	newest := model.FukanEvent{AssetType: model.AssetAircraft, AssetID: "abc", Timestamp: 20}
	upsertLatest(pending, newest)
	upsertLatest(pending, model.FukanEvent{AssetType: model.AssetAircraft, AssetID: "abc", Timestamp: 10})

	if len(pending) != 1 {
		t.Fatalf("got %d entries, want 1", len(pending))
	}
	for _, event := range pending {
		if event.Timestamp != newest.Timestamp {
			t.Fatalf("kept timestamp %d, want %d", event.Timestamp, newest.Timestamp)
		}
	}
}
