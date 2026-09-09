// SPDX-License-Identifier: Apache-2.0

package planningtestkit

import (
	"cmp"
	"slices"
	"sort"
)

type guardSet uint16

const (
	guardStableIdentity guardSet = 1 << iota
	guardWorkspaceBinding
	guardHierarchy
	guardExternalReferences
	guardDependencyEndpoints
	guardDuplicateDependencies
	guardSelfDependencies
	guardDependencyCycles
	guardDirectBlockers
	guardTransitiveBlockers
	guardEpicPropagation
	guardOverrideAuthorization
	guardOverrideIdentityUniqueness
)

const allGuards = guardStableIdentity |
	guardWorkspaceBinding |
	guardHierarchy |
	guardExternalReferences |
	guardDependencyEndpoints |
	guardDuplicateDependencies |
	guardSelfDependencies |
	guardDependencyCycles |
	guardDirectBlockers |
	guardTransitiveBlockers |
	guardEpicPropagation |
	guardOverrideAuthorization |
	guardOverrideIdentityUniqueness

type planningIndex struct {
	workspaces  map[ID]struct{}
	epicByID    map[ID]Epic
	taskByID    map[ID]Task
	nodes       map[string]NodeRef
	complete    map[string]bool
	edges       map[string]Dependency
	byFrom      map[string][]Dependency
	overrides   map[string][]OverrideFact
	overrideIDs map[ID]int
}

// Evaluate applies the complete PLAN-derived invariant oracle.
func Evaluate(project Project) Report {
	return evaluateWithGuards(project, allGuards)
}

func evaluateWithGuards(project Project, guards guardSet) Report {
	index := buildIndex(project)
	structural := validateStructure(project, index, guards)
	overrideFindings := validateOverrides(project, index, guards)
	report := Report{
		Valid:    len(structural) == 0,
		Findings: append(structural, overrideFindings...),
	}
	sortExplanations(report.Findings)
	report.Findings = deduplicateExplanations(report.Findings)

	tasks := slices.Clone(project.Tasks)
	slices.SortStableFunc(tasks, func(left, right Task) int {
		return cmp.Compare(left.ID, right.ID)
	})
	report.Tasks = make([]TaskResult, 0, len(tasks))
	for _, task := range tasks {
		if !report.Valid {
			report.Tasks = append(report.Tasks, TaskResult{
				TaskID:  task.ID,
				Blocked: true,
				Explanations: []Explanation{{
					Code:    CodePlanningInvalid,
					Subject: ref(NodeTask, task.ID),
				}},
			})
			continue
		}
		report.Tasks = append(report.Tasks, evaluateTask(task, index, guards))
	}
	return report
}

func buildIndex(project Project) planningIndex {
	index := planningIndex{
		workspaces:  make(map[ID]struct{}, len(project.Workspaces)),
		epicByID:    make(map[ID]Epic, len(project.Epics)),
		taskByID:    make(map[ID]Task, len(project.Tasks)),
		nodes:       make(map[string]NodeRef, len(project.Epics)+len(project.Tasks)),
		complete:    make(map[string]bool, len(project.Epics)+len(project.Tasks)),
		edges:       make(map[string]Dependency, len(project.Dependencies)),
		byFrom:      make(map[string][]Dependency),
		overrides:   make(map[string][]OverrideFact),
		overrideIDs: make(map[ID]int, len(project.Overrides)),
	}
	for _, workspace := range project.Workspaces {
		index.workspaces[workspace.ID] = struct{}{}
	}
	for _, epic := range project.Epics {
		node := ref(NodeEpic, epic.ID)
		index.epicByID[epic.ID] = epic
		index.nodes[refKey(node)] = node
		index.complete[refKey(node)] = epic.Complete
	}
	for _, task := range project.Tasks {
		node := ref(NodeTask, task.ID)
		index.taskByID[task.ID] = task
		index.nodes[refKey(node)] = node
		index.complete[refKey(node)] = task.Complete
	}
	for _, dependency := range project.Dependencies {
		key := dependencyKey(dependency)
		index.edges[key] = dependency
		index.byFrom[refKey(dependency.From)] = append(index.byFrom[refKey(dependency.From)], dependency)
	}
	for key := range index.byFrom {
		slices.SortFunc(index.byFrom[key], compareDependencies)
	}
	for _, override := range project.Overrides {
		key := overrideBindingKey(override.TaskID, override.Dependency)
		index.overrides[key] = append(index.overrides[key], override)
		index.overrideIDs[override.ID]++
	}
	for key := range index.overrides {
		slices.SortFunc(index.overrides[key], func(left, right OverrideFact) int {
			return cmp.Compare(left.ID, right.ID)
		})
	}
	return index
}

func validateStructure(project Project, index planningIndex, guards guardSet) []Explanation {
	var findings []Explanation
	if guards&guardStableIdentity != 0 {
		if project.ID == "" {
			findings = append(findings, Explanation{Code: CodeProjectIDMissing, Subject: ref(NodeProject, "")})
		}
		findings = append(findings, duplicateIdentityFindings(project)...)
	}
	if guards&guardWorkspaceBinding != 0 {
		if len(project.Workspaces) == 0 {
			findings = append(findings, Explanation{Code: CodeWorkspaceRequired, Subject: ref(NodeProject, project.ID)})
		}
		for _, task := range project.Tasks {
			subject := ref(NodeTask, task.ID)
			if len(task.WorkspaceIDs) != 1 {
				findings = append(findings, Explanation{Code: CodeTaskWorkspaceCount, Subject: subject})
				continue
			}
			if _, exists := index.workspaces[task.WorkspaceIDs[0]]; !exists {
				findings = append(findings, Explanation{
					Code: CodeTaskWorkspaceUnknown, Subject: subject,
					Related: []NodeRef{ref(NodeWorkspace, task.WorkspaceIDs[0])},
				})
			}
		}
	}
	if guards&guardHierarchy != 0 {
		for _, epic := range project.Epics {
			if epic.Parent != nil {
				findings = append(findings, Explanation{
					Code: CodeHierarchyThirdLevel, Subject: ref(NodeEpic, epic.ID),
					Related: []NodeRef{*epic.Parent},
				})
			}
		}
		for _, task := range project.Tasks {
			if task.Parent == nil {
				continue
			}
			if task.Parent.Kind != NodeEpic {
				findings = append(findings, Explanation{
					Code: CodeHierarchyThirdLevel, Subject: ref(NodeTask, task.ID),
					Related: []NodeRef{*task.Parent},
				})
				continue
			}
			if _, exists := index.epicByID[task.Parent.ID]; !exists {
				findings = append(findings, Explanation{
					Code: CodeTaskEpicUnknown, Subject: ref(NodeTask, task.ID),
					Related: []NodeRef{*task.Parent},
				})
			}
		}
	}
	if guards&guardExternalReferences != 0 {
		for _, task := range project.Tasks {
			for _, external := range task.ExternalReferences {
				if external.Provider == "" {
					findings = append(findings, Explanation{Code: CodeExternalReferenceProviderMissing, Subject: ref(NodeTask, task.ID)})
				}
				if external.Key == "" {
					findings = append(findings, Explanation{Code: CodeExternalReferenceKeyMissing, Subject: ref(NodeTask, task.ID)})
				}
			}
		}
	}

	seenDependencies := make(map[string]struct{}, len(project.Dependencies))
	for _, dependency := range project.Dependencies {
		subject := dependency.From
		validKinds := planningDependencyKind(dependency.From.Kind) && planningDependencyKind(dependency.On.Kind)
		if !validKinds || dependency.From.Kind == NodeEpic && dependency.On.Kind != NodeEpic {
			findings = append(findings, Explanation{
				Code: CodeDependencyKindInvalid, Subject: subject, Related: []NodeRef{dependency.On},
			})
		}
		if guards&guardDependencyEndpoints != 0 {
			if dependency.From.ID == "" || dependency.On.ID == "" {
				findings = append(findings, Explanation{
					Code: CodeDependencyEndpointMissing, Subject: subject, Related: []NodeRef{dependency.On},
				})
			} else {
				if _, exists := index.nodes[refKey(dependency.From)]; !exists {
					findings = append(findings, Explanation{
						Code: CodeDependencyEndpointUnknown, Subject: subject, Related: []NodeRef{dependency.On},
					})
				}
				if _, exists := index.nodes[refKey(dependency.On)]; !exists {
					findings = append(findings, Explanation{
						Code: CodeDependencyEndpointUnknown, Subject: subject, Related: []NodeRef{dependency.On},
					})
				}
			}
		}
		key := dependencyKey(dependency)
		if guards&guardDuplicateDependencies != 0 {
			if _, exists := seenDependencies[key]; exists {
				findings = append(findings, Explanation{
					Code: CodeDependencyDuplicate, Subject: subject, Related: []NodeRef{dependency.On},
				})
			}
		}
		seenDependencies[key] = struct{}{}
		if guards&guardSelfDependencies != 0 && sameRef(dependency.From, dependency.On) {
			findings = append(findings, Explanation{
				Code: CodeDependencySelf, Subject: subject, Related: []NodeRef{dependency.On},
			})
		}
	}
	if guards&guardDependencyCycles != 0 {
		if cycle := firstDependencyCycle(project.Dependencies, index); len(cycle) > 0 {
			findings = append(findings, Explanation{
				Code: CodeDependencyCycle, Subject: cycle[0], Related: slices.Clone(cycle[1:]),
			})
		}
	}
	sortExplanations(findings)
	return deduplicateExplanations(findings)
}

func duplicateIdentityFindings(project Project) []Explanation {
	var findings []Explanation
	workspaceIDs := make(map[ID]struct{}, len(project.Workspaces))
	for _, workspace := range project.Workspaces {
		subject := ref(NodeWorkspace, workspace.ID)
		if workspace.ID == "" {
			findings = append(findings, Explanation{Code: CodeWorkspaceIDMissing, Subject: subject})
		} else if _, exists := workspaceIDs[workspace.ID]; exists {
			findings = append(findings, Explanation{Code: CodeWorkspaceIDDuplicate, Subject: subject})
		}
		workspaceIDs[workspace.ID] = struct{}{}
	}
	epicIDs := make(map[ID]struct{}, len(project.Epics))
	for _, epic := range project.Epics {
		subject := ref(NodeEpic, epic.ID)
		if epic.ID == "" {
			findings = append(findings, Explanation{Code: CodeEpicIDMissing, Subject: subject})
		} else if _, exists := epicIDs[epic.ID]; exists {
			findings = append(findings, Explanation{Code: CodeEpicIDDuplicate, Subject: subject})
		}
		epicIDs[epic.ID] = struct{}{}
	}
	taskIDs := make(map[ID]struct{}, len(project.Tasks))
	for _, task := range project.Tasks {
		subject := ref(NodeTask, task.ID)
		if task.ID == "" {
			findings = append(findings, Explanation{Code: CodeTaskIDMissing, Subject: subject})
		} else if _, exists := taskIDs[task.ID]; exists {
			findings = append(findings, Explanation{Code: CodeTaskIDDuplicate, Subject: subject})
		}
		taskIDs[task.ID] = struct{}{}
	}
	return findings
}

func validateOverrides(project Project, index planningIndex, guards guardSet) []Explanation {
	if guards&guardOverrideAuthorization == 0 {
		return nil
	}
	var findings []Explanation
	bindings := make(map[string][]OverrideFact, len(project.Overrides))
	for _, override := range project.Overrides {
		subject := ref(NodeTask, override.TaskID)
		if override.ID == "" {
			findings = append(findings, Explanation{Code: CodeOverrideIDMissing, Subject: subject})
		} else if guards&guardOverrideIdentityUniqueness != 0 && index.overrideIDs[override.ID] > 1 {
			findings = append(findings, overrideExplanation(CodeOverrideIDDuplicate, override.TaskID, override))
		}
		bindings[overrideBindingKey(override.TaskID, override.Dependency)] = append(bindings[overrideBindingKey(override.TaskID, override.Dependency)], override)

		task, taskExists := index.taskByID[override.TaskID]
		_, edgeExists := index.edges[dependencyKey(override.Dependency)]
		if !taskExists || !edgeExists {
			findings = append(findings, Explanation{
				Code: CodeOverrideBindingUnknown, Subject: subject,
				Related: []NodeRef{override.Dependency.From, override.Dependency.On}, OverrideID: override.ID,
			})
			continue
		}
		if override.TaskVersion != task.Version {
			findings = append(findings, overrideExplanation(CodeOverrideStale, task.ID, override))
		}
		if override.ActorKind != ActorHuman {
			findings = append(findings, overrideExplanation(CodeOverrideActorNotHuman, task.ID, override))
		}
		if override.ActorID == "" {
			findings = append(findings, overrideExplanation(CodeOverrideActorMissing, task.ID, override))
		}
		if override.AuditID == "" {
			findings = append(findings, overrideExplanation(CodeOverrideAuditMissing, task.ID, override))
		}
		if !override.Granted {
			findings = append(findings, overrideExplanation(CodeOverrideNotGranted, task.ID, override))
		}
	}
	for _, matching := range bindings {
		if len(matching) > 1 {
			findings = append(findings, overrideExplanation(CodeOverrideAmbiguous, matching[0].TaskID, matching[0]))
		}
	}
	sortExplanations(findings)
	return deduplicateExplanations(findings)
}

func evaluateTask(task Task, index planningIndex, guards guardSet) TaskResult {
	result := TaskResult{TaskID: task.ID}
	root := ref(NodeTask, task.ID)
	visitedDepth := make(map[string]int)

	var walk func(NodeRef, int)
	walk = func(from NodeRef, depth int) {
		for _, dependency := range index.byFrom[refKey(from)] {
			targetKey := refKey(dependency.On)
			if index.complete[targetKey] {
				continue
			}
			applied, rejection := authorizeOverride(task, dependency, index, guards)
			if applied {
				continue
			}
			if rejection.Code != "" {
				result.Explanations = append(result.Explanations, rejection)
			}

			includeBlocker := depth == 0 && guards&guardDirectBlockers != 0 ||
				depth > 0 && guards&guardTransitiveBlockers != 0
			if includeBlocker {
				code := blockerCode(dependency.On.Kind, depth)
				result.Explanations = append(result.Explanations, Explanation{
					Code: code, Subject: root,
					Related: []NodeRef{dependency.From, dependency.On},
				})
			}
			if guards&guardTransitiveBlockers == 0 {
				continue
			}
			nextDepth := depth + 1
			if previous, seen := visitedDepth[targetKey]; seen && previous <= nextDepth {
				continue
			}
			visitedDepth[targetKey] = nextDepth
			walk(dependency.On, nextDepth)
			if dependency.On.Kind == NodeTask {
				if target, exists := index.taskByID[dependency.On.ID]; exists && target.Parent != nil && guards&guardEpicPropagation != 0 {
					walk(*target.Parent, nextDepth)
				}
			}
		}
	}

	visitedDepth[refKey(root)] = 0
	walk(root, 0)
	if task.Parent != nil && guards&guardEpicPropagation != 0 {
		walk(*task.Parent, 0)
	}
	sortExplanations(result.Explanations)
	result.Explanations = deduplicateExplanations(result.Explanations)
	for _, explanation := range result.Explanations {
		if blockerCodeValue(explanation.Code) {
			result.Blocked = true
			break
		}
	}
	return result
}

func authorizeOverride(task Task, dependency Dependency, index planningIndex, guards guardSet) (bool, Explanation) {
	matching := index.overrides[overrideBindingKey(task.ID, dependency)]
	if len(matching) == 0 {
		return false, Explanation{
			Code: CodeOverrideMissing, Subject: ref(NodeTask, task.ID),
			Related: []NodeRef{dependency.From, dependency.On},
		}
	}
	if guards&guardOverrideAuthorization == 0 {
		if len(matching) == 1 && matching[0].Granted {
			return true, Explanation{}
		}
		return false, Explanation{}
	}
	if len(matching) != 1 {
		return false, overrideExplanation(CodeOverrideAmbiguous, task.ID, matching[0])
	}
	override := matching[0]
	switch {
	case override.ID == "":
		return false, overrideExplanation(CodeOverrideIDMissing, task.ID, override)
	case guards&guardOverrideIdentityUniqueness != 0 && index.overrideIDs[override.ID] > 1:
		return false, overrideExplanation(CodeOverrideIDDuplicate, task.ID, override)
	case override.TaskVersion != task.Version:
		return false, overrideExplanation(CodeOverrideStale, task.ID, override)
	case override.ActorKind != ActorHuman:
		return false, overrideExplanation(CodeOverrideActorNotHuman, task.ID, override)
	case override.ActorID == "":
		return false, overrideExplanation(CodeOverrideActorMissing, task.ID, override)
	case override.AuditID == "":
		return false, overrideExplanation(CodeOverrideAuditMissing, task.ID, override)
	case !override.Granted:
		return false, overrideExplanation(CodeOverrideNotGranted, task.ID, override)
	default:
		return true, Explanation{}
	}
}

func overrideExplanation(code Code, taskID ID, override OverrideFact) Explanation {
	return Explanation{
		Code: code, Subject: ref(NodeTask, taskID),
		Related: []NodeRef{override.Dependency.From, override.Dependency.On}, OverrideID: override.ID,
	}
}

func blockerCode(kind NodeKind, depth int) Code {
	if kind == NodeEpic {
		if depth == 0 {
			return CodeEpicDependencyBlocked
		}
		return CodeEpicDependencyBlockedTransitive
	}
	if depth == 0 {
		return CodeTaskDependencyBlocked
	}
	return CodeTaskDependencyBlockedTransitive
}

func blockerCodeValue(code Code) bool {
	return code == CodeTaskDependencyBlocked ||
		code == CodeTaskDependencyBlockedTransitive ||
		code == CodeEpicDependencyBlocked ||
		code == CodeEpicDependencyBlockedTransitive
}

func planningDependencyKind(kind NodeKind) bool { return kind == NodeEpic || kind == NodeTask }

func overrideBindingKey(taskID ID, dependency Dependency) string {
	return string(taskID) + "\x02" + dependencyKey(dependency)
}

func firstDependencyCycle(dependencies []Dependency, index planningIndex) []NodeRef {
	adjacency := make(map[string][]NodeRef)
	for _, dependency := range dependencies {
		if !planningDependencyKind(dependency.From.Kind) || !planningDependencyKind(dependency.On.Kind) {
			continue
		}
		if dependency.From.Kind == NodeEpic && dependency.On.Kind != NodeEpic {
			continue
		}
		if _, exists := index.nodes[refKey(dependency.From)]; !exists {
			continue
		}
		if _, exists := index.nodes[refKey(dependency.On)]; !exists {
			continue
		}
		adjacency[refKey(dependency.From)] = append(adjacency[refKey(dependency.From)], dependency.On)
	}
	for key := range adjacency {
		slices.SortFunc(adjacency[key], compareRefs)
		adjacency[key] = slices.CompactFunc(adjacency[key], sameRef)
	}
	nodes := make([]NodeRef, 0, len(index.nodes))
	for _, node := range index.nodes {
		nodes = append(nodes, node)
	}
	slices.SortFunc(nodes, compareRefs)

	state := make(map[string]uint8, len(nodes))
	stack := make([]NodeRef, 0, len(nodes))
	positions := make(map[string]int, len(nodes))
	var visit func(NodeRef) []NodeRef
	visit = func(node NodeRef) []NodeRef {
		key := refKey(node)
		state[key] = 1
		positions[key] = len(stack)
		stack = append(stack, node)
		for _, next := range adjacency[key] {
			nextKey := refKey(next)
			switch state[nextKey] {
			case 0:
				if cycle := visit(next); len(cycle) > 0 {
					return cycle
				}
			case 1:
				start := positions[nextKey]
				cycle := slices.Clone(stack[start:])
				return append(cycle, next)
			}
		}
		stack = stack[:len(stack)-1]
		delete(positions, key)
		state[key] = 2
		return nil
	}
	for _, node := range nodes {
		if state[refKey(node)] == 0 {
			if cycle := visit(node); len(cycle) > 0 {
				return cycle
			}
		}
	}
	return nil
}

func compareRefs(left, right NodeRef) int {
	if order := cmp.Compare(left.Kind, right.Kind); order != 0 {
		return order
	}
	return cmp.Compare(left.ID, right.ID)
}

func compareDependencies(left, right Dependency) int {
	if order := compareRefs(left.From, right.From); order != 0 {
		return order
	}
	return compareRefs(left.On, right.On)
}

func sortExplanations(explanations []Explanation) {
	slices.SortStableFunc(explanations, func(left, right Explanation) int {
		if order := cmp.Compare(left.Code, right.Code); order != 0 {
			return order
		}
		if order := compareRefs(left.Subject, right.Subject); order != 0 {
			return order
		}
		if order := compareRefSlices(left.Related, right.Related); order != 0 {
			return order
		}
		return cmp.Compare(left.OverrideID, right.OverrideID)
	})
}

func compareRefSlices(left, right []NodeRef) int {
	limit := min(len(left), len(right))
	for index := 0; index < limit; index++ {
		if order := compareRefs(left[index], right[index]); order != 0 {
			return order
		}
	}
	return cmp.Compare(len(left), len(right))
}

func deduplicateExplanations(explanations []Explanation) []Explanation {
	return slices.CompactFunc(explanations, func(left, right Explanation) bool {
		return left.Code == right.Code &&
			sameRef(left.Subject, right.Subject) &&
			compareRefSlices(left.Related, right.Related) == 0 &&
			left.OverrideID == right.OverrideID
	})
}

// DependencyClosure returns the stable canonical IDs reachable from start.
// Invalid endpoints are omitted; Evaluate must be used to validate input.
func DependencyClosure(project Project, start NodeRef) []NodeRef {
	index := buildIndex(project)
	seen := map[string]struct{}{refKey(start): {}}
	queue := []NodeRef{start}
	var result []NodeRef
	for len(queue) > 0 {
		current := queue[0]
		queue = queue[1:]
		for _, dependency := range index.byFrom[refKey(current)] {
			key := refKey(dependency.On)
			if _, exists := index.nodes[key]; !exists {
				continue
			}
			if _, exists := seen[key]; exists {
				continue
			}
			seen[key] = struct{}{}
			result = append(result, dependency.On)
			queue = append(queue, dependency.On)
		}
	}
	sort.Slice(result, func(left, right int) bool { return compareRefs(result[left], result[right]) < 0 })
	return result
}
