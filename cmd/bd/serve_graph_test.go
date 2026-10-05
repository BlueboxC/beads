package main

import (
	"strings"
	"testing"
)

func TestServeGraphViewerRejectsOtherDatabaseSelectors(t *testing.T) {
	oldPath, oldDatabase, oldGlobal := dbPath, databaseFlag, globalFlag
	t.Cleanup(func() { dbPath, databaseFlag, globalFlag = oldPath, oldDatabase, oldGlobal })
	for _, flag := range []string{"--db", "--database", "--global"} {
		t.Run(flag, func(t *testing.T) {
			dbPath, databaseFlag, globalFlag = "", "", false
			switch flag {
			case "--db":
				dbPath = "foreign"
			case "--database":
				databaseFlag = "foreign"
			case "--global":
				globalFlag = true
			}
			if err := runGraphViewer(serveOptions{}); err == nil || !strings.Contains(err.Error(), "one discovered workspace") {
				t.Fatalf("selector %s: expected workspace refusal, got %v", flag, err)
			}
		})
	}
}
