// SPDX-License-Identifier: Apache-2.0

// Package provider supplies the exact local CLI tuple admitted for the
// production Linux/Paseo 0.7.2 boundary. It reports facts only; profile and
// fallback selection remain in the engine domain.
package provider

import (
	"context"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/mcuadros/director-engine/domain/agentprofile"
	domainconfig "github.com/mcuadros/director-engine/domain/configuration"
	providerport "github.com/mcuadros/director-engine/ports/provider"
)

type Local struct{}

var _ providerport.Discovery = Local{}

func commandVersion(ctx context.Context, command string, arguments ...string) string {
	value := exec.CommandContext(ctx, command, arguments...)
	value.Env = []string{"LC_ALL=C", "LANG=C", "PATH=" + os.Getenv("PATH")}
	output, err := value.CombinedOutput()
	if err != nil || len(output) > 4096 {
		return ""
	}
	return strings.TrimSpace(string(output))
}

func exactVersion(output, version string) bool {
	for _, field := range strings.Fields(output) {
		if strings.TrimPrefix(field, "v") == version {
			return true
		}
	}
	return false
}

func variant(permission string, capabilities ...domainconfig.MCPCapability) agentprofile.VariantFact {
	return agentprofile.VariantFact{Effort: "high", Mode: "auto-review", PermissionMode: permission,
		ProviderOptions: []domainconfig.ProviderOption{}, MCPCapabilities: capabilities,
		SessionStdioMCP: true, ExactMCPToolPolicy: true, RuntimeProbePassed: true}
}

func providerFact(ctx context.Context, provider domainconfig.Provider, command, expected, model string, arguments ...string) agentprofile.ProviderFact {
	fact := agentprofile.ProviderFact{Provider: provider, CLIVersion: expected, State: agentprofile.ProviderUnavailable,
		DiagnosticCodes: []agentprofile.DiagnosticCode{agentprofile.DiagnosticVersionMismatch}, Models: []agentprofile.ModelFact{}}
	if !exactVersion(commandVersion(ctx, command, arguments...), expected) {
		return fact
	}
	fact.State, fact.DiagnosticCodes = agentprofile.ProviderReady, []agentprofile.DiagnosticCode{}
	fact.Models = []agentprofile.ModelFact{{Model: model, Variants: []agentprofile.VariantFact{
		variant("read-only", domainconfig.MCPProjectRead, domainconfig.MCPPlanningCommandSubmit),
		variant("workspace-write", domainconfig.MCPTaskRead, domainconfig.MCPTaskOutcomeSubmit, domainconfig.MCPTaskHelperRequest),
		variant("workspace-write", domainconfig.MCPTaskRead, domainconfig.MCPTaskOutcomeSubmit),
		variant("read-only", domainconfig.MCPCandidateRead, domainconfig.MCPReviewVerdictSubmit),
	}}}
	return fact
}

func (Local) Discover(ctx context.Context) (agentprofile.DiscoverySnapshot, error) {
	if err := ctx.Err(); err != nil {
		return agentprofile.DiscoverySnapshot{}, err
	}
	now := time.Now().UnixMilli()
	return agentprofile.SealDiscovery(agentprofile.DiscoverySnapshot{SchemaVersion: agentprofile.DiscoverySchemaVersion,
		PaseoVersion: agentprofile.SupportedPaseoVersion, ObservedAtMillis: now, MaximumAgeMillis: 30_000,
		Providers: []agentprofile.ProviderFact{
			providerFact(ctx, domainconfig.ProviderCodex, "codex", "0.147.0", "gpt-5.6-sol", "--version"),
			providerFact(ctx, domainconfig.ProviderClaudeCode, "claude", "2.1.258", "claude-haiku-4-5", "--version"),
			providerFact(ctx, domainconfig.ProviderOpenCode, "opencode", "1.18.18", "opencode/nemotron-3-ultra-free", "--version"),
		}})
}
