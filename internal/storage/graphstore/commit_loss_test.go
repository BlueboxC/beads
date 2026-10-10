//go:build cgo

package graphstore

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	mysql "github.com/go-sql-driver/mysql"
)

func TestServerLostCommitResponse(t *testing.T) {
	portText := os.Getenv("BEADS_GRAPH_TEST_SERVER_PORT")
	if portText == "" {
		t.Skip("set BEADS_GRAPH_TEST_SERVER_PORT for ordinary released-server qualification")
	}
	port, err := strconv.Atoi(portText)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	o := testOptions(t)
	token, err := freshToken()
	if err != nil {
		t.Fatal(err)
	}
	o.Backend, o.Database = "server", "graph_loss_"+token
	o.ServerHost, o.ServerPort, o.ServerUser = "127.0.0.1", port, "root"
	if err := Init(ctx, o); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		cleanupCtx, stop := context.WithTimeout(context.Background(), 10*time.Second)
		defer stop()
		admin, err := openBackend(cleanupCtx, o, "")
		if err != nil {
			t.Error(err)
			return
		}
		if _, err = admin.db.ExecContext(cleanupCtx, "DROP DATABASE `"+o.Database+"`"); err != nil {
			t.Error(err)
		}
		if err = admin.Close(); err != nil {
			t.Error(err)
		}
	})
	proxyPort, observed := startCommitLossProxy(t, net.JoinHostPort(o.ServerHost, portText))
	proxied := o
	proxied.ServerPort = proxyPort
	s, err := OpenExisting(ctx, proxied)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := s.Close(); err != nil {
			t.Error(err)
		}
	})
	_, err = s.Create(ctx, CreateRequest{Path: "beads/uncertain", Title: "Accepted, response lost", Body: "Still complete", Actor: "test-author"})
	if !errors.Is(err, ErrOutcomeUnknown) || errors.Is(err, ErrConflict) {
		t.Fatalf("lost COMMIT response must be unknown, got %v", err)
	}
	select {
	case packet := <-observed:
		// This witness belongs to the fault harness, not the client. An OK
		// response proves this particular unknown outcome actually committed.
		if len(packet) == 0 || packet[0] != 0x00 {
			t.Fatalf("server did not acknowledge COMMIT with OK (payload length %d)", len(packet))
		}
	case <-ctx.Done():
		t.Fatal("proxy did not observe COMMIT response", ctx.Err())
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	direct, err := OpenExisting(ctx, o)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := direct.Close(); err != nil {
			t.Error(err)
		}
	})
	r, err := direct.Show(ctx, "beads/uncertain")
	if err != nil {
		t.Fatal(err)
	}
	if r.Properties.Title != "Accepted, response lost" || r.Properties.Body != "Still complete" || r.Attribution.Actor != "test-author" {
		t.Fatalf("wrong recovered record: %+v", r)
	}
	for _, table := range []string{"graph_preview_catalog", "graph_preview_payloads", "graph_preview_versions"} {
		var count int
		if err := direct.db.QueryRowContext(ctx, "SELECT COUNT(*) FROM "+table+" WHERE path='beads/uncertain'").Scan(&count); err != nil {
			t.Fatal(err)
		}
		if count != 1 {
			t.Fatalf("unknown outcome has %d rows in %s", count, table)
		}
	}
	if _, err := direct.Create(ctx, CreateRequest{Path: "beads/successor"}); err != nil {
		t.Fatal(err)
	}
	if _, err := direct.Show(ctx, "beads/uncertain"); err != nil {
		t.Fatal(err)
	}
}

// startCommitLossProxy is a single-client, plaintext, loopback fault control.
// Authentication and SQL packets are forwarded without inspection/logging,
// except exact COM_QUERY COMMIT. That command reaches the real server, whose
// response is consumed and withheld from the client. No mock engine is used.
func startCommitLossProxy(t *testing.T, target string) (int, <-chan []byte) {
	return startCommitProxy(t, target, nil)
}

type pendingCommitControl struct {
	held    chan struct{}
	release chan struct{}
}

func startCommitProxy(t *testing.T, target string, pending *pendingCommitControl) (int, <-chan []byte) {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	var mu sync.Mutex
	var front, back net.Conn
	done := make(chan struct{})
	stop := make(chan struct{})
	var stopOnce sync.Once
	stopProxy := func() { stopOnce.Do(func() { close(stop) }) }
	observed := make(chan []byte, 1)
	closeConnections := func() {
		mu.Lock()
		defer mu.Unlock()
		if front != nil {
			_ = front.Close()
		}
		if back != nil {
			_ = back.Close()
		}
	}
	t.Cleanup(func() {
		stopProxy()
		_ = listener.Close()
		closeConnections()
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			t.Error("test COMMIT proxy did not stop")
		}
	})
	go func() {
		defer close(done)
		defer closeConnections()
		client, err := listener.Accept()
		if err != nil {
			return
		}
		mu.Lock()
		front = client
		mu.Unlock()
		server, err := net.DialTimeout("tcp", target, 5*time.Second)
		if err != nil {
			return
		}
		mu.Lock()
		back = server
		mu.Unlock()
		_ = client.SetDeadline(time.Now().Add(45 * time.Second))
		_ = server.SetDeadline(time.Now().Add(45 * time.Second))
		var commitSent atomic.Bool
		uploadDone := make(chan struct{})
		go func() {
			defer close(uploadDone)
			closeOnReturn := true
			defer func() {
				if closeOnReturn {
					closeConnections()
				}
			}()
			for {
				packet, err := readTestMySQLPacket(client)
				if err != nil {
					return
				}
				payload := packet[4:]
				if len(payload) > 1 && payload[0] == 0x03 && strings.EqualFold(strings.TrimSpace(string(payload[1:])), "COMMIT") {
					commitSent.Store(true)
					if pending != nil {
						// The client has issued COMMIT and loses its transport,
						// while the real server's transaction remains alive.
						close(pending.held)
						_ = client.Close()
						select {
						case <-pending.release:
						case <-stop:
							return
						}
						if _, err := server.Write(packet); err != nil {
							return
						}
						// The response reader owns backend cleanup from here.
						closeOnReturn = false
						return
					}
				}
				if _, err := server.Write(packet); err != nil {
					return
				}
			}
		}()
		for {
			packet, err := readTestMySQLPacket(server)
			if err != nil {
				break
			}
			if commitSent.Load() {
				observed <- append([]byte(nil), packet[4:]...)
				break
			}
			if _, err := client.Write(packet); err != nil {
				break
			}
		}
		closeConnections()
		stopProxy()
		<-uploadDone
	}()
	return listener.Addr().(*net.TCPAddr).Port, observed
}

func TestServerUncertainWriterConflictsAfterSuccessor(t *testing.T) {
	portText := os.Getenv("BEADS_GRAPH_TEST_SERVER_PORT")
	if portText == "" {
		t.Skip("set BEADS_GRAPH_TEST_SERVER_PORT for ordinary released-server qualification")
	}
	port, err := strconv.Atoi(portText)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	o := testOptions(t)
	token, err := freshToken()
	if err != nil {
		t.Fatal(err)
	}
	o.Backend, o.Database = "server", "graph_pending_"+token
	o.ServerHost, o.ServerPort, o.ServerUser = "127.0.0.1", port, "root"
	if err := Init(ctx, o); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		cleanupCtx, stop := context.WithTimeout(context.Background(), 10*time.Second)
		defer stop()
		admin, err := openBackend(cleanupCtx, o, "")
		if err != nil {
			t.Error(err)
			return
		}
		if _, err = admin.db.ExecContext(cleanupCtx, "DROP DATABASE `"+o.Database+"`"); err != nil {
			t.Error(err)
		}
		if err = admin.Close(); err != nil {
			t.Error(err)
		}
	})
	pending := &pendingCommitControl{held: make(chan struct{}), release: make(chan struct{})}
	proxyPort, observed := startCommitProxy(t, net.JoinHostPort(o.ServerHost, portText), pending)
	proxied := o
	proxied.ServerPort = proxyPort
	uncertain, err := OpenExisting(ctx, proxied)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := uncertain.Close(); err != nil {
			t.Error(err)
		}
	})
	_, err = uncertain.Create(ctx, CreateRequest{Path: "beads/late", Title: "Must not publish after successor"})
	if !errors.Is(err, ErrOutcomeUnknown) || errors.Is(err, ErrConflict) {
		t.Fatalf("disconnected client: %v", err)
	}
	select {
	case <-pending.held:
	case <-ctx.Done():
		t.Fatal("COMMIT was not held", ctx.Err())
	}
	if err := uncertain.Close(); err != nil {
		t.Fatal(err)
	}
	successor, err := OpenExisting(ctx, o)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := successor.Close(); err != nil {
			t.Error(err)
		}
	})
	accepted, err := successor.Create(ctx, CreateRequest{Path: "beads/successor", Title: "Known accepted successor"})
	if err != nil {
		t.Fatal(err)
	}
	var acceptedGuard string
	if err := successor.db.QueryRowContext(ctx, `SELECT writer_token FROM graph_preview_scope WHERE singleton=1`).Scan(&acceptedGuard); err != nil {
		t.Fatal(err)
	}
	// Only now may the abandoned client transaction attempt publication.
	close(pending.release)
	select {
	case packet := <-observed:
		// Decode only the error code/state of this exact COMMIT response; no
		// general protocol decoder, authentication data, or server patch.
		if len(packet) < 9 || packet[0] != 0xff || packet[3] != '#' {
			t.Fatalf("late COMMIT did not return an error packet (length %d)", len(packet))
		}
		serverErr := &mysql.MySQLError{Number: binary.LittleEndian.Uint16(packet[1:3])}
		copy(serverErr.SQLState[:], packet[4:9])
		if err := classifyCommitError(serverErr); !errors.Is(err, ErrConflict) || errors.Is(err, ErrOutcomeUnknown) {
			t.Fatalf("late COMMIT response: %v", err)
		}
	case <-ctx.Done():
		t.Fatal("late COMMIT response missing", ctx.Err())
	}
	for _, table := range []string{"graph_preview_catalog", "graph_preview_payloads", "graph_preview_versions"} {
		var late, good int
		if err := successor.db.QueryRowContext(ctx, "SELECT COUNT(*) FROM "+table+" WHERE path='beads/late'").Scan(&late); err != nil {
			t.Fatal(err)
		}
		if err := successor.db.QueryRowContext(ctx, "SELECT COUNT(*) FROM "+table+" WHERE path='beads/successor'").Scan(&good); err != nil {
			t.Fatal(err)
		}
		if late != 0 || good != 1 {
			t.Fatalf("%s: late=%d accepted=%d", table, late, good)
		}
	}
	var finalGuard string
	if err := successor.db.QueryRowContext(ctx, `SELECT writer_token FROM graph_preview_scope WHERE singleton=1`).Scan(&finalGuard); err != nil {
		t.Fatal(err)
	}
	if finalGuard != acceptedGuard {
		t.Fatal("late writer replaced successor coordination token")
	}
	read, err := successor.Show(ctx, "beads/successor")
	if err != nil || read.Revision != accepted.Revision {
		t.Fatalf("successor changed: %+v %v", read, err)
	}
	if _, err := successor.Show(ctx, "beads/late"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("late writer is visible: %v", err)
	}
	if _, err := successor.Create(ctx, CreateRequest{Path: "beads/after-accounting"}); err != nil {
		t.Fatal(err)
	}
}

func readTestMySQLPacket(r io.Reader) ([]byte, error) {
	var header [4]byte
	if _, err := io.ReadFull(r, header[:]); err != nil {
		return nil, err
	}
	n := int(binary.LittleEndian.Uint32(header[:]) & 0x00ffffff)
	if n > 1<<20 {
		return nil, fmt.Errorf("test proxy packet exceeds 1 MiB")
	}
	packet := make([]byte, 4+n)
	copy(packet, header[:])
	_, err := io.ReadFull(r, packet[4:])
	return packet, err
}
