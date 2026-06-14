package gateway

import (
	"testing"
	"time"
)

func TestEventRingPushAndSnapshotNewestFirst(t *testing.T) {
	ring := NewEventRing(5)

	baseTime := time.Date(2026, 6, 13, 12, 0, 0, 0, time.UTC)
	for index := 0; index < 3; index++ {
		ring.Push(Event{
			OccurredAt:  baseTime.Add(time.Duration(index) * time.Second),
			ServiceName: "users",
			Kind:        EventKindServiceDown,
		})
	}

	snapshot := ring.Snapshot()
	if len(snapshot) != 3 {
		t.Fatalf("expected 3 events, got %d", len(snapshot))
	}
	if !snapshot[0].OccurredAt.After(snapshot[1].OccurredAt) {
		t.Fatalf("expected newest-first order, got %v then %v", snapshot[0].OccurredAt, snapshot[1].OccurredAt)
	}
}

func TestEventRingWrapsAroundAtCapacity(t *testing.T) {
	ring := NewEventRing(3)
	for index := 0; index < 7; index++ {
		ring.Push(Event{
			OccurredAt:  time.Unix(int64(index), 0).UTC(),
			ServiceName: "products-primary",
			Kind:        EventKindServiceDown,
		})
	}
	snapshot := ring.Snapshot()
	if len(snapshot) != 3 {
		t.Fatalf("expected snapshot capped at 3, got %d", len(snapshot))
	}
	// Mais novo primeiro: timestamps 6, 5, 4.
	if snapshot[0].OccurredAt.Unix() != 6 {
		t.Fatalf("expected newest event timestamp to be 6, got %d", snapshot[0].OccurredAt.Unix())
	}
	if snapshot[2].OccurredAt.Unix() != 4 {
		t.Fatalf("expected oldest retained event timestamp to be 4, got %d", snapshot[2].OccurredAt.Unix())
	}
}

func TestEventRingSnapshotEmpty(t *testing.T) {
	ring := NewEventRing(5)
	if snapshot := ring.Snapshot(); snapshot != nil {
		t.Fatalf("expected nil snapshot when empty, got %v", snapshot)
	}
}
