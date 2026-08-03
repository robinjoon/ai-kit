package app

import (
	"errors"
	"reflect"
	"testing"
)

func checkpoint(id string, parents ...string) Record {
	values := make([]any, len(parents))
	for i, parent := range parents {
		values[i] = parent
	}
	return Record{"checkpoint_id": id, "parent_ids": values}
}

func TestCheckpointHeadsPreservesBranches(t *testing.T) {
	records := []Record{checkpoint("base"), checkpoint("left", "base"), checkpoint("right", "base")}
	got, err := CheckpointHeads(records)
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"left", "right"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("heads = %v, want %v", got, want)
	}
}

func TestDefaultParentsRejectsAmbiguousNormalCheckpoint(t *testing.T) {
	_, err := DefaultParents([]Record{checkpoint("left"), checkpoint("right")}, "progress", nil)
	if !errors.Is(err, ErrAmbiguous) {
		t.Fatalf("error = %v, want ambiguity", err)
	}
}

func TestDefaultParentsAcceptsOnlyCurrentMergeHeads(t *testing.T) {
	records := []Record{checkpoint("base"), checkpoint("left", "base"), checkpoint("right", "base")}
	got, err := DefaultParents(records, "merge", []string{"left", "right"})
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"left", "right"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("parents = %v, want %v", got, want)
	}
	if _, err := DefaultParents(records, "merge", []string{"base", "left"}); !errors.Is(err, ErrValidation) {
		t.Fatalf("error = %v, want validation", err)
	}
}

func TestStableCheckpointHeadsKeepsStableAncestorOfDraft(t *testing.T) {
	stable := checkpoint("stable")
	stable["stability"] = "stable"
	draft := checkpoint("draft", "stable")
	draft["stability"] = "draft"
	got, err := StableCheckpointHeads([]Record{stable, draft})
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"stable"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("stable heads = %v, want %v", got, want)
	}
}

func TestStableCheckpointHeadsTracksStableBranchesThroughDrafts(t *testing.T) {
	base := checkpoint("base")
	base["stability"] = "stable"
	draft := checkpoint("draft", "base")
	draft["stability"] = "draft"
	left := checkpoint("left", "draft")
	left["stability"] = "stable"
	right := checkpoint("right", "base")
	right["stability"] = "stable"
	got, err := StableCheckpointHeads([]Record{base, draft, left, right})
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"left", "right"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("stable heads = %v, want %v", got, want)
	}
}
