package models

import "testing"

func TestCanTransition_UnderReviewIsReachable(t *testing.T) {

	for _, from := range []Status{StatusPending, StatusProcessing} {
		if !CanTransition(from, StatusUnderReview) {
			t.Errorf("%s -> under_review must be legal: it is where a payout with an unknown outcome parks", from)
		}
	}
}

func TestCanTransition_UnderReviewNeverRefunds(t *testing.T) {

	if CanTransition(StatusUnderReview, StatusRefunded) {
		t.Fatal("under_review -> refunded must be ILLEGAL: a review resolves by finding out what happened, never by assuming the money never left")
	}
	for _, to := range []Status{StatusProcessing, StatusCompleted, StatusFailed, StatusCancelled} {
		if !CanTransition(StatusUnderReview, to) {
			t.Errorf("under_review -> %s must be legal: it is a way a review can be resolved by evidence", to)
		}
	}
}

func TestCanTransition_CompletedMayReverseOrRefund(t *testing.T) {

	for _, to := range []Status{StatusReversed, StatusRefunded} {
		if !CanTransition(StatusCompleted, to) {
			t.Errorf("completed -> %s must be legal", to)
		}
	}
	// But a completed payment cannot silently go back into flight.
	if CanTransition(StatusCompleted, StatusProcessing) {
		t.Fatal("completed -> processing must be illegal: a settled payment does not re-enter the pipeline")
	}
}

func TestCanTransition_TerminalStatesHaveNoExits(t *testing.T) {
	// These are the end of the line. Nothing may move out of them, or the
	// append-only history of a money movement could be rewritten.
	terminal := []Status{StatusFailed, StatusCancelled, StatusReversed, StatusRefunded}
	for _, from := range terminal {
		if !IsTerminal(from) {
			t.Errorf("%s must be terminal (no outgoing transitions)", from)
		}
		for _, to := range []Status{StatusPending, StatusProcessing, StatusCompleted, StatusUnderReview} {
			if CanTransition(from, to) {
				t.Errorf("%s -> %s must be illegal: %s is terminal", from, to, from)
			}
		}
	}
}

func TestCanTransition_RejectsUnknownStatus(t *testing.T) {
	if CanTransition(Status("nonsense"), StatusPending) {
		t.Fatal("an unknown source status must have no legal transitions")
	}
}
