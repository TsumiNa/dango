package engine

import (
	"bytes"
	"log/slog"
	"strings"
	"testing"
)

func TestSkillMountAliasRoutesUnderAlias(t *testing.T) {
	o := newOrchestrator(testLogger)

	// Create a skill named "writer" and mount it under alias "my-writer"
	reg := newTestSkillRegistration(t, "writer", "Intrinsic writer", nil)
	reg.Alias = "my-writer"

	mustAddSkills(t, o, reg)

	skills := o.Skills()
	if skills["writer"] != nil {
		t.Fatal("expected skill to not be registered under intrinsic name 'writer'")
	}

	storedReg := skills["my-writer"]
	if storedReg == nil {
		t.Fatal("expected skill to be registered under alias 'my-writer'")
	}

	if storedReg.Name != "my-writer" {
		t.Errorf("storedReg.Name = %q, want 'my-writer'", storedReg.Name)
	}
}

func TestSkillConflictPrefersUserSkillAndWarns(t *testing.T) {
	var buf bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&buf, nil))
	o := newOrchestrator(logger)

	// 1. Register system skill named "writer"
	sysReg := newTestSkillRegistration(t, "writer", "System writer", nil)
	sysReg.IsUserSupplied = false

	// 2. Register user skill named "writer"
	userReg := newTestSkillRegistration(t, "writer", "User writer", nil)
	userReg.IsUserSupplied = true

	// Add both to the orchestrator (should resolve to user skill and log a warning)
	mustAddSkills(t, o, sysReg, userReg)

	skills := o.Skills()
	storedReg := skills["writer"]
	if storedReg == nil {
		t.Fatal("expected 'writer' to be registered")
	}

	// Verify that the user-supplied skill won (User writer description)
	if storedReg.Description != "User writer" {
		t.Errorf("storedReg.Description = %q, want 'User writer'", storedReg.Description)
	}

	// Verify the collision warning was logged
	logOutput := buf.String()
	if !strings.Contains(logOutput, "collision") && !strings.Contains(logOutput, "precedence") {
		t.Errorf("expected warning in logs about collision, got:\n%s", logOutput)
	}
}

func TestSkillConflictBothUserSuppliedIsError(t *testing.T) {
	o := newOrchestrator(testLogger)

	// 1. Two user-supplied skills with the same name
	userReg1 := newTestSkillRegistration(t, "writer", "User writer 1", nil)
	userReg1.IsUserSupplied = true

	userReg2 := newTestSkillRegistration(t, "writer", "User writer 2", nil)
	userReg2.IsUserSupplied = true

	if err := o.AddSkills(userReg1, userReg2); err == nil {
		t.Error("expected error when mounting multiple user-supplied skills with the same name, got nil")
	} else if !strings.Contains(err.Error(), "conflict") || !strings.Contains(err.Error(), "user-imported") {
		t.Errorf("expected error message to mention conflict and user-imported, got: %v", err)
	}

	// 2. Two system-provided skills with the same name (no user skill)
	sysReg1 := newTestSkillRegistration(t, "editor", "System editor 1", nil)
	sysReg1.IsUserSupplied = false

	sysReg2 := newTestSkillRegistration(t, "editor", "System editor 2", nil)
	sysReg2.IsUserSupplied = false

	if err := o.AddSkills(sysReg1, sysReg2); err == nil {
		t.Error("expected error when mounting multiple system-provided skills with the same name, got nil")
	} else if !strings.Contains(err.Error(), "conflict") || !strings.Contains(err.Error(), "system-provided") {
		t.Errorf("expected error message to mention conflict and system-provided, got: %v", err)
	}
}

func TestSkillConflictAcrossSeparateAddSkillsCalls(t *testing.T) {
	var buf bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&buf, nil))
	o := newOrchestrator(logger)

	// First call: register a system-supplied skill named "writer".
	sysReg := newTestSkillRegistration(t, "writer", "System writer", nil)
	sysReg.IsUserSupplied = false
	mustAddSkills(t, o, sysReg)

	if got := o.Skills()["writer"]; got == nil || got.Description != "System writer" {
		t.Fatalf("after first AddSkills, expected system 'writer' to be registered; got %+v", got)
	}

	// Second call: register a user-supplied skill that collides with the
	// existing system skill. The user skill should replace the system skill
	// and a collision warning should be emitted.
	userReg := newTestSkillRegistration(t, "writer", "User writer", nil)
	userReg.IsUserSupplied = true
	mustAddSkills(t, o, userReg)

	stored := o.Skills()["writer"]
	if stored == nil {
		t.Fatal("expected 'writer' to remain registered after second AddSkills")
	}
	if stored.Description != "User writer" {
		t.Errorf("stored.Description = %q, want 'User writer' (user-supplied skill should win cross-call collision)", stored.Description)
	}

	logOutput := buf.String()
	if !strings.Contains(logOutput, "collision") && !strings.Contains(logOutput, "precedence") {
		t.Errorf("expected cross-call collision warning, got logs:\n%s", logOutput)
	}
}

func TestSkillAliasResolvesConflictNoWarning(t *testing.T) {
	var buf bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&buf, nil))
	o := newOrchestrator(logger)

	// 1. Register system skill named "writer"
	sysReg := newTestSkillRegistration(t, "writer", "System writer", nil)
	sysReg.IsUserSupplied = false

	// 2. Register user skill named "writer" but with alias "user-writer"
	userReg := newTestSkillRegistration(t, "writer", "User writer", nil)
	userReg.IsUserSupplied = true
	userReg.Alias = "user-writer"

	// Both should mount under distinct names, without any name collision warning
	mustAddSkills(t, o, sysReg, userReg)

	skills := o.Skills()
	if skills["writer"] == nil {
		t.Fatal("expected system skill to be registered under 'writer'")
	}
	if skills["user-writer"] == nil {
		t.Fatal("expected user skill to be registered under 'user-writer'")
	}

	if skills["writer"].Description != "System writer" {
		t.Errorf("skills['writer'].Description = %q, want 'System writer'", skills["writer"].Description)
	}
	if skills["user-writer"].Description != "User writer" {
		t.Errorf("skills['user-writer'].Description = %q, want 'User writer'", skills["user-writer"].Description)
	}

	// Verify no collision warning was logged
	logOutput := buf.String()
	if strings.Contains(logOutput, "collision") || strings.Contains(logOutput, "precedence") {
		t.Errorf("expected no collision warnings in logs, got:\n%s", logOutput)
	}
}
