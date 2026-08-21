package redis

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/sespindola/fukan-ingest/internal/coord"
	"github.com/sespindola/fukan-ingest/internal/model"
)

func TestBuildTelemetryEnvelopesBatchesAndEncodesH3AsHex(t *testing.T) {
	cell, err := coord.ComputeH3(51.5074, -0.1278)
	if err != nil {
		t.Fatal(err)
	}
	events := []model.FukanEvent{
		{Timestamp: 10, AssetID: "a", AssetType: model.AssetAircraft, Lat: 515074000, Lon: -1278000, H3Cell: cell, Source: "test"},
		{Timestamp: 11, AssetID: "b", AssetType: model.AssetAircraft, Lat: 515074001, Lon: -1278001, H3Cell: cell, Source: "test"},
	}

	envelopes, err := buildTelemetryEnvelopes(events, 1234)
	if err != nil {
		t.Fatal(err)
	}
	if len(envelopes) != len(telemetryResolutions) {
		t.Fatalf("got %d envelopes, want %d", len(envelopes), len(telemetryResolutions))
	}

	var batch struct {
		Type   string `json:"type"`
		V      int    `json:"v"`
		SentAt int64  `json:"sent_at"`
		Events []struct {
			ID string `json:"id"`
			H3 string `json:"h3"`
		} `json:"events"`
	}
	if err := json.Unmarshal([]byte(envelopes[0].Data), &batch); err != nil {
		t.Fatal(err)
	}
	if batch.Type != "delta_batch" || batch.V != 1 || batch.SentAt != 1234 {
		t.Fatalf("unexpected batch header: %+v", batch)
	}
	if len(batch.Events) != 2 {
		t.Fatalf("got %d events, want 2", len(batch.Events))
	}
	if batch.Events[0].H3 == "" {
		t.Fatal("h3 was empty")
	}
	if !strings.Contains(envelopes[0].Data, `"h3":"`) {
		t.Fatalf("wire payload did not encode h3 as a string: %s", envelopes[0].Data)
	}
}

func TestChunkLiveEventsHonorsEventLimit(t *testing.T) {
	events := make([]json.RawMessage, maxEventsPerStreamBatch+1)
	for i := range events {
		events[i] = json.RawMessage(`{"id":"asset"}`)
	}
	chunks, err := chunkLiveEvents(events, 1)
	if err != nil {
		t.Fatal(err)
	}
	if len(chunks) != 2 {
		t.Fatalf("got %d chunks, want 2", len(chunks))
	}
	for _, chunk := range chunks {
		if len(chunk) > maxStreamBatchBytes {
			t.Fatalf("chunk is %d bytes, max is %d", len(chunk), maxStreamBatchBytes)
		}
	}
}

func TestBatchingCollapsesSameCellPublicationCount(t *testing.T) {
	cell, err := coord.ComputeH3(40.7128, -74.0060)
	if err != nil {
		t.Fatal(err)
	}
	events := make([]model.FukanEvent, 500)
	for i := range events {
		events[i] = model.FukanEvent{
			Timestamp: int64(i + 1), AssetID: string(rune(i + 1)),
			AssetType: model.AssetAircraft, Lat: 407128000, Lon: -740060000,
			H3Cell: cell, Source: "load-test",
		}
	}

	envelopes, err := buildTelemetryEnvelopes(events, 1)
	if err != nil {
		t.Fatal(err)
	}
	legacyCount := len(events) * len(telemetryResolutions)
	if len(envelopes) > legacyCount/10 {
		t.Fatalf("got %d publications, expected at least a 90%% reduction from legacy %d",
			len(envelopes), legacyCount)
	}
}

func BenchmarkBuildTelemetryEnvelopes500Assets(b *testing.B) {
	cell, err := coord.ComputeH3(40.7128, -74.0060)
	if err != nil {
		b.Fatal(err)
	}
	events := make([]model.FukanEvent, 500)
	for i := range events {
		events[i] = model.FukanEvent{
			Timestamp: int64(i + 1), AssetID: string(rune(i + 1)),
			AssetType: model.AssetAircraft, Lat: 407128000, Lon: -740060000,
			H3Cell: cell, Source: "benchmark",
		}
	}
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, err := buildTelemetryEnvelopes(events, 1); err != nil {
			b.Fatal(err)
		}
	}
}
