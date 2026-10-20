package interactive

import "testing"

func TestStoryTurnInterruptionPersistsForPendingPlayerInput(t *testing.T) {
	dir := t.TempDir()
	store := NewStore(dir)
	story, err := store.CreateStory(CreateStoryRequest{Title: "Paused story"})
	if err != nil {
		t.Fatal(err)
	}
	intent, err := NewPlayerInputIntent(DomainCommitIdentity{
		CommandID: "pause-command", OperationID: "pause-operation", Cycle: 1,
	}, "main", "Open the door")
	if err != nil {
		t.Fatal(err)
	}
	input, err := store.CommitPlayerInput(story.ID, intent)
	if err != nil {
		t.Fatal(err)
	}
	interruption, err := store.MarkTurnInterrupted(
		story.ID,
		"main",
		input.Event.ID,
		"Open the door",
		"The hinges groaned",
		"user_requested",
	)
	if err != nil {
		t.Fatal(err)
	}
	if interruption.PlayerInputID != input.Event.ID {
		t.Fatalf("interruption = %#v", interruption)
	}

	cold := NewStore(dir)
	pending, err := cold.PendingTurnInterruption(story.ID, "main")
	if err != nil {
		t.Fatal(err)
	}
	if pending == nil || pending.ID != interruption.ID || pending.AssistantContent != "The hinges groaned" {
		t.Fatalf("pending interruption after reload = %#v", pending)
	}
	if _, _, err := cold.AppendTurnWithState(story.ID, AppendTurnWithStateRequest{
		BranchID: "main", User: intent.Text, Narrative: "The door opened.",
		AgentCommandID: intent.Identity.CommandID, AgentOperationID: intent.Identity.OperationID, AgentCycle: intent.Identity.Cycle,
	}); err != nil {
		t.Fatal(err)
	}
	pending, err = cold.PendingTurnInterruption(story.ID, "main")
	if err != nil {
		t.Fatal(err)
	}
	if pending != nil {
		t.Fatalf("completed turn retained interruption = %#v", pending)
	}
}


func TestAbandonPendingTurnPreservesCommittedStateAndHistory(t *testing.T) {
	dir := t.TempDir()
	store := NewStore(dir)
	story, err := store.CreateStory(CreateStoryRequest{Title: "Safe abandonment"})
	if err != nil { t.Fatal(err) }
	intent, err := NewPlayerInputIntent(DomainCommitIdentity{CommandID:"first", OperationID:"external-game-first", Cycle:1}, "main", "Open the door")
	if err != nil { t.Fatal(err) }
	input, err := store.CommitPlayerInput(story.ID, intent)
	if err != nil { t.Fatal(err) }
	if _, err = store.MarkTurnInterrupted(story.ID,"main",input.Event.ID,intent.Text,"Partial prose","connection_lost"); err != nil { t.Fatal(err) }
	before, err := store.Snapshot(story.ID, "main")
	if err != nil { t.Fatal(err) }
	changed, err := store.AbandonPendingPlayerInput(story.ID,"main",input.Event.ID)
	if err != nil || !changed { t.Fatalf("abandon: %v changed=%t",err,changed) }
	cold := NewStore(dir)
	after, err := cold.Snapshot(story.ID, "main")
	if err != nil { t.Fatal(err) }
	if after.TurnCount != before.TurnCount || !reflect.DeepEqual(after.State,before.State) || len(after.PendingPlayerInputs)!=0 {
		t.Fatalf("abandon changed committed story: before=%+v after=%+v",before,after)
	}
	pending, err := cold.PendingTurnInterruption(story.ID,"main")
	if err != nil || pending != nil { t.Fatalf("stale interruption: %+v err=%v",pending,err) }
	changed, err = cold.AbandonPendingPlayerInput(story.ID,"main",input.Event.ID)
	if err != nil || changed { t.Fatalf("non-idempotent abort: changed=%t err=%v",changed,err) }
	second, err := NewPlayerInputIntent(DomainCommitIdentity{CommandID:"second",OperationID:"external-game-second",Cycle:1},"main","Look outside")
	if err != nil { t.Fatal(err) }
	accepted, err := cold.CommitPlayerInput(story.ID,second)
	if err != nil { t.Fatal(err) }
	if _, _, err := cold.AppendTurnWithState(story.ID,AppendTurnWithStateRequest{BranchID:"main",User:second.Text,Narrative:"The courtyard is quiet.",AgentCommandID:second.Identity.CommandID,AgentOperationID:second.Identity.OperationID,AgentCycle:second.Identity.Cycle}); err != nil { t.Fatal(err) }
	final,err := cold.Snapshot(story.ID,"main")
	if err != nil || final.TurnCount != 1 || final.CurrentTurn.PlayerInputID != accepted.Event.ID || len(final.PendingPlayerInputs)!=0 {
		t.Fatalf("new turn after abort was not committed: %+v err=%v",final,err)
	}
}
