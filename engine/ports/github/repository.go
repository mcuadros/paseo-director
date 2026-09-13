// SPDX-License-Identifier: Apache-2.0

package github

import "context"

type RepositoryDiscoveryRequest struct {
	CanonicalRemote string
	RepositoryID    string
	RepositoryKey   string
	NowMillis       int64
}

type RepositoryFact struct {
	DatabaseID       int64
	NodeID           string
	Owner            string
	Name             string
	ViewerLogin      string
	Authenticated    bool
	ObservedAtMillis int64
}

type RepositoryDiscoveryPort interface {
	DiscoverRepository(context.Context, RepositoryDiscoveryRequest) (RepositoryFact, error)
}
