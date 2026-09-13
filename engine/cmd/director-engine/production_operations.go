// SPDX-License-Identifier: Apache-2.0

package main

import (
	"context"
	"errors"
	"slices"
	"time"

	"github.com/mcuadros/director-engine/domain"
	homeport "github.com/mcuadros/director-engine/ports/home"
)

type OperationsExecutor struct {
	store      Store
	production *Production
}

func NewOperationsExecutor(store Store, production *Production) *OperationsExecutor {
	return &OperationsExecutor{store: store, production: production}
}

func (executor *OperationsExecutor) ObserveManualOperation(context.Context, string) (homeport.ManualOperation, homeport.ManualOperationEvidence, bool, error) {
	return homeport.ManualOperation{}, homeport.ManualOperationEvidence{}, false, nil
}

func (executor *OperationsExecutor) ApplyManualOperation(ctx context.Context, operation homeport.ManualOperation) (homeport.ManualOperationEvidence, error) {
	if executor == nil || executor.production == nil || operation.Kind != "reconcile.now" {
		return homeport.ManualOperationEvidence{}, errors.New("manual operation is unavailable")
	}
	if err := executor.production.ReconcileProject(ctx, operation.ProjectID); err != nil {
		return homeport.ManualOperationEvidence{}, err
	}
	project, err := executor.store.Project(ctx, operation.ProjectID)
	if err != nil {
		return homeport.ManualOperationEvidence{}, err
	}
	cursor, err := executor.store.LatestEventSequence(ctx)
	if err != nil {
		return homeport.ManualOperationEvidence{}, err
	}
	return homeport.ManualOperationEvidence{RequestID: operation.RequestID, Kind: operation.Kind, HostID: operation.HostID,
		HostInstanceID: operation.HostInstanceID, ProjectID: operation.ProjectID, ProjectVersion: project.Version,
		Cursor: cursor, ObservationID: operation.ObservationID, GitState: homeport.SyncNotConfigured,
		TaskStoreState: homeport.SyncCurrent, SuccessfulHalfRetried: false, CompletedAtMillis: time.Now().UnixMilli()}, nil
}

func (executor *OperationsExecutor) ObserveRepair(context.Context, string) (homeport.RepairExecution, homeport.RepairEvidence, bool, error) {
	return homeport.RepairExecution{}, homeport.RepairEvidence{}, false, nil
}

func (executor *OperationsExecutor) ApplyRepair(ctx context.Context, operation homeport.RepairExecution) (homeport.RepairEvidence, error) {
	if executor == nil || executor.production == nil || len(operation.OperationIDs) == 0 {
		return homeport.RepairEvidence{}, errors.New("repair operation is unavailable")
	}
	allowed := []string{"reconcile_lease", "reconcile_workspace_recovery"}
	for _, id := range operation.OperationIDs {
		if !slices.Contains(allowed, id) {
			return homeport.RepairEvidence{}, errors.New("repair operation is unsupported")
		}
	}
	if err := executor.production.ReconcileProject(ctx, operation.ProjectID); err != nil {
		return homeport.RepairEvidence{}, err
	}
	project, err := executor.store.Project(ctx, operation.ProjectID)
	if err != nil {
		return homeport.RepairEvidence{}, err
	}
	cursor, err := executor.store.LatestEventSequence(ctx)
	if err != nil {
		return homeport.RepairEvidence{}, err
	}
	return homeport.RepairEvidence{ProjectVersion: project.Version, Cursor: cursor, OperationIDs: slices.Clone(operation.OperationIDs)}, nil
}

func (production *Production) ReconcileProject(ctx context.Context, projectID string) error {
	project, err := production.store.Project(ctx, projectID)
	if err != nil {
		return err
	}
	if project.State == "active" && project.Organizer != nil && project.Organizer.Phase == domain.OrganizerPhaseActive {
		if _, err := production.launcher.ensureLease(ctx, project); err != nil {
			return err
		}
	}
	tasks, err := production.store.Tasks(ctx, projectID)
	if err != nil {
		return err
	}
	for _, task := range tasks {
		runs, err := production.store.Runs(ctx, task.ID)
		if err != nil {
			return err
		}
		for _, run := range runs {
			if run.Execution.Terminal {
				continue
			}
			if err := reconcileProductionRunApplication(ctx, production, run.ID); err != nil {
				return err
			}
		}
	}
	return nil
}

func reconcileProductionRunApplication(ctx context.Context, production *Production, runID string) error {
	for count := 0; count < 128; count++ {
		progressed, err := production.ReconcileRun(ctx, runID)
		if err != nil || !progressed {
			return err
		}
	}
	return errors.New("production reconciliation transition bound exceeded")
}
