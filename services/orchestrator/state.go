// Package orchestrator drives every payment through the bank's control gates
// and lifecycle states. Screening happens before signing; on-chain
// confirmation is an execution milestone; a payment is Settled only after the
// ledger posting reconciles with the chain.
package orchestrator

import "fmt"

type State string

const (
	Created        State = "created"
	Validated      State = "validated"
	Screening      State = "screening"
	AwaitingCoSign State = "awaiting_cosign"
	Signed         State = "signed"
	Broadcast      State = "broadcast"
	Confirmed      State = "confirmed"
	Reconciled     State = "reconciled"
	Settled        State = "settled"
	Rejected       State = "rejected"
	Held           State = "held"
	Returned       State = "returned"
	Exception      State = "exception"
)

// transitions is the complete set of legal state changes.
var transitions = map[State][]State{
	Created:        {Validated, Rejected},
	Validated:      {Screening, Held, Rejected},
	Screening:      {Signed, AwaitingCoSign, Rejected, Exception},
	AwaitingCoSign: {Signed, Rejected},
	Signed:         {Broadcast, Held},
	Broadcast:      {Confirmed, Held},
	Confirmed:      {Reconciled, Exception},
	Reconciled:     {Settled},
	Held:           {Returned, Exception},
	Exception:      {Returned},
}

// Terminal reports whether no further transition is possible.
func (s State) Terminal() bool {
	_, ok := transitions[s]
	return !ok
}

func canTransition(from, to State) bool {
	for _, s := range transitions[from] {
		if s == to {
			return true
		}
	}
	return false
}

// TransitionError reports an illegal state change.
type TransitionError struct {
	From, To State
}

func (e *TransitionError) Error() string {
	return fmt.Sprintf("orchestrator: illegal transition %s -> %s", e.From, e.To)
}
