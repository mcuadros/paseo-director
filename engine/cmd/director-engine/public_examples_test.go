// SPDX-License-Identifier: Apache-2.0

package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	configurationdomain "github.com/mcuadros/director-engine/domain/configuration"
)

func TestPublicExampleOrganizer(t *testing.T) {
	root := filepath.Join("..", "..", "..", "examples", "organizer")
	content, err := os.ReadFile(filepath.Join(root, "paseo-director.json"))
	if err != nil {
		t.Fatalf("read public Organizer example: %v", err)
	}
	document, err := configurationdomain.Parse(content)
	if err != nil {
		t.Fatalf("parse public Organizer example: %v", err)
	}
	configuration := document.Configuration()
	if configuration.Project.ID != "example-multi-repo" || len(configuration.Workspaces) != 2 {
		t.Fatalf("public Organizer identity = %q with %d Workspaces", configuration.Project.ID, len(configuration.Workspaces))
	}
	expectedRemotes := map[string]string{
		"web": "https://git.example.invalid/director/web.git",
		"api": "git@git.example.invalid:director/api.git",
	}
	seen := map[string]bool{}
	for _, workspace := range configuration.Workspaces {
		if seen[workspace.ID] || workspace.Remote != expectedRemotes[workspace.ID] ||
			workspace.SourcePath != "/srv/director-example/"+workspace.ID {
			t.Fatalf("unsafe or ambiguous public Workspace example: %#v", workspace)
		}
		seen[workspace.ID] = true
	}
	if !seen["web"] || !seen["api"] {
		t.Fatalf("public Organizer Workspaces = %#v", seen)
	}
	for _, reference := range append(configuration.Skills, configuration.Templates...) {
		path := filepath.Join(root, filepath.FromSlash(reference.Path))
		status, statErr := os.Lstat(path)
		if statErr != nil || !status.Mode().IsRegular() || status.Mode()&os.ModeSymlink != 0 {
			t.Fatalf("public Organizer reference %q is not a regular file: %v", reference.Path, statErr)
		}
	}
	text := string(content)
	for _, forbidden := range []string{"/home/", "/tmp/", "password", "credential", "authorization", "privateKey", "accessToken"} {
		if strings.Contains(strings.ToLower(text), strings.ToLower(forbidden)) {
			t.Fatalf("public Organizer contains forbidden private or credential-shaped value %q", forbidden)
		}
	}
	if len(document.CanonicalJSON()) == 0 || len(document.SHA256()) != 64 {
		t.Fatal("public Organizer did not produce a bounded canonical identity")
	}
}

func TestPublicConfigurationDocumentExample(t *testing.T) {
	content, err := os.ReadFile(filepath.Join("..", "..", "..", "docs", "configuration.md"))
	if err != nil {
		t.Fatalf("read public configuration guide: %v", err)
	}
	section := string(content)
	marker := "## Version 1 document"
	if index := strings.Index(section, marker); index >= 0 {
		section = section[index+len(marker):]
	} else {
		t.Fatal("public configuration guide lacks the version 1 example")
	}
	start := strings.Index(section, "```json\n")
	if start < 0 {
		t.Fatal("public configuration guide lacks a JSON example")
	}
	section = section[start+len("```json\n"):]
	end := strings.Index(section, "\n```")
	if end < 0 {
		t.Fatal("public configuration JSON example is not closed")
	}
	if _, err := configurationdomain.Parse([]byte(section[:end])); err != nil {
		t.Fatalf("parse public configuration guide example: %v", err)
	}
}
