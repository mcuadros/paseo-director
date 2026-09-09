// SPDX-License-Identifier: Apache-2.0

// Package organizer defines the engine-owned filesystem and Git boundary for
// one Project's Organizer repository. Implementations observe or perform one
// exact operation and contain no Preview/Apply or lifecycle policy.
package organizer

import (
	"context"
	"errors"
)

var (
	ErrInvalidPath       = errors.New("Organizer repository path is invalid")
	ErrTargetMissing     = errors.New("Organizer repository is missing")
	ErrTargetConflict    = errors.New("Organizer repository differs from the expected content")
	ErrNotGitRepository  = errors.New("Organizer path is not an exact Git repository root")
	ErrRepositoryDirty   = errors.New("Organizer repository is dirty")
	ErrRevisionMissing   = errors.New("Organizer repository has no committed revision")
	ErrMarkerMissing     = errors.New("Organizer repository has no paseo-director.json marker")
	ErrReferenceMissing  = errors.New("Organizer repository has a missing or uncommitted explicit reference")
	ErrWorkspaceMismatch = errors.New("Workspace path or remote identity does not match")
	ErrGitOutputLimit    = errors.New("Organizer Git output exceeded bounded limit")
)

// RootSpec identifies one already-approved exact create target and marker.
type RootSpec struct {
	Path   string
	Marker []byte
}

// Snapshot is the exact read-only repository input used by Adopt and reopen.
type Snapshot struct {
	Path              string
	Revision          string
	ConfigurationJSON []byte
}

// WorkspaceSnapshot is the canonical read-only repository identity observed
// by the engine adapter. It contains no credential or raw Git output.
type WorkspaceSnapshot struct {
	SourcePath         string
	SourceDevice       uint64
	SourceInode        uint64
	GitCommonDirectory string
	GitCommonDevice    uint64
	GitCommonInode     uint64
	CanonicalRemote    string
	RepositoryKey      string
	RepositoryID       string
}

// Repository performs bounded direct-argv Git and atomic filesystem effects.
// Every Ensure method adopts an exact desired result and refuses any conflict.
type Repository interface {
	CreateTargetAbsent(context.Context, string) (bool, error)
	EnsureRoot(context.Context, RootSpec) error
	VerifyMarker(context.Context, RootSpec) error
	EnsureFile(context.Context, string, string, []byte) error
	EnsureInitialized(context.Context, string) error
	EnsureCommit(context.Context, string, []string, string) (string, error)
	Read(context.Context, string) (Snapshot, error)
	VerifyFiles(context.Context, string, []string) error
	ResolveWorkspace(context.Context, string, string) (WorkspaceSnapshot, error)
	VerifyWorkspace(context.Context, string, string) error
}
