//go:build cgo

package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/steveyegge/beads/internal/configfile"
)

func TestMigrationRejectsExternalPaths(t *testing.T) {
	for _, name := range []string{"../../../victim-project", `..\..\victim-project`, ".", "..", "/victim-project", "C:\\victim-project", "bad$name", "1-project", strings.Repeat("a", 65) + "-old"} {
		t.Run(name, func(t *testing.T) {
			base := t.TempDir()
			beadsDir := filepath.Join(base, "workspace", ".beads")
			dataDir := filepath.Join(beadsDir, "embeddeddolt")
			if err := os.MkdirAll(filepath.Join(dataDir, "__", "__", "__"), 0700); err != nil {
				t.Fatal(err)
			}
			victim := filepath.Join(base, "victim-project")
			if err := os.Mkdir(victim, 0700); err != nil {
				t.Fatal(err)
			}
			sentinel := filepath.Join(victim, "sentinel")
			if err := os.WriteFile(sentinel, []byte("private"), 0600); err != nil {
				t.Fatal(err)
			}
			cfg := &configfile.Config{DoltDatabase: name}
			if err := cfg.Save(beadsDir); err != nil {
				t.Fatal(err)
			}
			metadata := filepath.Join(beadsDir, "metadata.json")
			before, err := os.ReadFile(metadata)
			if err != nil {
				t.Fatal(err)
			}
			if err := migrateHyphenatedDB(beadsDir, cfg, name, sanitizeDBName(name)); err == nil {
				t.Error("unsafe migration accepted")
			}
			if got, err := os.ReadFile(sentinel); err != nil || string(got) != "private" {
				t.Errorf("external data moved or changed: %q, %v", got, err)
			}
			after, err := os.ReadFile(metadata)
			if err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(before, after) || cfg.DoltDatabase != name {
				t.Error("unsafe migration changed metadata")
			}
		})
	}
}

func TestMigrationRejectsSymlinkedDirectories(t *testing.T) {
	for _, component := range []string{"embeddeddolt", "old-name"} {
		t.Run(component, func(t *testing.T) {
			beadsDir := t.TempDir()
			outside := t.TempDir()
			dataDir := filepath.Join(beadsDir, "embeddeddolt")
			oldDir := filepath.Join(dataDir, "old-name")
			if component == "embeddeddolt" {
				if err := os.Mkdir(filepath.Join(outside, "old-name"), 0700); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(outside, dataDir); err != nil {
					t.Skipf("symlinks unavailable: %v", err)
				}
			} else {
				if err := os.Mkdir(dataDir, 0700); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(outside, oldDir); err != nil {
					t.Skipf("symlinks unavailable: %v", err)
				}
			}
			if err := migrateHyphenatedDB(beadsDir, nil, "old-name", "old_name"); err == nil {
				t.Error("symlink migration accepted")
			}
			if _, err := os.Lstat(oldDir); err != nil {
				t.Errorf("source changed: %v", err)
			}
			if _, err := os.Lstat(filepath.Join(dataDir, "old_name")); !os.IsNotExist(err) {
				t.Error("migration created destination")
			}
		})
	}
}

func TestMetadataOnlyMigrationRejectsSymlinkTarget(t *testing.T) {
	beadsDir := t.TempDir()
	dataDir := filepath.Join(beadsDir, "embeddeddolt")
	if err := os.Mkdir(dataDir, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(t.TempDir(), filepath.Join(dataDir, "old_name")); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	cfg := &configfile.Config{DoltDatabase: "old-name"}
	if err := cfg.Save(beadsDir); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(filepath.Join(beadsDir, "metadata.json"))
	if err != nil {
		t.Fatal(err)
	}
	if err := migrateHyphenatedDB(beadsDir, cfg, "old-name", "old_name"); err == nil {
		t.Error("symlink target accepted")
	}
	after, err := os.ReadFile(filepath.Join(beadsDir, "metadata.json"))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(before, after) || cfg.DoltDatabase != "old-name" {
		t.Error("unsafe target changed metadata")
	}
}
