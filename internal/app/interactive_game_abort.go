package app

import (
	"fmt"
	"strings"

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
