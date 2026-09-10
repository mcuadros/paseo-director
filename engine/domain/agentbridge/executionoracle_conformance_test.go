// SPDX-License-Identifier: Apache-2.0

package agentbridge_test

import (
	"fmt"
	"slices"
	"strings"
	"testing"

	bridge "github.com/mcuadros/director-engine/domain/agentbridge"
	"github.com/mcuadros/director-engine/domain/agentprofile"
	domainconfig "github.com/mcuadros/director-engine/domain/configuration"
	oracle "github.com/mcuadros/director-engine/internal/testkit/executionoracle"
)

func TestExecutionOracleConformsToIntegratedSessionMCPContract(t *testing.T) {
	definition, err := bridge.EmbeddedDefinition()
	if err != nil {
		t.Fatal(err)
	}
	if definition.ContractVersion != bridge.ContractVersion || bridge.ContractVersion != "director.agent-mcp/v1" {
		t.Fatalf("contract version = %q", definition.ContractVersion)
	}
	if !slices.Equal(definition.Roles, []agentprofile.Role{
		agentprofile.RoleOrganizer, agentprofile.RoleWorker, agentprofile.RoleReviewer,
	}) {
		t.Fatalf("production session roles = %v", definition.Roles)
	}

	wantRows := []string{
		"director_candidate_read|candidate.read|reviewer|false",
		"director_planning_command_submit|planning.command.submit|organizer|true",
		"director_project_read|project.read|organizer,worker|false",
		"director_review_verdict_submit|review.verdict.submit|reviewer|true",
		"director_task_outcome_submit|task.outcome.submit|worker|true",
		"director_task_read|task.read|worker|false",
	}
	rows := make([]string, 0, len(definition.Tools))
	mapped := map[oracle.Role]oracle.ToolSet{}
	capabilityTools := map[domainconfig.MCPCapability]oracle.Tool{
		domainconfig.MCPProjectRead:           oracle.ToolInspectProject,
		domainconfig.MCPPlanningCommandSubmit: oracle.ToolSubmitPlanningCommand,
		domainconfig.MCPTaskRead:              oracle.ToolInspectRun,
		domainconfig.MCPTaskOutcomeSubmit:     oracle.ToolSubmitOutcome,
		domainconfig.MCPCandidateRead:         oracle.ToolInspectCandidate,
		domainconfig.MCPReviewVerdictSubmit:   oracle.ToolSubmitVerdict,
	}
	for _, tool := range definition.Tools {
		roleNames := make([]string, 0, len(tool.Roles))
		for _, productionRole := range tool.Roles {
			roleNames = append(roleNames, string(productionRole))
			oracleRole, ok := conformanceRole(productionRole)
			if !ok {
				t.Fatalf("production MCP contract added an unmapped role %q", productionRole)
			}
			mapped[oracleRole] |= oracle.ToolSet(capabilityTools[tool.Capability])
		}
		rows = append(rows, fmt.Sprintf("%s|%s|%s|%t", tool.Name, tool.Capability, strings.Join(roleNames, ","), tool.Mutating))
		for _, selector := range []string{"projectId", "workspaceId", "taskId", "runId", "agentId", "path", "remote", "credential"} {
			if strings.Contains(string(tool.InputSchema), `"`+selector+`"`) {
				t.Fatalf("%s leaks fixed scope selector %q into tool input", tool.Name, selector)
			}
		}
	}
	slices.Sort(rows)
	if !slices.Equal(rows, wantRows) {
		t.Fatalf("production MCP catalog drifted: got %v want %v", rows, wantRows)
	}

	if mapped[oracle.RoleOrganizer] != oracle.AllowedTools(oracle.RoleOrganizer) {
		t.Fatalf("Organizer MCP mapping = %08b, oracle = %08b", mapped[oracle.RoleOrganizer], oracle.AllowedTools(oracle.RoleOrganizer))
	}
	if mapped[oracle.RoleReviewer] != oracle.AllowedTools(oracle.RoleReviewer) {
		t.Fatalf("Reviewer MCP mapping = %08b, oracle = %08b", mapped[oracle.RoleReviewer], oracle.AllowedTools(oracle.RoleReviewer))
	}
	workerOracle := oracle.AllowedTools(oracle.RoleWorker)
	workerShared := mapped[oracle.RoleWorker] & workerOracle
	if workerShared != oracle.ToolSet(oracle.ToolInspectRun|oracle.ToolSubmitOutcome) ||
		mapped[oracle.RoleWorker]&^workerOracle != oracle.ToolSet(oracle.ToolInspectProject) ||
		workerOracle&^mapped[oracle.RoleWorker] != oracle.ToolSet(oracle.ToolRequestHelper) {
		t.Fatalf("Worker MCP boundary drifted: production=%08b oracle=%08b shared=%08b", mapped[oracle.RoleWorker], workerOracle, workerShared)
	}
	if _, exists := mapped[oracle.RoleHelper]; exists ||
		oracle.AllowedTools(oracle.RoleHelper) != oracle.ToolSet(oracle.ToolInspectRun|oracle.ToolSubmitContribution) {
		t.Fatal("helper admission/contribution must remain oracle-only and outside the Director-launched session MCP roles")
	}
}

func conformanceRole(role agentprofile.Role) (oracle.Role, bool) {
	switch role {
	case agentprofile.RoleOrganizer:
		return oracle.RoleOrganizer, true
	case agentprofile.RoleWorker:
		return oracle.RoleWorker, true
	case agentprofile.RoleReviewer:
		return oracle.RoleReviewer, true
	default:
		return 0, false
	}
}
