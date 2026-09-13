// SPDX-License-Identifier: Apache-2.0

package runtime

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"regexp"
	"strconv"
	"time"

	"github.com/mcuadros/director-engine/domain"
	domainexecution "github.com/mcuadros/director-engine/domain/execution"
	"github.com/mcuadros/director-engine/ports/host"
	processport "github.com/mcuadros/director-engine/ports/process"
)

var processIdentityPattern = regexp.MustCompile(`^pid-([1-9][0-9]*):start-[1-9][0-9]*$`)

type TakeoverStore interface {
	Project(context.Context, string) (domain.Project, error)
	Tasks(context.Context, string) ([]domain.Task, error)
	Runs(context.Context, string) ([]domain.Run, error)
}

// TakeoverObserver proves the former engine process absent, the exact public
// host contract current, and every nonterminal Run freshly reconciled before
// the TaskStore may re-enable dispatch on a takeover lease.
type TakeoverObserver struct {
	store TakeoverStore
	host  host.Port
}

func NewTakeoverObserver(store TakeoverStore, hostPort host.Port) (*TakeoverObserver, error) {
	if store == nil || hostPort == nil {
		return nil, errors.New("takeover observation ports are required")
	}
	return &TakeoverObserver{store: store, host: hostPort}, nil
}

func processAbsent(identity string) bool {
	match := processIdentityPattern.FindStringSubmatch(identity)
	if match == nil {
		return false
	}
	pid, err := strconv.Atoi(match[1])
	if err != nil || pid <= 0 {
		return false
	}
	_, err = os.Stat("/proc/" + strconv.Itoa(pid))
	return errors.Is(err, os.ErrNotExist)
}

func takeoverDigest(value any) string {
	encoded, err := json.Marshal(value)
	if err != nil {
		return ""
	}
	digest := sha256.Sum256(encoded)
	return hex.EncodeToString(digest[:])
}

func (observer *TakeoverObserver) ObserveProjectLeaseTakeover(ctx context.Context,
	target processport.ProjectLeaseTakeoverTarget,
) (domain.ProjectLeaseObservationInput, error) {
	project, err := observer.store.Project(ctx, target.ProjectID)
	if err != nil || project.Lease == nil || project.Lease.DispatchAllowed ||
		project.Lease.HolderInstance != target.HolderInstance || project.Lease.HolderProcessIdentity != target.HolderProcessIdentity ||
		project.Lease.Epoch != target.LeaseEpoch || project.Lease.PriorHolderInstance != target.PriorHolderInstance ||
		project.Lease.PriorProcessIdentity != target.PriorProcessIdentity || !processAbsent(target.PriorProcessIdentity) {
		return domain.ProjectLeaseObservationInput{}, errors.New("prior engine process absence is unproved")
	}
	descriptor, err := observer.host.Describe(ctx)
	if err != nil || host.ValidateDescriptor(descriptor) != nil {
		return domain.ProjectLeaseObservationInput{}, errors.New("current host identity is unproved")
	}
	tasks, err := observer.store.Tasks(ctx, project.ID)
	if err != nil {
		return domain.ProjectLeaseObservationInput{}, errors.New("Project reconciliation facts are unavailable")
	}
	reconciliations := make([]string, 0)
	nowMillis := time.Now().UnixMilli()
	for _, task := range tasks {
		runs, runErr := observer.store.Runs(ctx, task.ID)
		if runErr != nil {
			return domain.ProjectLeaseObservationInput{}, errors.New("Run reconciliation facts are unavailable")
		}
		for _, run := range runs {
			if run.Execution.Terminal {
				continue
			}
			reconciliation := run.Execution.LastStartupReconciliation
			if reconciliation == nil || !domainexecution.ValidStartupReconciliation(*reconciliation) ||
				reconciliation.ObservedRunVersion > run.Version || reconciliation.ObservedAtMillis > nowMillis ||
				nowMillis-reconciliation.ObservedAtMillis > 30_000 {
				return domain.ProjectLeaseObservationInput{}, errors.New("nonterminal Run was not reconciled before takeover")
			}
			reconciliations = append(reconciliations, reconciliation.FactHash)
		}
	}
	factHash := takeoverDigest(struct {
		Target          processport.ProjectLeaseTakeoverTarget `json:"target"`
		HostContract    string                                 `json:"hostContract"`
		Reconciliations []string                               `json:"reconciliations"`
	}{target, descriptor.ContractHash, reconciliations})
	if factHash == "" {
		return domain.ProjectLeaseObservationInput{}, errors.New("takeover observation cannot be sealed")
	}
	return domain.ProjectLeaseObservationInput{AdapterKind: "linux-procfs-host-reconciliation",
		AdapterVersion: "v1", FactHash: factHash}, nil
}

var _ processport.ProjectLeaseTakeoverObserver = (*TakeoverObserver)(nil)
