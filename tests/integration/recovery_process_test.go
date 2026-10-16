//go:build integration

package integration

import (
	"bytes"
	"context"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"onec/internal/protocol"
)

type mobiusProcess struct {
	cmd    *exec.Cmd
	done   chan struct{}
	err    error
	output bytes.Buffer
	ui     string
	api    string
}

func startMobiusProcess(t *testing.T, binary string) *mobiusProcess {
	t.Helper()
	address := func() string {
		listener, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			t.Fatal(err)
		}
		defer listener.Close()
		return listener.Addr().String()
	}
	ui, api := address(), address()
	p := &mobiusProcess{cmd: exec.Command(binary), done: make(chan struct{}), ui: "http://" + ui, api: "http://" + api}
	p.cmd.Env = append(os.Environ(), "DATABASE_URL="+os.Getenv("TEST_DATABASE_URL"), "MOBIUS_LISTEN_ADDR="+ui, "LOKI_API_LISTEN_ADDR="+api)
	p.cmd.Stdout, p.cmd.Stderr = &p.output, &p.output
	if err := p.cmd.Start(); err != nil {
		t.Fatal(err)
	}
	go func() { p.err = p.cmd.Wait(); close(p.done) }()
	t.Cleanup(func() { p.stop(t) })
	return p
}

func (p *mobiusProcess) stop(t *testing.T) {
	t.Helper()
	select {
	case <-p.done:
		return
	default:
	}
	_ = p.cmd.Process.Signal(syscall.SIGTERM)
	select {
	case <-p.done:
		if p.err != nil {
			t.Errorf("Mobius shutdown: %v: %s", p.err, p.output.String())
		}
	case <-time.After(6 * time.Second):
		_ = p.cmd.Process.Kill()
		<-p.done
		t.Error("Mobius did not shut down within its deadline")
	}
}

func (p *mobiusProcess) ready(t *testing.T) {
	t.Helper()
	client := &http.Client{Timeout: 1500 * time.Millisecond}
	deadline := time.Now().Add(8 * time.Second)
	for time.Now().Before(deadline) {
		select {
		case <-p.done:
			t.Fatalf("Mobius exited before readiness: %v: %s", p.err, p.output.String())
		default:
		}
		response, err := client.Get(p.ui + "/readyz")
		if err == nil {
			response.Body.Close()
			if response.StatusCode == 200 {
				return
			}
		}
		time.Sleep(25 * time.Millisecond)
	}
	t.Fatal("Mobius readiness did not recover")
}

func TestRealServerRestartSecondInstanceAndDatabaseOutage(t *testing.T) {
	f := invitationSetup(t)
	binary := filepath.Join(t.TempDir(), "mobius")
	build := exec.CommandContext(f.ctx, "go", "build", "-buildvcs=false", "-o", binary, "./cmd/mobius")
	build.Dir = "../.."
	if output, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build Mobius: %v: %s", err, output)
	}
	a, b, c := f.register(), f.register(), f.register()
	completed := f.finish(a, "O", []int{0, 3, 1, 4, 2})
	first := startMobiusProcess(t, binary)
	first.ready(t)
	for _, player := range []protocol.Registration{a, b, c} {
		jsonRequest(t, "POST", first.api+"/api/v1/loki/connect", player.Token, nil, 200)
	}
	active := f.start(b, "X")
	f.move(b, active.ID, "mobius", moveRequest(f.id(), 1, 0), 200)
	pending := decode[protocol.Invitation](t, f.invite(c.State.Loki.ID, f.id(), "O", 200))
	second := startMobiusProcess(t, binary)
	select {
	case <-second.done:
		if second.err == nil || !strings.Contains(second.output.String(), "another Mobius server") {
			t.Fatalf("second instance did not reject database ownership: %v: %s", second.err, second.output.String())
		}
	case <-time.After(5 * time.Second):
		t.Fatal("second instance did not exit")
	}
	if f.count("SELECT count(*) FROM game.games WHERE id=$1 AND status='active'", active.ID) != 1 || f.count("SELECT count(*) FROM game.invitations WHERE id=$1 AND status='pending'", pending.ID) != 1 {
		t.Fatal("second instance cleaned the working server's activity")
	}
	// Opt in only with the name of a disposable PostgreSQL container serving TEST_DATABASE_URL.
	if container := os.Getenv("TEST_POSTGRES_CONTAINER"); container != "" {
		outageMove := moveRequest(f.id(), 2, 1)
		stopped := false
		defer func() {
			if stopped {
				ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
				defer cancel()
				if output, err := exec.CommandContext(ctx, "docker", "start", container).CombinedOutput(); err != nil {
					t.Errorf("restore test PostgreSQL: %v: %s", err, output)
					return
				}
				for f.pool.Ping(ctx) != nil {
					if ctx.Err() != nil {
						t.Error("test PostgreSQL did not recover for cleanup")
						return
					}
					time.Sleep(25 * time.Millisecond)
				}
			}
		}()
		if output, err := exec.CommandContext(f.ctx, "docker", "stop", "--time", "2", container).CombinedOutput(); err != nil {
			t.Fatalf("stop test PostgreSQL: %v: %s", err, output)
		}
		stopped = true
		jsonRequest(t, "GET", first.ui+"/healthz", "", nil, 200)
		jsonRequest(t, "GET", first.ui+"/readyz", "", nil, 503)
		jsonRequest(t, "POST", first.ui+"/api/v1/mobius/games/"+active.ID+"/moves", "", outageMove, 503)
		if output, err := exec.CommandContext(f.ctx, "docker", "start", container).CombinedOutput(); err != nil {
			t.Fatalf("start test PostgreSQL: %v: %s", err, output)
		}
		stopped = false
		first.ready(t)
		// The fixture's independent pool still contains connections from before the stop.
		f.pool.Reset()
		state := decode[protocol.State](t, jsonRequest(t, "GET", first.api+"/api/v1/loki/state", b.Token, nil, 200))
		if state.Game == nil || state.Game.ID != active.ID || len(state.Game.Moves) != 1 {
			t.Fatal("short database outage damaged the unfinished game")
		}
	} else {
		t.Log("database stop/start requires TEST_POSTGRES_CONTAINER pointing to a disposable test container")
	}
	first.stop(t)
	restarted := startMobiusProcess(t, binary)
	restarted.ready(t)
	if f.count("SELECT count(*) FROM game.games WHERE id=$1", active.ID) != 0 || f.count("SELECT count(*) FROM game.moves WHERE game_id=$1", active.ID) != 0 || f.count("SELECT count(*) FROM game.invitations WHERE id=$1 AND status='cancelled'", pending.ID) != 1 {
		t.Fatal("server restart did not clean unfinished activity")
	}
	state := decode[protocol.State](t, jsonRequest(t, "POST", restarted.api+"/api/v1/loki/connect", a.Token, nil, 200))
	if state.Loki.ID != a.State.Loki.ID || state.Game == nil || state.Game.ID != completed.ID || state.Game.Result != "loki_win" || state.Statistics.Played != 1 || state.Statistics.Wins != 1 {
		t.Fatal("server restart lost identity or completed result")
	}
	stored := decode[protocol.Game](t, jsonRequest(t, "GET", restarted.api+"/api/v1/loki/games/"+completed.ID, a.Token, nil, 200))
	if len(stored.Moves) != 5 || stored.Board[0][2] != "X" {
		t.Fatal("server restart lost the completed board/history")
	}
}
