// SPDX-License-Identifier: Apache-2.0

package planningtestkit

import (
	"cmp"
	"slices"
)

// Clone returns a deep copy which shares no mutable slices or parent pointers
// with project.
func Clone(project Project) Project {
	clone := project
	clone.Workspaces = slices.Clone(project.Workspaces)
	clone.Epics = slices.Clone(project.Epics)
	for index := range clone.Epics {
		clone.Epics[index].Parent = cloneRef(project.Epics[index].Parent)
	}
	clone.Tasks = slices.Clone(project.Tasks)
	for index := range clone.Tasks {
		clone.Tasks[index].WorkspaceIDs = slices.Clone(project.Tasks[index].WorkspaceIDs)
		clone.Tasks[index].Parent = cloneRef(project.Tasks[index].Parent)
		clone.Tasks[index].ExternalReferences = slices.Clone(project.Tasks[index].ExternalReferences)
	}
	clone.Dependencies = slices.Clone(project.Dependencies)
	clone.Overrides = slices.Clone(project.Overrides)
	return clone
}

func cloneRef(value *NodeRef) *NodeRef {
	if value == nil {
		return nil
	}
	clone := *value
	return &clone
}

// SortProject returns a deep copy in canonical slice order. It never changes
// an object's canonical identity or rewrites an external reference.
func SortProject(project Project) Project {
	project = Clone(project)
	slices.SortFunc(project.Workspaces, func(left, right Workspace) int {
		return cmp.Compare(left.ID, right.ID)
	})
	slices.SortStableFunc(project.Epics, func(left, right Epic) int {
		return cmp.Compare(left.ID, right.ID)
	})
	slices.SortStableFunc(project.Tasks, func(left, right Task) int {
		return cmp.Compare(left.ID, right.ID)
	})
	for index := range project.Tasks {
		slices.Sort(project.Tasks[index].WorkspaceIDs)
		slices.SortFunc(project.Tasks[index].ExternalReferences, func(left, right ExternalReference) int {
			if order := cmp.Compare(left.Provider, right.Provider); order != 0 {
				return order
			}
			return cmp.Compare(left.Key, right.Key)
		})
	}
	slices.SortStableFunc(project.Dependencies, compareDependencies)
	slices.SortStableFunc(project.Overrides, func(left, right OverrideFact) int {
		if order := cmp.Compare(left.ID, right.ID); order != 0 {
			return order
		}
		if order := cmp.Compare(left.TaskID, right.TaskID); order != 0 {
			return order
		}
		return compareDependencies(left.Dependency, right.Dependency)
	})
	return project
}

// Complexity is a monotonic structural size used by Minimize. Identity text
// is intentionally excluded so shrinking never renames canonical objects.
func Complexity(project Project) int {
	size := len(project.Workspaces) + len(project.Epics) + len(project.Tasks) +
		len(project.Dependencies) + len(project.Overrides)
	for _, task := range project.Tasks {
		size += len(task.WorkspaceIDs) + len(task.ExternalReferences)
		if task.Parent != nil {
			size++
		}
	}
	for _, epic := range project.Epics {
		if epic.Parent != nil {
			size++
		}
	}
	return size
}

// Shrink returns deterministic, strictly smaller candidates. Candidates
// preserve surviving IDs; callers choose which candidate retains their
// property with Minimize or their own predicate.
func Shrink(project Project) []Project {
	project = Clone(project)
	var candidates []Project
	appendCandidate := func(candidate Project) {
		if Complexity(candidate) < Complexity(project) {
			candidates = append(candidates, SortProject(candidate))
		}
	}

	for index := range project.Overrides {
		candidate := Clone(project)
		candidate.Overrides = removeAt(candidate.Overrides, index)
		appendCandidate(candidate)
	}
	for taskIndex, task := range project.Tasks {
		for referenceIndex := range task.ExternalReferences {
			candidate := Clone(project)
			candidate.Tasks[taskIndex].ExternalReferences = removeAt(candidate.Tasks[taskIndex].ExternalReferences, referenceIndex)
			appendCandidate(candidate)
		}
	}
	for index := range project.Dependencies {
		candidate := Clone(project)
		candidate.Dependencies = removeAt(candidate.Dependencies, index)
		appendCandidate(candidate)
	}
	for taskIndex, task := range project.Tasks {
		for workspaceIndex := range task.WorkspaceIDs {
			candidate := Clone(project)
			candidate.Tasks[taskIndex].WorkspaceIDs = removeAt(candidate.Tasks[taskIndex].WorkspaceIDs, workspaceIndex)
			appendCandidate(candidate)
		}
		if task.Parent != nil {
			candidate := Clone(project)
			candidate.Tasks[taskIndex].Parent = nil
			appendCandidate(candidate)
		}
	}
	for epicIndex, epic := range project.Epics {
		if epic.Parent != nil {
			candidate := Clone(project)
			candidate.Epics[epicIndex].Parent = nil
			appendCandidate(candidate)
		}
	}
	for index, task := range project.Tasks {
		candidate := Clone(project)
		candidate.Tasks = removeAt(candidate.Tasks, index)
		candidate.Dependencies = filterDependencies(candidate.Dependencies, ref(NodeTask, task.ID))
		candidate.Overrides = filterOverrides(candidate.Overrides, ref(NodeTask, task.ID))
		appendCandidate(candidate)
	}
	for index, epic := range project.Epics {
		candidate := Clone(project)
		candidate.Epics = removeAt(candidate.Epics, index)
		removed := ref(NodeEpic, epic.ID)
		keptTasks := candidate.Tasks[:0]
		for _, task := range candidate.Tasks {
			if task.Parent != nil && sameRef(*task.Parent, removed) {
				candidate.Dependencies = filterDependencies(candidate.Dependencies, ref(NodeTask, task.ID))
				candidate.Overrides = filterOverrides(candidate.Overrides, ref(NodeTask, task.ID))
				continue
			}
			keptTasks = append(keptTasks, task)
		}
		candidate.Tasks = keptTasks
		candidate.Dependencies = filterDependencies(candidate.Dependencies, removed)
		candidate.Overrides = filterOverrides(candidate.Overrides, removed)
		appendCandidate(candidate)
	}
	for index := range project.Workspaces {
		candidate := Clone(project)
		candidate.Workspaces = removeAt(candidate.Workspaces, index)
		appendCandidate(candidate)
	}
	return candidates
}

// Minimize greedily applies the first deterministic shrink which preserves
// predicate, restarting after each accepted reduction.
func Minimize(project Project, predicate func(Project) bool) Project {
	current := SortProject(project)
	if predicate == nil || !predicate(current) {
		return current
	}
	for {
		changed := false
		for _, candidate := range Shrink(current) {
			if predicate(candidate) {
				current = candidate
				changed = true
				break
			}
		}
		if !changed {
			return current
		}
	}
}

func removeAt[Value any](values []Value, index int) []Value {
	result := make([]Value, 0, len(values)-1)
	result = append(result, values[:index]...)
	return append(result, values[index+1:]...)
}

func filterDependencies(dependencies []Dependency, removed NodeRef) []Dependency {
	result := make([]Dependency, 0, len(dependencies))
	for _, dependency := range dependencies {
		if sameRef(dependency.From, removed) || sameRef(dependency.On, removed) {
			continue
		}
		result = append(result, dependency)
	}
	return result
}

func filterOverrides(overrides []OverrideFact, removed NodeRef) []OverrideFact {
	result := make([]OverrideFact, 0, len(overrides))
	for _, override := range overrides {
		if removed.Kind == NodeTask && override.TaskID == removed.ID ||
			sameRef(override.Dependency.From, removed) || sameRef(override.Dependency.On, removed) {
			continue
		}
		result = append(result, override)
	}
	return result
}
