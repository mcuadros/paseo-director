// SPDX-License-Identifier: Apache-2.0

// Package provider defines the policy-free discovery port consumed by the
// standalone Director Engine. A Paseo connector implementation may translate
// only public-v0.7 facts into this contract; selection and fallback remain in
// the engine domain.
package provider

import (
	"context"

	"github.com/mcuadros/director-engine/domain/agentprofile"
)

// Discovery reports one normalized immutable provider snapshot. It must not
// select a profile, filter by role, apply a fallback, or return raw diagnostic
// text or credentials.
type Discovery interface {
	Discover(context.Context) (agentprofile.DiscoverySnapshot, error)
}
