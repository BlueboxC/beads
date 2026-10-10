package main

import (
	"context"
	"database/sql"
	"net"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"testing"
	"time"

	mysql "github.com/go-sql-driver/mysql"
	"github.com/steveyegge/beads/internal/configfile"
)

func isolateBeadsDirForTest(t *testing.T) { t.Helper(); t.Setenv("BEADS_DIR", "") }

var graphTestAuthority = regexp.MustCompile(`^[0-9a-f]{32}$`)

// Only fresh authority-named fixtures on the explicitly supplied test server
// are eligible; never resolve a user database or an operator credential here.
func cleanupGraphTestServer(t *testing.T, work string, args []string) {
	t.Helper()
	if len(args) == 0 || args[0] != "init" {
		return
	}
	port, err := strconv.Atoi(os.Getenv("BEADS_GRAPH_TEST_SERVER_PORT"))
	if err != nil || port <= 0 {
		return
	}
	cfg, err := configfile.LoadForDiscovery(filepath.Join(work, ".beads"))
	workspace, pathErr := filepath.EvalSymlinks(filepath.Join(work, ".beads"))
	if err != nil || cfg == nil || pathErr != nil || cfg.GraphMode != "link" ||
		cfg.GraphWorkspace != workspace || !graphTestAuthority.MatchString(cfg.GraphAuthorityID) ||
		cfg.DoltMode != configfile.DoltModeServer || cfg.DoltServerHost != "127.0.0.1" ||
		cfg.DoltServerPort != port || cfg.DoltDatabase != "beads_graph_"+cfg.GraphAuthorityID {
		return
	}
	database := cfg.DoltDatabase
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer cancel()
		connection := mysql.NewConfig()
		connection.User, connection.Net = "root", "tcp"
		connection.Addr = net.JoinHostPort("127.0.0.1", strconv.Itoa(port))
		connection.Timeout, connection.ReadTimeout, connection.WriteTimeout = 5*time.Second, 15*time.Second, 15*time.Second
		admin, err := sql.Open("mysql", connection.FormatDSN())
		if err != nil {
			t.Error(err)
			return
		}
		defer func() {
			if err := admin.Close(); err != nil {
				t.Error(err)
			}
		}()
		if _, err := admin.ExecContext(ctx, "DROP DATABASE IF EXISTS `"+database+"`"); err != nil {
			t.Error(err)
		}
	})
}
