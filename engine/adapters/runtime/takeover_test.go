// SPDX-License-Identifier: Apache-2.0

package runtime

import (
	"context"
	"os"
	"strconv"
	"strings"
	"testing"

	"github.com/mcuadros/director-engine/domain"
	domainexecution "github.com/mcuadros/director-engine/domain/execution"
	"github.com/mcuadros/director-engine/ports/host"
	processport "github.com/mcuadros/director-engine/ports/process"
)

type takeoverStoreFixture struct {
	project domain.Project
	tasks   []domain.Task
	runs    map[string][]domain.Run
}

func (fixture takeoverStoreFixture) Project(context.Context, string) (domain.Project, error) {
	return fixture.project, nil
}
func (fixture takeoverStoreFixture) Tasks(context.Context, string) ([]domain.Task, error) {
	return fixture.tasks, nil
}
func (fixture takeoverStoreFixture) Runs(_ context.Context, taskID string) ([]domain.Run, error) {
	return fixture.runs[taskID], nil
}

type takeoverHostFixture struct{}

func (takeoverHostFixture) Describe(context.Context) (host.Descriptor, error) {
	return host.ExpectedDescriptor()
}
func (takeoverHostFixture) Invoke(context.Context, host.Command) (host.Observation, error) {
	return host.Observation{}, nil
}

func takeoverTarget(priorProcess string) (domain.Project, processport.ProjectLeaseTakeoverTarget) {
	project := domain.Project{ID: "project-takeover", Lease: &domain.ProjectLease{HolderInstance: "engine-current",
		HolderProcessIdentity: "pid-123:start-2", Epoch: 2, DispatchAllowed: false,
		PriorHolderInstance: "engine-prior", PriorProcessIdentity: priorProcess}}
	target := processport.ProjectLeaseTakeoverTarget{ProjectID: project.ID, HolderInstance: project.Lease.HolderInstance,
		HolderProcessIdentity: project.Lease.HolderProcessIdentity, LeaseEpoch: project.Lease.Epoch,
		PriorHolderInstance: project.Lease.PriorHolderInstance, PriorProcessIdentity: priorProcess}
	return project, target
}

func TestTakeoverObserverReportsOnlyExactPriorProcessHostAndRunFacts(t *testing.T) {
	project, target := takeoverTarget("pid-99999999:start-1")
	observer, err := NewTakeoverObserver(takeoverStoreFixture{project: project, runs: map[string][]domain.Run{}}, takeoverHostFixture{})
	if err != nil {
		t.Fatal(err)
	}
	input, err := observer.ObserveProjectLeaseTakeover(context.Background(), target)
	if err != nil || input.AdapterKind != "linux-procfs-host-reconciliation" || len(input.FactHash) != 64 {
		t.Fatalf("takeover observation = %#v, %v", input, err)
	}

	activeProject, activeTarget := takeoverTarget("pid-" + strconv.Itoa(os.Getpid()) + ":start-1")
	active, _ := NewTakeoverObserver(takeoverStoreFixture{project: activeProject, runs: map[string][]domain.Run{}}, takeoverHostFixture{})
	if _, err := active.ObserveProjectLeaseTakeover(context.Background(), activeTarget); err == nil {
		t.Fatal("live prior process was reported absent")
	}

	project, target = takeoverTarget("pid-99999999:start-1")
	task := domain.Task{ID: "task-takeover"}
	unreconciled := domain.Run{ID: "run-takeover", TaskID: task.ID, Execution: domainexecution.State{}}
	observer, _ = NewTakeoverObserver(takeoverStoreFixture{project: project, tasks: []domain.Task{task},
		runs: map[string][]domain.Run{task.ID: {unreconciled}}}, takeoverHostFixture{})
	if _, err := observer.ObserveProjectLeaseTakeover(context.Background(), target); err == nil || strings.Contains(err.Error(), "/") {
		t.Fatalf("unreconciled Run refusal = %v", err)
	}
}
