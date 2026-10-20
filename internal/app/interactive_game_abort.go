package app

import (
	"context"
	"fmt"
	"strings"

	agentrun "denova/internal/agents/run"
	"denova/internal/interactive"
)

// settleAbortedGameInput clears only the accepted input belonging to the
// finished external command. Disconnects and suspended runs must not call it.
func settleAbortedGameInput(store *interactive.Store, storyID, branchID, commandID string) error {
	if store == nil { return fmt.Errorf("game store is unavailable") }
	snapshot, err := store.Snapshot(storyID, branchID)
	if err != nil { return err }
	for _, input := range snapshot.PendingPlayerInputs {
		if input.AgentCommandID != strings.TrimSpace(commandID) || !strings.HasPrefix(input.AgentOperationID, "external-game-") { continue }
		if _, err := store.AbandonPendingPlayerInput(storyID, branchID, input.ID); err != nil { return err }
	}
	return nil
}

// reconcileAbortedGameInputs closes the narrow crash window between an
// explicitly persisted controller abort and the Game abandonment journal row.
// Non-aborted interruptions remain intact for safe draft-preserving resume.
func (a *App) reconcileAbortedGameInputs(ctx context.Context, store *interactive.Store, options agentrun.Options) error {
	if store == nil { return ErrNoWorkspace }
	snapshot, err := store.Snapshot(options.StoryID, options.BranchID)
	if err != nil { return err }
	unfinished := false
	for _, input := range snapshot.PendingPlayerInputs {
		if strings.HasPrefix(input.AgentOperationID, "external-game-") { unfinished = true; break }
	}
	if !unfinished { return nil }
	control, selected, err := a.externalController(options)
	if err != nil { return err }
	if !selected { return nil }
	for _, input := range snapshot.PendingPlayerInputs {
		if !strings.HasPrefix(input.AgentOperationID, "external-game-") { continue }
		aborted, err := control.AbortedCommand(ctx, input.AgentCommandID)
		if err != nil { return err }
		if aborted {
			if _, err := store.AbandonPendingPlayerInput(options.StoryID, options.BranchID, input.ID); err != nil { return err }
		}
	}
	return nil
}
