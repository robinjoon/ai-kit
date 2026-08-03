// Package model contains the small derived records stored beside canonical
// checkpoint JSON. Checkpoint, snapshot, and handoff bodies remain untyped JSON
// at this boundary because their schemas are the compatibility contract.
package model

import "time"

const SchemaVersion = 1

// TaskManifest combines durable task identity (title and aliases) with status
// fields derived from immutable checkpoints. A missing manifest can recover a
// minimal task ID, but its complete identity cannot be reconstructed.
type TaskManifest struct {
	SchemaVersion     int               `yaml:"schema_version"`
	TaskID            string            `yaml:"task_id"`
	Title             string            `yaml:"title"`
	Status            string            `yaml:"status"`
	Aliases           []string          `yaml:"aliases,omitempty"`
	IdentityRecovered bool              `yaml:"identity_recovered,omitempty"`
	CheckpointIDs     []string          `yaml:"checkpoint_ids,omitempty"`
	HeadIDs           []string          `yaml:"head_ids,omitempty"`
	StableHeadIDs     []string          `yaml:"stable_head_ids,omitempty"`
	StableHeadID      string            `yaml:"stable_head_id,omitempty"`
	HeadStatuses      map[string]string `yaml:"head_statuses,omitempty"`
	CreatedAt         time.Time         `yaml:"created_at"`
	LastUsedAt        time.Time         `yaml:"last_used_at,omitempty"`
}

// Binding is device-local state linking one client application to its active task.
// It is intentionally separate from the canonical checkpoint records.
type Binding struct {
	SchemaVersion int       `yaml:"schema_version"`
	RepoID        string    `yaml:"repo_id"`
	TaskID        string    `yaml:"task_id"`
	Client        string    `yaml:"client"`
	BoundAt       time.Time `yaml:"bound_at"`
}

// Repository is the small local mapping persisted in repo.yaml.
type Repository struct {
	SchemaVersion   int               `yaml:"schema_version"`
	RepoID          string            `yaml:"repo_id"`
	CanonicalRemote string            `yaml:"canonical_remote,omitempty"`
	Aliases         []string          `yaml:"aliases,omitempty"`
	WorkingCopies   map[string]string `yaml:"working_copies,omitempty"`
}
