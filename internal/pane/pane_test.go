package pane

import (
	"strings"
	"testing"
)

func TestPaneBoundsAFloodingChildAndMarksWhatItDropped(t *testing.T) {
	flooded := New("run-1", "flooder", Options{ScrollbackLines: 50, ScrollbackBytes: 1 << 20, Columns: 200})
	var written strings.Builder
	for index := 0; index < 5000; index++ {
		written.WriteString("line ")
		written.WriteString(strings.Repeat("x", 10))
		written.WriteString("\n")
	}
	flooded.Consume(strings.NewReader(written.String()))

	snapshot := flooded.Snapshot()
	if len(snapshot.Lines) != 50 {
		t.Fatalf("pane kept %d lines, want the 50-line cap", len(snapshot.Lines))
	}
	if snapshot.Marker == "" {
		t.Fatalf("pane dropped lines without marking the truncation")
	}
	if snapshot.Bytes != int64(written.Len()) {
		t.Fatalf("pane consumed %d bytes, want %d — a bounded pane must still READ everything", snapshot.Bytes, written.Len())
	}
}

func TestPaneSnapshotShowsTheLineTheChildIsStillWriting(t *testing.T) {
	watched := New("run-2", "watched", Options{})
	watched.Consume(strings.NewReader("finished\nstill going"))
	snapshot := watched.Snapshot()
	if len(snapshot.Lines) != 2 || snapshot.Lines[1] != "still going" {
		t.Fatalf("snapshot lines are %q, want the completed line and the partial row", snapshot.Lines)
	}
}

func TestPaneCountsTheDecodeErrorsItRecoveredFrom(t *testing.T) {
	noisy := New("run-3", "noisy", Options{})
	noisy.Consume(strings.NewReader("good\n\x80bad\ngood again\n"))
	snapshot := noisy.Snapshot()
	if snapshot.Resets != 1 {
		t.Fatalf("pane reported %d decode resets, want 1", snapshot.Resets)
	}
	if snapshot.Lines[len(snapshot.Lines)-1] != "good again" {
		t.Fatalf("pane did not resume after the decode error: %q", snapshot.Lines)
	}
}

func TestPaneResizeRewidthsTheDecoderWithNoChildAttached(t *testing.T) {
	resized := New("run-4", "resized", Options{Columns: 4})
	if err := resized.Resize(Size{Rows: 10, Columns: 10}); err != nil {
		t.Fatalf("resize: %v", err)
	}
	resized.Consume(strings.NewReader("abcdefgh"))
	snapshot := resized.Snapshot()
	if len(snapshot.Lines) != 1 || snapshot.Lines[0] != "abcdefgh" {
		t.Fatalf("lines are %q — the decoder is still wrapping at the old width", snapshot.Lines)
	}
}
