// SPDX-License-Identifier: Apache-2.0

package dolt_test

import (
	"context"
	"strings"
	"testing"

	"github.com/mcuadros/director-engine/domain"
	"github.com/mcuadros/director-engine/domain/execution"
)

func TestMCPCommandAndReceiptSurviveDoltReopen(t *testing.T) {
	fixture := startDoltFixture(t)
	const storeID = "agent-mcp-restart-store"
	store := openContractStore(t, fixture, storeID, true)
	ctx := context.Background()
	project := domain.Project{ID: "project-mcp", Name: "MCP", State: "active", Organizer: testOrganizer("project-mcp")}
	workspace := testWorkspace(t, project.ID, "workspace-mcp", "/srv/workspaces/mcp", "https://github.com/example/mcp.git")
	result, err := store.CreateProject(
		ctx, command("mcp-project-create", "project.create", project.ID, 0, `{}`), project, []domain.Workspace{workspace},
		event("mcp-project-created", "", 1, project.ID, 0, "project.created"),
	)
	requireApplied(t, result, err)
	task := domain.Task{ID: "task-mcp", ProjectID: project.ID, Title: "MCP", Objective: "MCP", AcceptanceCriteria: "MCP", WorkspaceIDs: []string{workspace.ID}}
	result, err = store.CreateTask(
		ctx, command("mcp-task-create", "task.create", task.ID, 0, `{}`), task,
		event("mcp-task-created", "", 1, task.ID, 0, "task.created"),
	)
	requireApplied(t, result, err)
	run := domain.Run{ID: "run-mcp", TaskID: task.ID, Number: 1, BaseSHA: baseSHA, Execution: execution.State{
		SchemaVersion: execution.SchemaVersion,
		Scope:         execution.Scope{ProjectID: project.ID, WorkspaceID: workspace.ID, TaskID: task.ID, RunID: "run-mcp"},
	}}
	result, err = store.CreateRun(
		ctx, command("mcp-run-create", "run.create", run.ID, 0, `{}`), run,
		event("mcp-run-created", run.ID, 1, run.ID, 0, "run.created"),
	)
	requireApplied(t, result, err)
	key := "mcp-command-" + strings.Repeat("1", 64)
	receipt := execution.MCPCommandReceipt{
		CommandKey: key, ToolName: "director_task_outcome_submit", Capability: "task.outcome.submit", Role: "worker",
		SessionSHA256: strings.Repeat("2", 64), PayloadSHA256: strings.Repeat("3", 64),
		EffectiveProfilesSHA256: strings.Repeat("4", 64), ConfigurationSHA256: strings.Repeat("5", 64),
	}
	run.Version = 1
	run.Execution.MCPCommandReceipts = []execution.MCPCommandReceipt{receipt}
	result, err = store.UpdateRun(
		ctx, command(key, "agent.mcp.task_outcome.submit", run.ID, 0, `{"fixedScope":"run-mcp"}`), run,
		event("mcp-event-"+strings.Repeat("1", 64), run.ID, 2, run.ID, 1, "agent.mcp.task_outcome.submit"),
	)
	requireApplied(t, result, err)
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	reopened := openContractStore(t, fixture, storeID, true)
	reloaded, err := reopened.Run(ctx, run.ID)
	if err != nil {
		t.Fatal(err)
	}
	if reloaded.Version != 1 || len(reloaded.Execution.MCPCommandReceipts) != 1 || reloaded.Execution.MCPCommandReceipts[0] != receipt {
		t.Fatalf("reloaded MCP ledger = %#v", reloaded.Execution.MCPCommandReceipts)
	}
	stored, err := reopened.Command(ctx, key)
	if err != nil {
		t.Fatal(err)
	}
	if stored.Outcome != domain.CommandApplied || stored.ObservedVersion != 1 || stored.Type != "agent.mcp.task_outcome.submit" {
		t.Fatalf("reloaded MCP Command = %#v", stored)
	}
}
