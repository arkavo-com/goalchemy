package project

import (
	"os"
	"path/filepath"
	"testing"
)

func TestCompactNamesConfiguration(t *testing.T) {
	for _, tc := range []struct {
		value   string
		want    bool
		invalid bool
	}{
		{"", false, false}, {"compact_names: true\n", true, false},
		{"compact_names: false\n", false, false}, {"compact_names: yes\n", false, true},
	} {
		t.Run(tc.value, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), FileName)
			data := "schema_version: 1\npackages: [.]\n" + tc.value + "targets:\n  go: {out: out/go}\n"
			if err := os.WriteFile(path, []byte(data), 0o600); err != nil {
				t.Fatal(err)
			}
			c, err := Load(path)
			if tc.invalid {
				if err == nil {
					t.Fatal("accepted non-boolean compact_names")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if c.CompactNames != tc.want {
				t.Fatalf("compact_names = %v, want %v", c.CompactNames, tc.want)
			}
		})
	}
}

func TestNamesConfiguration(t *testing.T) {
	for _, tc := range []struct {
		value, mode, policy string
		invalid             bool
	}{
		{"", "", "", false},
		{"names: readable\n", "readable", "", false},
		{"names: source\nname_collisions: fail\n", "source", "fail", false},
		{"compact_names: true\n", "compact", "", false},
		{"compact_names: true\nnames: compact\n", "compact", "", false},
		{"compact_names: true\nnames: source\n", "", "", true},
		{"names: short\n", "", "", true},
		{"name_collisions: ignore\n", "", "", true},
	} {
		t.Run(tc.value, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), FileName)
			data := "schema_version: 1\npackages: [.]\n" + tc.value + "targets:\n  go: {out: out/go}\n"
			if err := os.WriteFile(path, []byte(data), 0o600); err != nil {
				t.Fatal(err)
			}
			c, err := Load(path)
			if tc.invalid {
				if err == nil {
					t.Fatal("accepted invalid names configuration")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if c.NameMode() != tc.mode || c.NameCollisions != tc.policy {
				t.Fatalf("names = %q/%q, want %q/%q", c.NameMode(), c.NameCollisions, tc.mode, tc.policy)
			}
		})
	}
}
