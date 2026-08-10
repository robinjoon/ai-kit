package autocheckpoint

import (
	"errors"
	"fmt"
	"time"

	"github.com/robinjoon/ai-kit/cli/internal/core"
	"github.com/robinjoon/ai-kit/cli/internal/sessionlog"
)

var ErrNoNewEvents = errors.New("no new session log events")

type Evidence struct {
	Previous   core.CheckpointInput `json:"previous"`
	Events     []sessionlog.Event   `json:"events"`
	CurrentGit core.GitState        `json:"current_git"`
}

type Collector interface {
	Collect(worktree string, since time.Time) ([]sessionlog.Event, error)
}

type Reconstructor interface {
	Reconstruct(Evidence) (core.CheckpointInput, error)
}

type Service struct {
	contexts      *core.Service
	logs          Collector
	reconstructor Reconstructor
}

func New(contexts *core.Service, logs Collector, reconstructor Reconstructor) *Service {
	return &Service{contexts: contexts, logs: logs, reconstructor: reconstructor}
}

func (s *Service) Capture(cwd string) (core.Checkpoint, error) {
	state, err := s.contexts.Status(cwd)
	if err != nil {
		return core.Checkpoint{}, err
	}
	events, err := s.logs.Collect(state.Scope.WorktreeRoot, state.Latest.CreatedAt)
	if err != nil {
		return core.Checkpoint{}, fmt.Errorf("collect session logs: %w", err)
	}
	if len(events) == 0 {
		return core.Checkpoint{}, ErrNoNewEvents
	}
	input, err := s.reconstructor.Reconstruct(Evidence{
		Previous:   state.Latest.Context,
		Events:     events,
		CurrentGit: state.CurrentGit,
	})
	if err != nil {
		return core.Checkpoint{}, fmt.Errorf("reconstruct checkpoint: %w", err)
	}
	return s.contexts.Checkpoint(cwd, "auto", input)
}
