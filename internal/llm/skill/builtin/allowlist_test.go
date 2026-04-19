package builtin

import "testing"

// TestDefaultAllowlistExcludesDestructive pins the safety contract the
// package doc promises: destructive or privilege-escalating commands must
// never appear in DefaultAllowlist. This guards against accidental
// additions when the list is edited.
func TestDefaultAllowlistExcludesDestructive(t *testing.T) {
	forbidden := []string{
		"rm", "rmdir", "mv", "sudo", "doas", "su",
		"dd", "shred", "chmod", "chown", "chgrp",
		"kill", "killall", "pkill", "reboot", "shutdown", "halt", "poweroff",
		"mount", "umount", "mkfs",
	}
	set := make(map[string]struct{}, len(DefaultAllowlist))
	for _, n := range DefaultAllowlist {
		set[n] = struct{}{}
	}
	for _, bad := range forbidden {
		if _, ok := set[bad]; ok {
			t.Errorf("DefaultAllowlist unexpectedly contains %q", bad)
		}
	}
}

// TestDefaultAllowlistHasNoDuplicates ensures the list stays a clean set;
// duplicates usually signal a merge mistake.
func TestDefaultAllowlistHasNoDuplicates(t *testing.T) {
	seen := make(map[string]struct{}, len(DefaultAllowlist))
	for _, n := range DefaultAllowlist {
		if _, ok := seen[n]; ok {
			t.Errorf("DefaultAllowlist contains duplicate entry %q", n)
		}
		seen[n] = struct{}{}
	}
}

// TestDefaultAllowlistHasNoEmptyEntries catches editing mistakes such as a
// stray "" left after removing a name.
func TestDefaultAllowlistHasNoEmptyEntries(t *testing.T) {
	for i, n := range DefaultAllowlist {
		if n == "" {
			t.Errorf("DefaultAllowlist[%d] is empty", i)
		}
	}
}
