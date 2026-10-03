package processor

import "testing"

func TestExamineRecoveryCandidateStopsCursorAtProbeBudget(t *testing.T) {
	last := ""
	probes := 0
	for i := 1; i <= maxMigrationRecoveryProbesPerTick+5; i++ {
		id := string(rune('a' + i - 1))
		if !examineRecoveryCandidate(&last, probes, maxMigrationRecoveryProbesPerTick, id) {
			break
		}
		probes++
	}
	if last != "j" {
		t.Fatalf("cursor after probe budget = %q, want %q", last, "j")
	}
}
