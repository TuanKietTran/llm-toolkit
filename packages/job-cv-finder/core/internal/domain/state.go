// Package domain holds the job-automation domain model: application workflow states and
// their legal transitions, canonical jobs, source capabilities and policy outcomes.
// Candidate and CV data are NOT modelled here as owned entities: Ruxt owns them and this
// package only references them by Ruxt id + revision (see CandidateSnapshot).
package domain

import "fmt"

// AppState is the durable state of one application workflow.
type AppState string

const (
	StateMatching          AppState = "MATCHING"           // waiting for intel.match
	StateMatched           AppState = "MATCHED"            // match persisted, selection pending
	StateNotSelected       AppState = "NOT_SELECTED"       // terminal: match below threshold or excluded
	StateSelected          AppState = "SELECTED"           // chosen for preparation
	StatePreparing         AppState = "PREPARING"          // waiting for intel.prepare
	StatePrepared          AppState = "PREPARED"           // documents + answers persisted
	StatePolicyCheck       AppState = "POLICY_CHECK"       // policy engine deciding executor
	StateExecuting         AppState = "EXECUTING"          // an executor attempt is in flight
	StateVerifying         AppState = "VERIFYING"          // executor returned; confirming externally
	StateSubmitted         AppState = "SUBMITTED"          // terminal success: receipt verified
	StateHandoffPending    AppState = "HANDOFF_PENDING"    // prepared for the candidate to submit on the platform themselves
	StateFailedRetryable   AppState = "FAILED_RETRYABLE"   // will be retried after backoff
	StateSubmissionUnknown AppState = "SUBMISSION_UNKNOWN" // outcome uncertain: reconcile before any retry
	StateFailedFinal       AppState = "FAILED_FINAL"       // terminal failure
	StateBlockedException  AppState = "BLOCKED_EXCEPTION"  // needs the candidate: missing info, restricted capability, ambiguity
)

// VERIFYING is reachable from most non-executing states because a confirmation email can
// prove the candidate submitted on the platform themselves (out-of-band evidence).
var transitions = map[AppState][]AppState{
	StateMatching:          {StateMatched, StateBlockedException, StateFailedFinal, StateVerifying},
	StateMatched:           {StateSelected, StateNotSelected, StateVerifying, StateMatching},
	StateNotSelected:       {StateSelected, StateVerifying, StateMatching}, // SELECTED only by explicit candidate override; MATCHING after job enrichment
	StateSelected:          {StatePreparing, StateVerifying},
	StatePreparing:         {StatePrepared, StateBlockedException, StateFailedFinal, StateVerifying},
	StatePrepared:          {StatePolicyCheck, StateVerifying},
	StatePolicyCheck:       {StateExecuting, StateHandoffPending, StateBlockedException, StateVerifying},
	StateExecuting:         {StateVerifying, StateFailedRetryable, StateSubmissionUnknown, StateFailedFinal, StateBlockedException},
	StateVerifying:         {StateSubmitted, StateSubmissionUnknown, StateFailedRetryable},
	StateHandoffPending:    {StateVerifying, StateBlockedException, StatePolicyCheck},
	StateFailedRetryable:   {StatePolicyCheck, StateFailedFinal, StateVerifying},
	StateSubmissionUnknown: {StateVerifying, StatePolicyCheck, StateFailedFinal, StateBlockedException},
	StateBlockedException:  {StateMatching, StateSelected, StatePreparing, StatePolicyCheck, StateHandoffPending, StateFailedFinal, StateVerifying},
	StateSubmitted:         {},
	StateFailedFinal:       {},
}

// CanTransition reports whether from -> to is a legal workflow edge.
func CanTransition(from, to AppState) bool {
	for _, s := range transitions[from] {
		if s == to {
			return true
		}
	}
	return false
}

// CheckTransition returns an error describing an illegal edge.
func CheckTransition(from, to AppState) error {
	if _, ok := transitions[from]; !ok {
		return fmt.Errorf("unknown state %q", from)
	}
	if !CanTransition(from, to) {
		return fmt.Errorf("illegal transition %s -> %s", from, to)
	}
	return nil
}

// Terminal reports whether no automatic step will ever move this state.
func (s AppState) Terminal() bool {
	return s == StateSubmitted || s == StateFailedFinal || s == StateNotSelected
}

// AllStates lists every state, for validation and UI filters.
func AllStates() []AppState {
	out := make([]AppState, 0, len(transitions))
	for _, s := range []AppState{StateMatching, StateMatched, StateNotSelected, StateSelected, StatePreparing, StatePrepared, StatePolicyCheck, StateExecuting, StateVerifying, StateSubmitted, StateHandoffPending, StateFailedRetryable, StateSubmissionUnknown, StateFailedFinal, StateBlockedException} {
		out = append(out, s)
	}
	return out
}

// DiscoveryState is the lifecycle of one discovery run.
type DiscoveryState string

const (
	DiscoveryScheduled   DiscoveryState = "SCHEDULED"
	DiscoveryDiscovering DiscoveryState = "DISCOVERING"
	DiscoveryCompleted   DiscoveryState = "COMPLETED"
	DiscoveryFailed      DiscoveryState = "FAILED"
)
