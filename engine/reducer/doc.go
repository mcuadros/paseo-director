// SPDX-License-Identifier: Apache-2.0

// Package reducer names the closed set of pure Director lifecycle decisions.
// Each reducer has its own package and maps immutable facts plus frozen policy
// to one deterministic decision without I/O, clocks, randomness, or adapters.
package reducer

// Kind identifies one of the six engine-owned decision reducers.
type Kind string

const (
	Eligibility Kind = "eligibility"
	Launch      Kind = "launch"
	Retry       Kind = "retry"
	Escalation  Kind = "escalation"
	Routing     Kind = "routing"
	Closure     Kind = "closure"
)

// Kinds returns the closed reducer vocabulary in contract order.
func Kinds() []Kind {
	return []Kind{Eligibility, Launch, Retry, Escalation, Routing, Closure}
}
