// Package app contains the use-cases behind the ctx command.
package app

import (
	"errors"
	"fmt"
	"sort"
)

var (
	ErrNotFound   = errors.New("ctx record not found")
	ErrAmbiguous  = errors.New("ctx selection is ambiguous")
	ErrValidation = errors.New("ctx validation failed")
)

// Record is the schema-shaped representation used at the CLI boundary.
// The foundation package has the same map representation so no information is
// lost while assembling or rendering records.
type Record = map[string]any

// CheckpointHeads returns all checkpoint IDs not referenced by another record
// in the same task. The result is sorted so commands never hide ambiguity
// behind filesystem order.
func CheckpointHeads(records []Record) ([]string, error) {
	all := make(map[string]struct{}, len(records))
	parents := make(map[string]struct{})
	for _, record := range records {
		id, err := recordString(record, "checkpoint_id")
		if err != nil {
			return nil, err
		}
		if _, duplicate := all[id]; duplicate {
			return nil, fmt.Errorf("%w: duplicate checkpoint ID %q", ErrValidation, id)
		}
		all[id] = struct{}{}
		for _, parent := range stringSlice(record["parent_ids"]) {
			parents[parent] = struct{}{}
		}
	}
	heads := make([]string, 0, len(all))
	for id := range all {
		if _, isParent := parents[id]; !isParent {
			heads = append(heads, id)
		}
	}
	sort.Strings(heads)
	return heads, nil
}

// StableCheckpointHeads returns the newest stable checkpoints in the stable
// subgraph. Draft descendants do not hide their last stable ancestor, while a
// later stable descendant does.
func StableCheckpointHeads(records []Record) ([]string, error) {
	byID := make(map[string]Record, len(records))
	stable := make(map[string]struct{})
	for _, record := range records {
		id, err := recordString(record, "checkpoint_id")
		if err != nil {
			return nil, err
		}
		if _, duplicate := byID[id]; duplicate {
			return nil, fmt.Errorf("%w: duplicate checkpoint ID %q", ErrValidation, id)
		}
		byID[id] = record
		if record["stability"] == "stable" {
			stable[id] = struct{}{}
		}
	}
	hidden := make(map[string]struct{})
	for descendant := range stable {
		stack := append([]string(nil), stringSlice(byID[descendant]["parent_ids"])...)
		seen := make(map[string]struct{})
		for len(stack) > 0 {
			last := len(stack) - 1
			id := stack[last]
			stack = stack[:last]
			if _, visited := seen[id]; visited {
				continue
			}
			seen[id] = struct{}{}
			parent, exists := byID[id]
			if !exists {
				return nil, fmt.Errorf("%w: parent checkpoint %q does not exist", ErrValidation, id)
			}
			if _, isStable := stable[id]; isStable {
				hidden[id] = struct{}{}
			}
			stack = append(stack, stringSlice(parent["parent_ids"])...)
		}
	}
	heads := make([]string, 0, len(stable))
	for id := range stable {
		if _, isHidden := hidden[id]; !isHidden {
			heads = append(heads, id)
		}
	}
	sort.Strings(heads)
	return heads, nil
}

// DefaultParents applies the v1 parent selection rule. A normal checkpoint
// follows the unique head. A merge only accepts explicit, distinct current
// heads. No command is allowed to guess among multiple heads.
func DefaultParents(records []Record, purpose string, explicit []string) ([]string, error) {
	heads, err := CheckpointHeads(records)
	if err != nil {
		return nil, err
	}
	headSet := make(map[string]struct{}, len(heads))
	for _, id := range heads {
		headSet[id] = struct{}{}
	}
	if purpose == "merge" {
		if len(explicit) < 2 {
			return nil, fmt.Errorf("%w: merge checkpoint needs at least two parents", ErrValidation)
		}
		seen := make(map[string]struct{}, len(explicit))
		for _, id := range explicit {
			if _, duplicate := seen[id]; duplicate {
				return nil, fmt.Errorf("%w: duplicate merge parent %q", ErrValidation, id)
			}
			seen[id] = struct{}{}
			if _, isHead := headSet[id]; !isHead {
				return nil, fmt.Errorf("%w: merge parent %q is not a current head", ErrValidation, id)
			}
		}
		return append([]string(nil), explicit...), nil
	}
	if len(explicit) > 1 {
		return nil, fmt.Errorf("%w: a non-merge checkpoint has at most one parent", ErrValidation)
	}
	if len(explicit) == 1 {
		if _, exists := allCheckpointIDs(records)[explicit[0]]; !exists {
			return nil, fmt.Errorf("%w: parent %q does not exist", ErrValidation, explicit[0])
		}
		return append([]string(nil), explicit...), nil
	}
	switch len(heads) {
	case 0:
		return nil, nil
	case 1:
		return []string{heads[0]}, nil
	default:
		return nil, fmt.Errorf("%w: checkpoint heads %v; provide --parent", ErrAmbiguous, heads)
	}
}

func allCheckpointIDs(records []Record) map[string]struct{} {
	ids := make(map[string]struct{}, len(records))
	for _, record := range records {
		if id, ok := record["checkpoint_id"].(string); ok && id != "" {
			ids[id] = struct{}{}
		}
	}
	return ids
}

func recordString(record Record, key string) (string, error) {
	value, ok := record[key].(string)
	if !ok || value == "" {
		return "", fmt.Errorf("%w: record field %q is required", ErrValidation, key)
	}
	return value, nil
}

func stringSlice(value any) []string {
	items, ok := value.([]any)
	if !ok {
		items2, ok := value.([]string)
		if !ok {
			return nil
		}
		return items2
	}
	result := make([]string, 0, len(items))
	for _, item := range items {
		if value, ok := item.(string); ok {
			result = append(result, value)
		}
	}
	return result
}
