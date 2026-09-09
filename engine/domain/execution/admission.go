// SPDX-License-Identifier: Apache-2.0

// Package execution owns the immutable facts and structural state used by the
// M1 execution path. It performs no I/O; host, Git, and OCI adapters only
// observe or apply commands authorized from these facts.
package execution

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"strings"
	"unicode"
	"unicode/utf8"
)

// Scope is the complete fixed identity carried by one execution Run.
type Scope struct {
	ProjectID   string `json:"projectId"`
	WorkspaceID string `json:"workspaceId"`
	TaskID      string `json:"taskId"`
	RunID       string `json:"runId"`
}

// LifecycleSurfaces is the canonical engine input for all four automatic
// worktree surfaces exposed by the admitted Paseo schema. Command content is
// hashed for admission and is never copied into an Admission result.
type LifecycleSurfaces struct {
	Setup             []string `json:"setup"`
	Teardown          []string `json:"teardown"`
	TerminalCommands  []string `json:"terminalCommands"`
	ServicePortScript []string `json:"servicePortScript"`
}

type canonicalLifecycle struct {
	Setup             []string `json:"setup"`
	Teardown          []string `json:"teardown"`
	TerminalCommands  []string `json:"terminalCommands"`
	ServicePortScript []string `json:"servicePortScript"`
}

func normalizeCommands(commands []string) []string {
	normalized := make([]string, 0, len(commands))
	for _, command := range commands {
		if value := strings.TrimSpace(command); value != "" {
			normalized = append(normalized, value)
		}
	}
	return normalized
}

func canonicalLifecycleSurfaces(surfaces LifecycleSurfaces) canonicalLifecycle {
	return canonicalLifecycle{
		Setup:             normalizeCommands(surfaces.Setup),
		Teardown:          normalizeCommands(surfaces.Teardown),
		TerminalCommands:  normalizeCommands(surfaces.TerminalCommands),
		ServicePortScript: normalizeCommands(surfaces.ServicePortScript),
	}
}

// LifecycleDigest binds approval to the exact normalized command sequence on
// every installed automatic lifecycle surface.
func LifecycleDigest(surfaces LifecycleSurfaces) string {
	encoded, err := json.Marshal(canonicalLifecycleSurfaces(surfaces))
	if err != nil {
		panic("marshal fixed lifecycle envelope: " + err.Error())
	}
	digest := sha256.Sum256(encoded)
	return hex.EncodeToString(digest[:])
}

// LifecycleApproval can only be constructed from a normalized authenticated
// engine command. ActorKind and Source are checked again by admission so a
// parsed model claim cannot masquerade as approval.
type LifecycleApproval struct {
	ActorKind string `json:"actorKind"`
	Source    string `json:"source"`
	ActorID   string `json:"actorId"`
	Scope     Scope  `json:"scope"`
	Digest    string `json:"digest"`
}

// AdmissionKind is the closed result shared by safety fact evaluators.
type AdmissionKind string

const (
	AdmissionAllow AdmissionKind = "allow"
	AdmissionPark  AdmissionKind = "park_needs_you"
)

// NeedCode is a bounded reason which contains no lifecycle command, path, or
// provider output.
type NeedCode string

const (
	NeedLifecycleApproval             NeedCode = "lifecycle_human_approval_required"
	NeedLifecycleApprovalInvalid      NeedCode = "lifecycle_human_approval_invalid"
	NeedLifecycleConfigurationInvalid NeedCode = "lifecycle_configuration_invalid"
	NeedIsolationFactMissing          NeedCode = "rootless_oci_observation_missing"
	NeedRootlessOCI                   NeedCode = "rootless_oci_boundary_required"
	NeedOperationalPolicyInvalid      NeedCode = "operational_limit_policy_invalid"
	NeedOperationalFactMissing        NeedCode = "operational_limit_fact_missing"
	NeedFreeDiskFloor                 NeedCode = "free_disk_floor_exceeded"
	NeedWorktreeBytes                 NeedCode = "worktree_bytes_exceeded"
	NeedProcessLimit                  NeedCode = "process_limit_exceeded"
	NeedMemoryLimit                   NeedCode = "memory_limit_exceeded"
	NeedElapsedLimit                  NeedCode = "elapsed_time_limit_exceeded"
	NeedOutputLimit                   NeedCode = "output_limit_exceeded"
	NeedTemporaryLimit                NeedCode = "temporary_storage_limit_exceeded"
	NeedWorkerVisibilityMissing       NeedCode = "worker_visibility_registration_missing"
)

// Admission never grants cleanup authority. Cleanup requires a separate pure
// closure decision over current ownership and terminal facts.
type Admission struct {
	Kind              AdmissionKind `json:"kind"`
	Code              NeedCode      `json:"code,omitempty"`
	Digest            string        `json:"digest,omitempty"`
	CleanupAuthorized bool          `json:"cleanupAuthorized"`
}

func park(code NeedCode) Admission {
	return Admission{Kind: AdmissionPark, Code: code, CleanupAuthorized: false}
}

func validLifecycleSurfaces(surfaces LifecycleSurfaces) bool {
	commands := [][]string{
		surfaces.Setup, surfaces.Teardown, surfaces.TerminalCommands, surfaces.ServicePortScript,
	}
	total := 0
	for _, group := range commands {
		total += len(group)
		if total > 256 {
			return false
		}
		for _, command := range group {
			if len(command) > 4_096 || !utf8.ValidString(command) || strings.IndexFunc(command, unicode.IsControl) >= 0 {
				return false
			}
		}
	}
	return true
}

// AdmitLifecycle is the sole deterministic admission function for the four
// installed lifecycle surfaces. It never executes a command.
func AdmitLifecycle(scope Scope, surfaces LifecycleSurfaces, approval *LifecycleApproval) Admission {
	if !validLifecycleSurfaces(surfaces) {
		return park(NeedLifecycleConfigurationInvalid)
	}
	canonical := canonicalLifecycleSurfaces(surfaces)
	digest := LifecycleDigest(surfaces)
	configured := len(canonical.Setup)+len(canonical.Teardown)+len(canonical.TerminalCommands)+len(canonical.ServicePortScript) > 0
	if !configured {
		return Admission{Kind: AdmissionAllow, Digest: digest}
	}
	if approval == nil {
		return park(NeedLifecycleApproval)
	}
	if approval.ActorKind != "human" ||
		approval.Source != "authenticated_engine_command" ||
		strings.TrimSpace(approval.ActorID) == "" ||
		approval.Scope != scope ||
		approval.Digest != digest {
		return park(NeedLifecycleApprovalInvalid)
	}
	return Admission{Kind: AdmissionAllow, Digest: digest}
}

// IsolationObservation is the complete ADR-0014 rootless-OCI capability fact.
type IsolationObservation struct {
	Observed                bool   `json:"observed"`
	Runtime                 string `json:"runtime"`
	Rootless                bool   `json:"rootless"`
	ReadOnlyRootFilesystem  bool   `json:"readOnlyRootFilesystem"`
	CapabilitiesDropped     bool   `json:"capabilitiesDropped"`
	NoNewPrivileges         bool   `json:"noNewPrivileges"`
	PrivateNetworkNamespace bool   `json:"privateNetworkNamespace"`
	RuntimeSocketsAbsent    bool   `json:"runtimeSocketsAbsent"`
	ControlToolsAbsent      bool   `json:"controlToolsAbsent"`
	OwnedWorktreeOnly       bool   `json:"ownedWorktreeOnly"`
	FixedStdioMCP           bool   `json:"fixedStdioMcp"`
}

// AdmitIsolation refuses a partial or weaker provider boundary.
func AdmitIsolation(observation IsolationObservation) Admission {
	if !observation.Observed || strings.TrimSpace(observation.Runtime) == "" {
		return park(NeedIsolationFactMissing)
	}
	if !observation.Rootless || !observation.ReadOnlyRootFilesystem ||
		!observation.CapabilitiesDropped || !observation.NoNewPrivileges ||
		!observation.PrivateNetworkNamespace || !observation.RuntimeSocketsAbsent ||
		!observation.ControlToolsAbsent || !observation.OwnedWorktreeOnly ||
		!observation.FixedStdioMCP {
		return park(NeedRootlessOCI)
	}
	encoded, err := json.Marshal(observation)
	if err != nil {
		panic("marshal fixed isolation observation: " + err.Error())
	}
	digest := sha256.Sum256(encoded)
	return Admission{Kind: AdmissionAllow, Digest: hex.EncodeToString(digest[:])}
}

// Measurement distinguishes an observed zero from a missing fact.
type Measurement struct {
	Present bool   `json:"present"`
	Value   uint64 `json:"value"`
}

// OperationalPolicy contains positive finite ceilings for every resource fact
// required by the walking-skeleton launch and active checks.
type OperationalPolicy struct {
	MinimumFreeDiskBasisPoints  uint64 `json:"minimumFreeDiskBasisPoints"`
	MaximumWorktreeBytes        uint64 `json:"maximumWorktreeBytes"`
	MaximumProcesses            uint64 `json:"maximumProcesses"`
	MaximumMemoryBytes          uint64 `json:"maximumMemoryBytes"`
	MaximumElapsedMilliseconds  uint64 `json:"maximumElapsedMilliseconds"`
	MaximumOutputBytes          uint64 `json:"maximumOutputBytes"`
	MaximumTemporaryBytes       uint64 `json:"maximumTemporaryBytes"`
	MaximumObservationAgeMillis int64  `json:"maximumObservationAgeMillis"`
}

// OperationalObservation is one immutable launch or periodic sample. Time is
// supplied as TaskStore milliseconds; the pure evaluator reads no clock.
type OperationalObservation struct {
	ID                  string      `json:"id"`
	ObservedAtMillis    int64       `json:"observedAtMillis"`
	FreeDiskBasisPoints Measurement `json:"freeDiskBasisPoints"`
	WorktreeBytes       Measurement `json:"worktreeBytes"`
	Processes           Measurement `json:"processes"`
	MemoryBytes         Measurement `json:"memoryBytes"`
	ElapsedMilliseconds Measurement `json:"elapsedMilliseconds"`
	OutputBytes         Measurement `json:"outputBytes"`
	TemporaryBytes      Measurement `json:"temporaryBytes"`
}

func finitePolicy(policy OperationalPolicy) bool {
	return policy.MinimumFreeDiskBasisPoints > 0 && policy.MinimumFreeDiskBasisPoints <= 10_000 &&
		policy.MaximumWorktreeBytes > 0 && policy.MaximumProcesses > 0 &&
		policy.MaximumMemoryBytes > 0 && policy.MaximumElapsedMilliseconds > 0 &&
		policy.MaximumOutputBytes > 0 && policy.MaximumTemporaryBytes > 0 &&
		policy.MaximumObservationAgeMillis > 0
}

// EvaluateOperationalLimits applies the same deterministic policy at launch
// and during every active reconciliation pass.
func EvaluateOperationalLimits(policy OperationalPolicy, observation OperationalObservation, nowMillis int64) Admission {
	if !finitePolicy(policy) {
		return park(NeedOperationalPolicyInvalid)
	}
	if observation.ID == "" || observation.ObservedAtMillis > nowMillis ||
		nowMillis-observation.ObservedAtMillis > policy.MaximumObservationAgeMillis ||
		!observation.FreeDiskBasisPoints.Present || !observation.WorktreeBytes.Present ||
		!observation.Processes.Present || !observation.MemoryBytes.Present ||
		!observation.ElapsedMilliseconds.Present || !observation.OutputBytes.Present ||
		!observation.TemporaryBytes.Present {
		return park(NeedOperationalFactMissing)
	}
	if observation.FreeDiskBasisPoints.Value < policy.MinimumFreeDiskBasisPoints {
		return park(NeedFreeDiskFloor)
	}
	if observation.WorktreeBytes.Value > policy.MaximumWorktreeBytes {
		return park(NeedWorktreeBytes)
	}
	if observation.Processes.Value > policy.MaximumProcesses {
		return park(NeedProcessLimit)
	}
	if observation.MemoryBytes.Value > policy.MaximumMemoryBytes {
		return park(NeedMemoryLimit)
	}
	if observation.ElapsedMilliseconds.Value > policy.MaximumElapsedMilliseconds {
		return park(NeedElapsedLimit)
	}
	if observation.OutputBytes.Value > policy.MaximumOutputBytes {
		return park(NeedOutputLimit)
	}
	if observation.TemporaryBytes.Value > policy.MaximumTemporaryBytes {
		return park(NeedTemporaryLimit)
	}
	encoded, err := json.Marshal(struct {
		Policy      OperationalPolicy      `json:"policy"`
		Observation OperationalObservation `json:"observation"`
	}{policy, observation})
	if err != nil {
		panic("marshal fixed operational facts: " + err.Error())
	}
	digest := sha256.Sum256(encoded)
	return Admission{Kind: AdmissionAllow, Digest: hex.EncodeToString(digest[:])}
}
