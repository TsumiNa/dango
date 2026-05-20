package builtin

import "testing"

// TestDefaultAllowlistExcludesDestructive pins the safety contract the
// package doc promises: destructive or privilege-escalating commands must
// never appear in defaultAllowlist. This guards against accidental
// additions when the list is edited.
func TestDefaultAllowlistExcludesDestructive(t *testing.T) {
	forbidden := []string{
		"rm", "rmdir", "mv", "sudo", "doas", "su",
		"dd", "shred", "chmod", "chown", "chgrp",
		"kill", "killall", "pkill", "reboot", "shutdown", "halt", "poweroff",
		"mount", "umount", "mkfs",
	}
	set := make(map[string]struct{}, len(defaultAllowlist))
	for _, n := range defaultAllowlist {
		set[n] = struct{}{}
	}
	for _, bad := range forbidden {
		if _, ok := set[bad]; ok {
			t.Errorf("defaultAllowlist unexpectedly contains %q", bad)
		}
	}
}

// TestDefaultAllowlistIncludesGit confirms that "git" is present in
// defaultAllowlist so that skills can run read-oriented git inspection.
func TestDefaultAllowlistIncludesGit(t *testing.T) {
	for _, n := range defaultAllowlist {
		if n == "git" {
			return
		}
	}
	t.Error("defaultAllowlist does not contain \"git\"")
}

// TestDefaultAllowlistHasNoDuplicates ensures the list stays a clean set;
// duplicates usually signal a merge mistake.
func TestDefaultAllowlistHasNoDuplicates(t *testing.T) {
	seen := make(map[string]struct{}, len(defaultAllowlist))
	for _, n := range defaultAllowlist {
		if _, ok := seen[n]; ok {
			t.Errorf("defaultAllowlist contains duplicate entry %q", n)
		}
		seen[n] = struct{}{}
	}
}

// TestDefaultAllowlistHasNoEmptyEntries catches editing mistakes such as a
// stray "" left after removing a name.
func TestDefaultAllowlistHasNoEmptyEntries(t *testing.T) {
	for i, n := range defaultAllowlist {
		if n == "" {
			t.Errorf("defaultAllowlist[%d] is empty", i)
		}
	}
}
