// SPDX-License-Identifier: Apache-2.0

package planningtestkit

import (
	"errors"
	"fmt"
)

const (
	hardMaximumWorkspaces         = 16
	hardMaximumEpics              = 32
	hardMaximumTasks              = 128
	hardMaximumDependencies       = 512
	hardMaximumExternalReferences = 256
	hardMaximumOverrides          = 256
)

// Bounds is an explicit finite generator envelope. Generate rejects zero
// Workspace/Task maxima and values above the testkit's hard resource ceiling.
type Bounds struct {
	MaxWorkspaces         int
	MaxEpics              int
	MaxTasks              int
	MaxDependencies       int
	MaxExternalReferences int
	MaxOverrides          int
}

// DefaultBounds supplies a small corpus suitable for ordinary property tests.
func DefaultBounds() Bounds {
	return Bounds{
		MaxWorkspaces: 3, MaxEpics: 4, MaxTasks: 10,
		MaxDependencies: 20, MaxExternalReferences: 12, MaxOverrides: 8,
	}
}

func (bounds Bounds) validate() error {
	if bounds.MaxWorkspaces < 1 || bounds.MaxTasks < 1 {
		return errors.New("planningtestkit: at least one Workspace and Task are required")
	}
	values := []struct {
		name  string
		value int
		limit int
	}{
		{"Workspaces", bounds.MaxWorkspaces, hardMaximumWorkspaces},
		{"Epics", bounds.MaxEpics, hardMaximumEpics},
		{"Tasks", bounds.MaxTasks, hardMaximumTasks},
		{"Dependencies", bounds.MaxDependencies, hardMaximumDependencies},
		{"ExternalReferences", bounds.MaxExternalReferences, hardMaximumExternalReferences},
		{"Overrides", bounds.MaxOverrides, hardMaximumOverrides},
	}
	for _, value := range values {
		if value.value < 0 || value.value > value.limit {
			return fmt.Errorf("planningtestkit: Max%s must be between 0 and %d", value.name, value.limit)
		}
	}
	return nil
}

// Variant is the closed generated-case vocabulary. VariantValid is a valid
// graph; every other variant makes one named adversarial condition observable.
type Variant string

const (
	VariantValid               Variant = "valid"
	VariantDuplicateIdentity   Variant = "duplicate_identity"
	VariantMissingWorkspace    Variant = "missing_workspace"
	VariantMultipleWorkspaces  Variant = "multiple_workspaces"
	VariantThirdPlanningLevel  Variant = "third_planning_level"
	VariantDuplicateDependency Variant = "duplicate_dependency"
	VariantSelfDependency      Variant = "self_dependency"
	VariantDependencyCycle     Variant = "dependency_cycle"
	VariantMissingOverride     Variant = "missing_override"
	VariantStaleOverride       Variant = "stale_override"
	VariantAmbiguousOverride   Variant = "ambiguous_override"
	VariantUnauditedOverride   Variant = "unaudited_override"
)

// Variants returns the complete generated-case vocabulary in stable order.
func Variants() []Variant {
	return []Variant{
		VariantValid,
		VariantDuplicateIdentity,
		VariantMissingWorkspace,
		VariantMultipleWorkspaces,
		VariantThirdPlanningLevel,
		VariantDuplicateDependency,
		VariantSelfDependency,
		VariantDependencyCycle,
		VariantMissingOverride,
		VariantStaleOverride,
		VariantAmbiguousOverride,
		VariantUnauditedOverride,
	}
}

// Generate returns a valid deterministic Project. The built-in SplitMix64
// stream avoids dependence on math/rand implementation changes. With bounds
// large enough for the concepts, every result includes standalone and Epic
// Tasks, a cross-Workspace Task dependency, an Epic dependency, external
// references, and an audited human override.
func Generate(seed uint64, bounds Bounds) (Project, error) {
	if err := bounds.validate(); err != nil {
		return Project{}, err
	}
	random := splitMix64{state: seed}
	prefix := fmt.Sprintf("%016x", seed)
	workspaceCount := generatedCount(&random, bounds.MaxWorkspaces, 2)
	epicCount := generatedCount(&random, bounds.MaxEpics, 2)
	taskCount := generatedCount(&random, bounds.MaxTasks, 4)

	project := Project{ID: ID("project-" + prefix)}
	for index := 0; index < workspaceCount; index++ {
		project.Workspaces = append(project.Workspaces, Workspace{ID: ID(fmt.Sprintf("workspace-%s-%02d", prefix, index))})
	}
	for index := 0; index < epicCount; index++ {
		project.Epics = append(project.Epics, Epic{
			ID:       ID(fmt.Sprintf("epic-%s-%02d", prefix, index)),
			Complete: random.next()%3 == 0,
		})
	}
	if len(project.Epics) > 0 {
		project.Epics[0].Complete = false
	}
	for index := 0; index < taskCount; index++ {
		task := Task{
			ID:           ID(fmt.Sprintf("task-%s-%02d", prefix, index)),
			Version:      1 + random.next()%3,
			WorkspaceIDs: []ID{project.Workspaces[index%len(project.Workspaces)].ID},
			Complete:     random.next()%3 == 0,
		}
		if index >= 2 && len(project.Epics) > 0 {
			parent := ref(NodeEpic, project.Epics[(index-2)%len(project.Epics)].ID)
			task.Parent = &parent
		}
		project.Tasks = append(project.Tasks, task)
	}
	project.Tasks[0].Complete = false

	for index := 0; index < len(project.Tasks) && lenExternalReferences(project) < bounds.MaxExternalReferences; index += 2 {
		project.Tasks[index].ExternalReferences = append(project.Tasks[index].ExternalReferences, ExternalReference{
			Provider: "fixture", Key: fmt.Sprintf("EXT-%s-%02d", prefix[:6], index),
		})
	}

	addDependency := func(from, on NodeRef) {
		if len(project.Dependencies) >= bounds.MaxDependencies {
			return
		}
		candidate := Dependency{From: from, On: on}
		for _, existing := range project.Dependencies {
			if dependencyKey(existing) == dependencyKey(candidate) {
				return
			}
		}
		project.Dependencies = append(project.Dependencies, candidate)
	}
	if len(project.Tasks) >= 2 {
		addDependency(ref(NodeTask, project.Tasks[1].ID), ref(NodeTask, project.Tasks[0].ID))
	}
	for index := 2; index < len(project.Tasks); index++ {
		if random.next()%2 == 0 {
			addDependency(ref(NodeTask, project.Tasks[index].ID), ref(NodeTask, project.Tasks[index-1].ID))
		}
	}
	if len(project.Epics) >= 2 {
		addDependency(ref(NodeEpic, project.Epics[1].ID), ref(NodeEpic, project.Epics[0].ID))
	}
	for index := 2; index < len(project.Epics); index++ {
		if random.next()%2 == 0 {
			addDependency(ref(NodeEpic, project.Epics[index].ID), ref(NodeEpic, project.Epics[index-1].ID))
		}
	}

	if bounds.MaxOverrides > 0 && len(project.Dependencies) > 0 {
		dependency := project.Dependencies[0]
		if task, exists := taskBoundToDependency(project, dependency); exists {
			project.Overrides = append(project.Overrides, validOverride(task, dependency, ID("override-"+prefix+"-00")))
		}
	}
	return SortProject(project), nil
}

// GenerateCase applies one closed adversarial variant to a generated Project.
func GenerateCase(seed uint64, bounds Bounds, variant Variant) (Project, error) {
	project, err := Generate(seed, bounds)
	if err != nil {
		return Project{}, err
	}
	if variant == VariantValid {
		return project, nil
	}
	if !containsVariant(variant) {
		return Project{}, fmt.Errorf("planningtestkit: unknown variant %q", variant)
	}
	if len(project.Tasks) < 2 || len(project.Workspaces) < 2 {
		return Project{}, errors.New("planningtestkit: adversarial variants require at least two Tasks and Workspaces")
	}
	firstTask := &project.Tasks[0]
	secondTask := &project.Tasks[1]
	firstTask.Complete = false
	primary := Dependency{From: ref(NodeTask, secondTask.ID), On: ref(NodeTask, firstTask.ID)}
	ensureDependency(&project, primary)
	removeOverridesFor(&project, secondTask.ID, primary)

	switch variant {
	case VariantDuplicateIdentity:
		project.Tasks = append(project.Tasks, Clone(project).Tasks[0])
	case VariantMissingWorkspace:
		firstTask.WorkspaceIDs = nil
	case VariantMultipleWorkspaces:
		firstTask.WorkspaceIDs = []ID{project.Workspaces[0].ID, project.Workspaces[1].ID}
	case VariantThirdPlanningLevel:
		parent := ref(NodeTask, firstTask.ID)
		secondTask.Parent = &parent
	case VariantDuplicateDependency:
		project.Dependencies = append(project.Dependencies, primary)
	case VariantSelfDependency:
		project.Dependencies = append(project.Dependencies, Dependency{From: primary.From, On: primary.From})
	case VariantDependencyCycle:
		project.Dependencies = append(project.Dependencies, Dependency{From: primary.On, On: primary.From})
	case VariantMissingOverride:
		// The exact edge is incomplete and intentionally has no override fact.
	case VariantStaleOverride:
		override := validOverride(*secondTask, primary, ID("override-stale"))
		override.TaskVersion++
		project.Overrides = append(project.Overrides, override)
	case VariantAmbiguousOverride:
		project.Overrides = append(project.Overrides,
			validOverride(*secondTask, primary, ID("override-a")),
			validOverride(*secondTask, primary, ID("override-b")),
		)
	case VariantUnauditedOverride:
		override := validOverride(*secondTask, primary, ID("override-unaudited"))
		override.AuditID = ""
		project.Overrides = append(project.Overrides, override)
	}
	return project, nil
}

// GenerateCorpus returns exactly one deterministic Project for every Variant.
func GenerateCorpus(seed uint64, bounds Bounds) ([]Project, error) {
	variants := Variants()
	corpus := make([]Project, 0, len(variants))
	for index, variant := range variants {
		project, err := GenerateCase(seed+uint64(index), bounds, variant)
		if err != nil {
			return nil, fmt.Errorf("generate %s: %w", variant, err)
		}
		corpus = append(corpus, project)
	}
	return corpus, nil
}

// SmallCase is one named member of ExhaustiveSmallStates.
type SmallCase struct {
	Name    string
	Project Project
}

// ExhaustiveSmallStates enumerates all completion and directed-edge subsets
// for two Tasks and then two Epics, plus every override acceptance class.
func ExhaustiveSmallStates() []SmallCase {
	const edgeSubsets = 1 << 4
	cases := make([]SmallCase, 0, 4*edgeSubsets*2+5)
	for completionMask := 0; completionMask < 4; completionMask++ {
		for edgeMask := 0; edgeMask < edgeSubsets; edgeMask++ {
			project := smallTaskProject(completionMask, edgeMask)
			cases = append(cases, SmallCase{
				Name:    fmt.Sprintf("tasks/completion-%02d/edges-%02d", completionMask, edgeMask),
				Project: project,
			})
		}
	}
	for completionMask := 0; completionMask < 4; completionMask++ {
		for edgeMask := 0; edgeMask < edgeSubsets; edgeMask++ {
			project := smallEpicProject(completionMask, edgeMask)
			cases = append(cases, SmallCase{
				Name:    fmt.Sprintf("epics/completion-%02d/edges-%02d", completionMask, edgeMask),
				Project: project,
			})
		}
	}
	for _, variant := range []Variant{
		VariantMissingOverride,
		VariantValid,
		VariantStaleOverride,
		VariantAmbiguousOverride,
		VariantUnauditedOverride,
	} {
		project := overrideSmallProject(variant)
		cases = append(cases, SmallCase{Name: "overrides/" + string(variant), Project: project})
	}
	return cases
}

type splitMix64 struct{ state uint64 }

func (random *splitMix64) next() uint64 {
	random.state += 0x9e3779b97f4a7c15
	value := random.state
	value = (value ^ (value >> 30)) * 0xbf58476d1ce4e5b9
	value = (value ^ (value >> 27)) * 0x94d049bb133111eb
	return value ^ (value >> 31)
}

func generatedCount(random *splitMix64, maximum, desiredMinimum int) int {
	if maximum == 0 {
		return 0
	}
	minimum := min(maximum, desiredMinimum)
	return minimum + int(random.next()%uint64(maximum-minimum+1))
}

func lenExternalReferences(project Project) int {
	total := 0
	for _, task := range project.Tasks {
		total += len(task.ExternalReferences)
	}
	return total
}

func containsVariant(candidate Variant) bool {
	for _, variant := range Variants() {
		if candidate == variant {
			return true
		}
	}
	return false
}

func ensureDependency(project *Project, dependency Dependency) {
	for _, existing := range project.Dependencies {
		if dependencyKey(existing) == dependencyKey(dependency) {
			return
		}
	}
	project.Dependencies = append(project.Dependencies, dependency)
}

func removeOverridesFor(project *Project, taskID ID, dependency Dependency) {
	filtered := project.Overrides[:0]
	for _, override := range project.Overrides {
		if override.TaskID == taskID && dependencyKey(override.Dependency) == dependencyKey(dependency) {
			continue
		}
		filtered = append(filtered, override)
	}
	project.Overrides = filtered
}

func validOverride(task Task, dependency Dependency, id ID) OverrideFact {
	return OverrideFact{
		ID: id, TaskID: task.ID, TaskVersion: task.Version, Dependency: dependency,
		ActorKind: ActorHuman, ActorID: "human-fixture", AuditID: "audit-" + string(id), Granted: true,
	}
}

func taskBoundToDependency(project Project, dependency Dependency) (Task, bool) {
	if dependency.From.Kind == NodeTask {
		for _, task := range project.Tasks {
			if task.ID == dependency.From.ID {
				return task, true
			}
		}
	}
	if dependency.From.Kind == NodeEpic {
		for _, task := range project.Tasks {
			if task.Parent != nil && sameRef(*task.Parent, dependency.From) {
				return task, true
			}
		}
	}
	return Task{}, false
}

func smallTaskProject(completionMask, edgeMask int) Project {
	project := Project{
		ID:         "small-task-project",
		Workspaces: []Workspace{{ID: "workspace-0"}, {ID: "workspace-1"}},
		Tasks: []Task{
			{ID: "task-0", Version: 1, WorkspaceIDs: []ID{"workspace-0"}, Complete: completionMask&1 != 0,
				ExternalReferences: []ExternalReference{{Provider: "fixture", Key: "EXT-0"}}},
			{ID: "task-1", Version: 1, WorkspaceIDs: []ID{"workspace-1"}, Complete: completionMask&2 != 0},
		},
	}
	nodes := []NodeRef{ref(NodeTask, "task-0"), ref(NodeTask, "task-1")}
	edges := []Dependency{
		{From: nodes[0], On: nodes[0]},
		{From: nodes[0], On: nodes[1]},
		{From: nodes[1], On: nodes[0]},
		{From: nodes[1], On: nodes[1]},
	}
	for bit, edge := range edges {
		if edgeMask&(1<<bit) != 0 {
			project.Dependencies = append(project.Dependencies, edge)
		}
	}
	return project
}

func smallEpicProject(completionMask, edgeMask int) Project {
	epic0 := ref(NodeEpic, "epic-0")
	epic1 := ref(NodeEpic, "epic-1")
	project := Project{
		ID:         "small-epic-project",
		Workspaces: []Workspace{{ID: "workspace-0"}, {ID: "workspace-1"}},
		Epics: []Epic{
			{ID: epic0.ID, Complete: completionMask&1 != 0},
			{ID: epic1.ID, Complete: completionMask&2 != 0},
		},
		Tasks: []Task{
			{ID: "task-0", Version: 1, WorkspaceIDs: []ID{"workspace-0"}, Parent: &epic0},
			{ID: "task-1", Version: 1, WorkspaceIDs: []ID{"workspace-1"}, Parent: &epic1},
		},
	}
	edges := []Dependency{
		{From: epic0, On: epic0},
		{From: epic0, On: epic1},
		{From: epic1, On: epic0},
		{From: epic1, On: epic1},
	}
	for bit, edge := range edges {
		if edgeMask&(1<<bit) != 0 {
			project.Dependencies = append(project.Dependencies, edge)
		}
	}
	return project
}

func overrideSmallProject(variant Variant) Project {
	project := smallTaskProject(0, 1<<2)
	edge := project.Dependencies[0]
	task := project.Tasks[1]
	switch variant {
	case VariantValid:
		project.Overrides = []OverrideFact{validOverride(task, edge, "override-valid")}
	case VariantStaleOverride:
		override := validOverride(task, edge, "override-stale")
		override.TaskVersion++
		project.Overrides = []OverrideFact{override}
	case VariantAmbiguousOverride:
		project.Overrides = []OverrideFact{
			validOverride(task, edge, "override-a"),
			validOverride(task, edge, "override-b"),
		}
	case VariantUnauditedOverride:
		override := validOverride(task, edge, "override-unaudited")
		override.AuditID = ""
		project.Overrides = []OverrideFact{override}
	case VariantMissingOverride:
	}
	return project
}
