package main

import "testing"

// Every edge declared in the transition table must be accepted, and the two
// gate edges must be accepted only when the caller came through a gate.
func TestEveryDeclaredEdgeIsAllowed(t *testing.T) {
	for from, targets := range episodeTransitions {
		for _, to := range targets {
			viaGate := gateFor(from, to) != ""
			if err := validateTransition(from, to, viaGate); err != nil {
				t.Errorf("declared edge %s -> %s was refused: %v", from, to, err)
			}
		}
	}
}

func TestGateEdgesAreRefusedForAgents(t *testing.T) {
	gated := []struct {
		from, to EpisodeStatus
		gate     string
	}{
		{StatusScriptDraft, StatusScriptApproved, Gate1Script},
		{StatusQAReview, StatusPlatformAdapted, Gate2Release},
	}

	for _, tc := range gated {
		if got := gateFor(tc.from, tc.to); got != tc.gate {
			t.Errorf("gateFor(%s, %s) = %q, want %q", tc.from, tc.to, got, tc.gate)
		}
		// An agent calling the transition endpoint must be refused.
		if err := validateTransition(tc.from, tc.to, false); err == nil {
			t.Errorf("%s -> %s was allowed without a gate, expected refusal", tc.from, tc.to)
		}
		// The same move through the approvals endpoint must succeed.
		if err := validateTransition(tc.from, tc.to, true); err != nil {
			t.Errorf("%s -> %s was refused via its gate: %v", tc.from, tc.to, err)
		}
	}
}

func TestIllegalTransitionsAreRefused(t *testing.T) {
	cases := []struct {
		name     string
		from, to EpisodeStatus
	}{
		{"skip the whole pipeline", StatusIdeaBacklog, StatusPublished},
		{"skip production stages", StatusScriptApproved, StatusAssembled},
		{"publish without scheduling", StatusPlatformAdapted, StatusPublished},
		{"revive a cancelled episode", StatusCancelled, StatusScriptDraft},
		{"move on from a terminal stage", StatusAnalyzed, StatusPublished},
		{"go backwards from published", StatusPublished, StatusScheduled},
		{"jump back over a gate", StatusScheduled, StatusScriptDraft},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			// viaGate is true here to prove a gate does not widen the machine:
			// arriving through the approvals endpoint is not a way past the
			// transition table itself.
			if err := validateTransition(tc.from, tc.to, true); err == nil {
				t.Errorf("%s -> %s was allowed, expected refusal", tc.from, tc.to)
			}
		})
	}
}

func TestTransitionRejectsUnknownAndNoOpTargets(t *testing.T) {
	if err := validateTransition(StatusIdeaBacklog, EpisodeStatus("NOT_A_STAGE"), false); err == nil {
		t.Error("unknown target status was accepted")
	}
	if err := validateTransition(StatusScriptDraft, StatusScriptDraft, false); err == nil {
		t.Error("transition to the current status was accepted")
	}
}

// Cancelling must stay reachable from every active stage, otherwise an
// episode can get stuck with no way to retire it.
func TestCancelReachableFromEveryActiveStage(t *testing.T) {
	terminal := map[EpisodeStatus]bool{
		StatusAnalyzed:  true,
		StatusCancelled: true,
		StatusPublished: true,
	}

	for status := range episodeTransitions {
		if terminal[status] {
			continue
		}
		if !canTransition(status, StatusCancelled) {
			t.Errorf("%s cannot be cancelled", status)
		}
	}
}

// The happy path must actually connect end to end, so a walk from the first
// stage reaches the last one without a missing edge.
func TestHappyPathIsConnected(t *testing.T) {
	happyPath := []EpisodeStatus{
		StatusIdeaBacklog,
		StatusScriptDraft,
		StatusScriptApproved,
		StatusVOGenerated,
		StatusMusicGenerated,
		StatusAnimationRendered,
		StatusAssembled,
		StatusQAReview,
		StatusPlatformAdapted,
		StatusScheduled,
		StatusPublished,
		StatusAnalyzed,
	}

	for i := 0; i < len(happyPath)-1; i++ {
		from, to := happyPath[i], happyPath[i+1]
		if err := validateTransition(from, to, gateFor(from, to) != ""); err != nil {
			t.Fatalf("happy path breaks at %s -> %s: %v", from, to, err)
		}
	}
}

// A gate rejection has to land on a stage that can be worked again, or the
// episode is stranded.
func TestGateRejectionsReturnToAWorkableStage(t *testing.T) {
	for _, gate := range []string{Gate1Script, Gate2Release} {
		waiting := gateStatus(gate)
		if waiting == "" {
			t.Fatalf("gateStatus(%s) returned no waiting stage", gate)
		}

		approved, rejected, ok := gateTarget(gate)
		if !ok {
			t.Fatalf("gateTarget(%s) not found", gate)
		}
		if gateFor(waiting, approved) != gate {
			t.Errorf("%s approval target %s is not guarded by the gate", gate, approved)
		}
		// A rejection is an ordinary edge, so it must not need the gate.
		if err := validateTransition(waiting, rejected, false); err != nil {
			t.Errorf("%s rejection %s -> %s is not a legal move: %v", gate, waiting, rejected, err)
		}
		if len(episodeTransitions[rejected]) == 0 {
			t.Errorf("%s rejection strands the episode at terminal stage %s", gate, rejected)
		}
	}
}

func TestGateLookupsRejectUnknownGates(t *testing.T) {
	if _, _, ok := gateTarget("GATE_99_NONSENSE"); ok {
		t.Error("unknown gate was accepted by gateTarget")
	}
	if got := gateStatus("GATE_99_NONSENSE"); got != "" {
		t.Errorf("gateStatus of an unknown gate = %q, want empty", got)
	}
	if got := gateFor(StatusVOGenerated, StatusMusicGenerated); got != "" {
		t.Errorf("ungated edge reported gate %q", got)
	}
}

// The discovery endpoint is how an agent learns the flow, so every stage has
// to be described exactly once and carry its owner and layer.
func TestPipelineStagesDescribesEveryStage(t *testing.T) {
	stages := pipelineStages()
	if len(stages) != len(episodeTransitions) {
		t.Fatalf("pipelineStages returned %d stages, transition table has %d", len(stages), len(episodeTransitions))
	}

	seen := map[EpisodeStatus]bool{}
	for _, stage := range stages {
		if seen[stage.Status] {
			t.Errorf("stage %s described twice", stage.Status)
		}
		seen[stage.Status] = true

		if !isValidStatus(stage.Status) {
			t.Errorf("stage %s is not in the transition table", stage.Status)
		}
		if stage.Layer == "" {
			t.Errorf("stage %s has no layer", stage.Status)
		}
		if stage.NextStatuses == nil {
			t.Errorf("stage %s has nil next statuses, which serialises as null", stage.Status)
		}
	}

	for status := range episodeTransitions {
		if !seen[status] {
			t.Errorf("stage %s is missing from the discovery payload", status)
		}
	}
}

func TestStagesReportTheirGate(t *testing.T) {
	gates := map[EpisodeStatus]string{}
	for _, stage := range pipelineStages() {
		if stage.RequiresGate != "" {
			gates[stage.Status] = stage.RequiresGate
		}
	}

	want := map[EpisodeStatus]string{
		StatusScriptDraft: Gate1Script,
		StatusQAReview:    Gate2Release,
	}
	if len(gates) != len(want) {
		t.Errorf("found %d gated stages, want %d: %v", len(gates), len(want), gates)
	}
	for status, gate := range want {
		if gates[status] != gate {
			t.Errorf("stage %s reports gate %q, want %q", status, gates[status], gate)
		}
	}
}

// Every stage an agent can act on needs a named owner, or nothing knows to
// poll for it.
func TestActiveStagesHaveAnOwner(t *testing.T) {
	for status, targets := range episodeTransitions {
		if len(targets) == 0 {
			continue
		}
		if stageOwners[status] == "" {
			t.Errorf("stage %s has outgoing edges but no owner", status)
		}
	}
}
