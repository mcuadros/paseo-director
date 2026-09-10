// SPDX-License-Identifier: Apache-2.0

package configuration

import (
	"context"
	"errors"

	"github.com/mcuadros/director-engine/domain/agentprofile"
	providerport "github.com/mcuadros/director-engine/ports/provider"
)

// ProfileService binds an approved Organizer snapshot to normalized provider
// discovery. It owns no cache or policy state; replaying the same snapshot and
// revisions after restart produces the same frozen bytes and hash.
type ProfileService struct {
	discovery providerport.Discovery
}

// NewProfileService constructs the engine application boundary.
func NewProfileService(discovery providerport.Discovery) (*ProfileService, error) {
	if discovery == nil {
		return nil, errors.New("provider discovery port is required")
	}
	return &ProfileService{discovery: discovery}, nil
}

// FreezeProfiles observes provider facts once and binds them to the exact
// active Organizer configuration snapshot and caller-observed discovery
// revision.
func (service *ProfileService) FreezeProfiles(
	ctx context.Context,
	snapshot RunConfigurationSnapshot,
	expectedOrganizerRevision string,
	expectedDiscoveryRevision string,
	nowMillis int64,
) (agentprofile.FrozenSet, error) {
	discovery, err := service.discovery.Discover(ctx)
	if err != nil {
		return agentprofile.FrozenSet{}, err
	}
	configuration := snapshot.Configuration()
	return agentprofile.Freeze(agentprofile.FreezeRequest{
		Profiles:                  configuration.AgentProfiles,
		OrganizerRevision:         snapshot.OrganizerRevision(),
		ExpectedOrganizerRevision: expectedOrganizerRevision,
		ConfigurationSHA256:       snapshot.ConfigurationSHA256(),
		Discovery:                 discovery,
		ExpectedDiscoveryRevision: expectedDiscoveryRevision,
		NowMillis:                 nowMillis,
	})
}
