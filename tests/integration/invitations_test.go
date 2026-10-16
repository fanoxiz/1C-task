//go:build integration

package integration

import (
	"context"
	"errors"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"onec/internal/mobius/httpapi"
	"onec/internal/mobius/postgres"
	"onec/internal/mobius/service"
	"onec/internal/protocol"
	"onec/migrations"
)

type invitationFixture struct {
	t             *testing.T
	ctx           context.Context
	pool          *pgxpool.Pool
	store         *postgres.IdentityStore
	service       *service.Identity
	public, admin *httptest.Server
}

func invitationSetup(t *testing.T) *invitationFixture {
	t.Helper()
	ctx, pool := testDatabase(t)
	if err := migrations.Apply(ctx, pool); err != nil {
		t.Fatal(err)
	}
	store := postgres.NewIdentityStore(pool)
	svc := service.NewIdentity(store)
	ready := func(context.Context) error { return nil }
	f := &invitationFixture{t: t, ctx: ctx, pool: pool, store: store, service: svc,
		public: httptest.NewServer(httpapi.Loki(ready, svc)), admin: httptest.NewServer(httpapi.Mobius(ready, svc))}
	t.Cleanup(f.public.Close)
	t.Cleanup(f.admin.Close)
	return f
}

func (f *invitationFixture) exec(sql string, args ...any) {
	f.t.Helper()
	if _, err := f.pool.Exec(f.ctx, sql, args...); err != nil {
		f.t.Fatal(err)
	}
}

func (f *invitationFixture) id() string {
	f.t.Helper()
	var id string
	if err := f.pool.QueryRow(f.ctx, "SELECT gen_random_uuid()::text").Scan(&id); err != nil {
		f.t.Fatal(err)
	}
	return id
}

func (f *invitationFixture) register() protocol.Registration {
	f.t.Helper()
	return decode[protocol.Registration](f.t, jsonRequest(f.t, "POST", f.public.URL+"/api/v1/loki/register", "", map[string]string{"display_name": "Loki"}, 201))
}

func (f *invitationFixture) invite(owner, request, side string, status int) []byte {
	f.t.Helper()
	return jsonRequest(f.t, "POST", f.admin.URL+"/api/v1/mobius/loki/"+owner+"/invitations", "", map[string]string{"request_id": request, "mobius_side": side}, status)
}

func (f *invitationFixture) respond(token, id, decision string, status int) []byte {
	f.t.Helper()
	return jsonRequest(f.t, "POST", f.public.URL+"/api/v1/loki/invitations/"+id+"/response", token, map[string]string{"decision": decision}, status)
}

func (f *invitationFixture) state(token string) protocol.State {
	f.t.Helper()
	return decode[protocol.State](f.t, jsonRequest(f.t, "GET", f.public.URL+"/api/v1/loki/state", token, nil, 200))
}

func (f *invitationFixture) events(token string) protocol.EventBatch {
	f.t.Helper()
	return decode[protocol.EventBatch](f.t, jsonRequest(f.t, "GET", f.public.URL+"/api/v1/loki/events", token, nil, 200))
}

func (f *invitationFixture) count(sql string, args ...any) int {
	f.t.Helper()
	var count int
	if err := f.pool.QueryRow(f.ctx, sql, args...).Scan(&count); err != nil {
		f.t.Fatal(err)
	}
	return count
}

func TestInvitationDeliveryAndDecisions(t *testing.T) {
	f := invitationSetup(t)
	a, b := f.register(), f.register()
	owner, request := a.State.Loki.ID, f.id()
	i := decode[protocol.Invitation](t, f.invite(owner, request, "X", 200))
	if i.Status != "pending" || i.ExpiresAt.Sub(i.CreatedAt) != 30*time.Second || i.Statistics.Played != 0 || i.Statistics.WinRate != nil {
		t.Fatalf("invalid invitation: %+v", i)
	}
	if repeated := decode[protocol.Invitation](t, f.invite(owner, request, "X", 200)); repeated.ID != i.ID {
		t.Fatal("request duplicated invitation")
	}
	if repeated := decode[protocol.Invitation](t, f.invite(strings.ToUpper(owner), strings.ToUpper(request), "X", 200)); repeated.ID != i.ID {
		t.Fatal("UUID casing broke idempotency")
	}
	f.invite(owner, request, "O", 409)
	f.invite(owner, f.id(), "X", 409)
	f.invite(owner, f.id(), "invalid", 400)
	batch, repeated := f.events(a.Token), f.events(a.Token)
	if len(batch.Items) != 1 || batch.Items[0].Type != "invitation_created" || repeated.Items[0].ID != batch.Items[0].ID {
		t.Fatal("unacked event was lost or duplicated")
	}
	if len(f.events(b.Token).Items) != 0 {
		t.Fatal("another Loki received the invitation")
	}
	f.respond(b.Token, i.ID, "accept", 404)
	jsonRequest(t, "POST", f.public.URL+"/api/v1/loki/events/"+batch.Items[0].ID+"/ack", b.Token, nil, 404)
	for range 2 {
		jsonRequest(t, "POST", f.public.URL+"/api/v1/loki/events/"+batch.Items[0].ID+"/ack", a.Token, nil, 204)
	}
	if len(f.events(a.Token).Items) != 0 || f.count("SELECT count(*) FROM game.events WHERE acked_at IS NOT NULL") != 1 {
		t.Fatal("ACK was not idempotent")
	}
	if own := f.state(a.Token); own.Invitation == nil || own.Invitation.ID != i.ID || own.Game != nil {
		t.Fatal("snapshot lost pending invitation")
	}
	declined := decode[protocol.Invitation](t, f.respond(a.Token, i.ID, "decline", 200))
	if declined.Status != "declined" {
		t.Fatal("decline not saved")
	}
	f.respond(a.Token, i.ID, "decline", 200)
	f.respond(a.Token, i.ID, "accept", 409)
	if f.count("SELECT count(*) FROM game.games") != 0 {
		t.Fatal("decline created a game")
	}
	i = decode[protocol.Invitation](t, f.invite(owner, f.id(), "O", 200))
	accepted := decode[protocol.Invitation](t, f.respond(a.Token, i.ID, "accept", 200))
	retry := decode[protocol.Invitation](t, f.respond(a.Token, i.ID, "accept", 200))
	if accepted.GameID == nil || retry.GameID == nil || *accepted.GameID != *retry.GameID {
		t.Fatal("repeat accept changed the game")
	}
	state := f.state(a.Token)
	if state.Invitation != nil || state.Game == nil || state.Game.MobiusSide != "O" || state.Game.LokiSide != "X" || state.Game.NextActor != "loki" {
		t.Fatalf("invalid side/turn: %+v", state)
	}
	f.invite(owner, f.id(), "X", 409)
	other := decode[protocol.Invitation](t, f.invite(b.State.Loki.ID, f.id(), "X", 200))
	f.respond(b.Token, other.ID, "accept", 200)
	if f.state(b.Token).Game.NextActor != "mobius" {
		t.Fatal("X should start")
	}
	listing := decode[protocol.LokiList](t, jsonRequest(t, "GET", f.admin.URL+"/api/v1/mobius/loki", "", nil, 200))
	for _, row := range listing.Items {
		if row.ID == owner && (row.Invitation == nil || row.Invitation.Status != "accepted" || row.Game == nil) {
			t.Fatal("admin snapshot is incomplete")
		}
	}
	jsonRequest(t, "POST", f.public.URL+"/api/v1/loki/disconnect", a.Token, nil, 204)
	jsonRequest(t, "POST", f.public.URL+"/api/v1/loki/connect", a.Token, nil, 200)
	if state := f.state(a.Token); state.Game != nil || state.Invitation != nil {
		t.Fatal("disconnect kept active state")
	}
	retry = decode[protocol.Invitation](t, f.respond(a.Token, i.ID, "accept", 200))
	if retry.GameID != nil || f.count("SELECT count(*) FROM game.games WHERE loki_id=$1", owner) != 0 {
		t.Fatal("retry resurrected deleted game")
	}
	if f.state(b.Token).Game == nil {
		t.Fatal("another Loki lost their game")
	}
	f.respond(a.Token, i.ID, "unknown", 400)
	jsonRequest(t, "GET", f.public.URL+"/api/v1/loki/events?limit=101", a.Token, nil, 400)
	jsonRequest(t, "GET", f.public.URL+"/api/v1/loki/events?limit=0", a.Token, nil, 400)
	jsonRequest(t, "POST", f.public.URL+"/api/v1/mobius/loki/"+owner+"/invitations", a.Token, map[string]string{}, 404)
}

func TestInvitationExpiryAndOfflineCleanup(t *testing.T) {
	f := invitationSetup(t)
	a := f.register()
	owner, request := a.State.Loki.ID, f.id()
	i := decode[protocol.Invitation](t, f.invite(owner, request, "X", 200))
	f.exec("UPDATE game.invitations SET created_at=clock_timestamp()-interval '31 seconds', expires_at=clock_timestamp() WHERE id=$1", i.ID)
	f.respond(a.Token, i.ID, "accept", 410)
	if prior := decode[protocol.Invitation](t, f.invite(owner, request, "X", 200)); prior.Status != "expired" {
		t.Fatal("expired invitation was recreated")
	}
	if f.count("SELECT count(*) FROM game.events WHERE type='invitation_closed'") != 1 || f.count("SELECT count(*) FROM game.games") != 0 {
		t.Fatal("expiry was not committed atomically")
	}
	i = decode[protocol.Invitation](t, f.invite(owner, f.id(), "O", 200))
	f.exec("UPDATE game.invitations SET created_at=clock_timestamp()-interval '31 seconds', expires_at=clock_timestamp() WHERE id=$1", i.ID)
	if err := f.store.Expire(f.ctx); err != nil {
		t.Fatal(err)
	}
	if f.count("SELECT count(*) FROM game.invitations WHERE id=$1 AND status='expired'", i.ID) != 1 {
		t.Fatal("worker did not expire invite")
	}
	i = decode[protocol.Invitation](t, f.invite(owner, f.id(), "O", 200))
	f.respond(a.Token, i.ID, "accept", 200)
	f.exec("UPDATE game.loki SET last_seen_at=clock_timestamp()-interval '31 seconds' WHERE id=$1", owner)
	jsonRequest(t, "GET", f.public.URL+"/api/v1/loki/state", a.Token, nil, 409)
	if f.count("SELECT count(*) FROM game.games WHERE loki_id=$1", owner) != 0 || f.count("SELECT count(*) FROM game.events WHERE type='game_cancelled'") != 1 {
		t.Fatal("late poll did not delete active game")
	}
	jsonRequest(t, "POST", f.public.URL+"/api/v1/loki/connect", a.Token, nil, 200)
	i = decode[protocol.Invitation](t, f.invite(owner, f.id(), "X", 200))
	f.exec("UPDATE game.loki SET last_seen_at=clock_timestamp()-interval '31 seconds' WHERE id=$1", owner)
	f.invite(owner, f.id(), "X", 409)
	if f.count("SELECT count(*) FROM game.invitations WHERE id=$1 AND status='cancelled'", i.ID) != 1 {
		t.Fatal("offline invitation did not cancel")
	}
	if f.count("SELECT count(*) FROM game.loki WHERE connected") != 0 {
		t.Fatal("Mobius request renewed presence")
	}
}

func TestInvitationConcurrentCommands(t *testing.T) {
	f := invitationSetup(t)
	a := f.register()
	request := f.id()
	type reply struct {
		invitation protocol.Invitation
		err        error
	}
	results := make(chan reply, 8)
	var wg sync.WaitGroup
	for range 8 {
		wg.Go(func() {
			invitation, err := f.service.Invite(f.ctx, a.State.Loki.ID, request, "O")
			results <- reply{invitation, err}
		})
	}
	wg.Wait()
	close(results)
	var id string
	for result := range results {
		if result.err != nil {
			t.Fatal(result.err)
		}
		if id != "" && result.invitation.ID != id {
			t.Fatal("concurrent duplicate created another invitation")
		}
		id = result.invitation.ID
	}
	results = make(chan reply, 8)
	for range 8 {
		wg.Go(func() {
			invitation, err := f.service.Respond(f.ctx, a.Token, id, "accept")
			results <- reply{invitation, err}
		})
	}
	wg.Wait()
	close(results)
	var gameID string
	for result := range results {
		if result.err != nil || result.invitation.GameID == nil {
			t.Fatalf("accept: %+v", result)
		}
		if gameID != "" && gameID != *result.invitation.GameID {
			t.Fatal("concurrent accept created another game")
		}
		gameID = *result.invitation.GameID
	}
	if f.count("SELECT count(*) FROM game.games") != 1 || f.count("SELECT count(*) FROM game.events WHERE type='game_started'") != 1 {
		t.Fatal("game creation was duplicated")
	}
}

func TestInvitationDeadlineCheckedAfterLockWait(t *testing.T) {
	f := invitationSetup(t)
	a := f.register()
	i := decode[protocol.Invitation](t, f.invite(a.State.Loki.ID, f.id(), "X", 200))
	f.exec("UPDATE game.invitations SET expires_at=clock_timestamp()+interval '2 seconds' WHERE id=$1", i.ID)
	tx, err := f.pool.Begin(f.ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(context.Background())
	if _, err := tx.Exec(f.ctx, "SELECT id FROM game.invitations WHERE id=$1 FOR UPDATE", i.ID); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { _, err := f.service.Respond(f.ctx, a.Token, i.ID, "accept"); done <- err }()
	deadline := time.Now().Add(time.Second)
	for f.count("SELECT count(*) FROM pg_stat_activity WHERE wait_event_type='Lock' AND query LIKE '%FROM game.invitations WHERE id = $1 AND loki_id = $2 FOR UPDATE%'") == 0 {
		if time.Now().After(deadline) {
			t.Fatal("response did not reach the row lock before deadline")
		}
		time.Sleep(5 * time.Millisecond)
	}
	if _, err := tx.Exec(f.ctx, "SELECT pg_sleep(GREATEST(0,EXTRACT(EPOCH FROM (expires_at-clock_timestamp())))+0.05) FROM game.invitations WHERE id=$1", i.ID); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(f.ctx); err != nil {
		t.Fatal(err)
	}
	if err := <-done; !errors.Is(err, postgres.ErrExpired) {
		t.Fatalf("late response got %v", err)
	}
	if f.count("SELECT count(*) FROM game.games") != 0 {
		t.Fatal("late response created game")
	}
}

func TestInvitationEventFailureRollsBackCreation(t *testing.T) {
	f := invitationSetup(t)
	a := f.register()
	f.exec(`CREATE FUNCTION game.reject_event() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'test delivery failure'; END; $$;
		CREATE TRIGGER reject_event BEFORE INSERT ON game.events FOR EACH ROW EXECUTE FUNCTION game.reject_event()`)
	request := f.id()
	f.invite(a.State.Loki.ID, request, "X", 503)
	if f.count("SELECT count(*) FROM game.invitations") != 0 || f.count("SELECT event_sequence FROM game.loki WHERE id=$1", a.State.Loki.ID) != 0 {
		t.Fatal("event failure left a partial invitation")
	}
	f.exec("DROP TRIGGER reject_event ON game.events")
	f.invite(a.State.Loki.ID, request, "X", 200)
	if len(f.events(a.Token).Items) != 1 {
		t.Fatal("retry failed to create one durable event")
	}
}

func (f *invitationFixture) scores(owner string, wins, played int) {
	f.t.Helper()
	for n := range played {
		result := "mobius_win"
		if n < wins {
			result = "loki_win"
		} else if n%2 == 1 {
			result = "draw"
		}
		f.exec(`WITH invitation AS (INSERT INTO game.invitations
			(loki_id,request_id,mobius_side,status,expires_at,responded_at,statistics_snapshot)
			VALUES ($1,gen_random_uuid(),'X','accepted',clock_timestamp()+interval '30 seconds',clock_timestamp(),'{}') RETURNING id)
			INSERT INTO game.games (loki_id,invitation_id,mobius_side,status,result,created_at,finished_at)
			SELECT $1,id,'X','completed',$2,clock_timestamp()-interval '1 second',clock_timestamp() FROM invitation`, owner, result)
	}
}

func TestInvitationStatisticsSnapshotAndRestart(t *testing.T) {
	f := invitationSetup(t)
	a, b, c, d, newbie := f.register(), f.register(), f.register(), f.register(), f.register()
	f.scores(a.State.Loki.ID, 1, 2)
	first := decode[protocol.Invitation](t, f.invite(a.State.Loki.ID, f.id(), "X", 200))
	if first.Statistics.BetterPercent != nil || first.Statistics.Message != "Пока недостаточно данных для сравнения" {
		t.Fatal("no eligible opponents handled incorrectly")
	}
	f.respond(a.Token, first.ID, "decline", 200)
	f.scores(b.State.Loki.ID, 3, 4)
	f.scores(c.State.Loki.ID, 1, 2)
	f.scores(d.State.Loki.ID, 0, 2)
	jsonRequest(t, "POST", f.public.URL+"/api/v1/loki/disconnect", b.Token, nil, 204)
	request := f.id()
	i := decode[protocol.Invitation](t, f.invite(a.State.Loki.ID, request, "O", 200))
	if i.Statistics.Wins != 1 || i.Statistics.Played != 2 || i.Statistics.WinRate == nil || *i.Statistics.WinRate != 0.5 || i.Statistics.BetterPercent == nil || *i.Statistics.BetterPercent != 33 {
		t.Fatalf("wrong aggregate: %+v", i.Statistics)
	}
	f.scores(b.State.Loki.ID, 0, 8)
	repeated := decode[protocol.Invitation](t, f.invite(a.State.Loki.ID, request, "O", 200))
	if repeated.Statistics.BetterPercent == nil || *repeated.Statistics.BetterPercent != 33 {
		t.Fatal("retry recalculated statistics")
	}
	for _, event := range f.events(a.Token).Items {
		if event.Type == "invitation_created" {
			payload := decode[protocol.Invitation](t, event.Payload)
			if payload.ID == i.ID && (payload.Statistics.BetterPercent == nil || *payload.Statistics.BetterPercent != 33) {
				t.Fatal("event snapshot changed")
			}
		}
		if strings.Contains(string(event.Payload), b.State.Loki.ID) || strings.Contains(string(event.Payload), c.State.Loki.ID) {
			t.Fatal("aggregate exposed another Loki")
		}
	}
	newInvite := decode[protocol.Invitation](t, f.invite(newbie.State.Loki.ID, f.id(), "X", 200))
	if newInvite.Statistics.Played != 0 || newInvite.Statistics.BetterPercent != nil || newInvite.Statistics.Message != "Вы ещё не сыграли ни одной партии. Самое время начать!" {
		t.Fatal("newbie message incorrect")
	}
	f.respond(a.Token, i.ID, "accept", 200)
	completed := f.count("SELECT count(*) FROM game.games WHERE status='completed'")
	if err := f.service.ResetPresence(f.ctx); err != nil {
		t.Fatal(err)
	}
	if f.count("SELECT count(*) FROM game.games WHERE status='active'") != 0 || f.count("SELECT count(*) FROM game.invitations WHERE status='pending'") != 0 || f.count("SELECT count(*) FROM game.games WHERE status='completed'") != completed {
		t.Fatal("restart cleanup damaged lifecycle/history")
	}
	if f.count("SELECT count(*) FROM game.events WHERE type='game_cancelled' AND acked_at IS NULL") != 1 {
		t.Fatal("restart lost cancellation notification")
	}
	jsonRequest(t, "POST", f.public.URL+"/api/v1/loki/connect", a.Token, nil, 200)
	if restored := f.state(a.Token); restored.Loki.ID != a.State.Loki.ID || restored.Game == nil || restored.Game.Status != "completed" {
		t.Fatal("restart lost identity or revived game")
	}
}

func TestServerLockRejectsSecondInstance(t *testing.T) {
	f := invitationSetup(t)
	release, err := f.store.LockServer(f.ctx)
	if err != nil {
		t.Fatal(err)
	}
	if second, err := f.store.LockServer(f.ctx); err == nil {
		second()
		release()
		t.Fatal("second server acquired lock")
	}
	release()
	release, err = f.store.LockServer(f.ctx)
	if err != nil {
		t.Fatal("server lock was not released")
	}
	release()
}

func TestServerLockRecoversOnlyWhenStillTheOwner(t *testing.T) {
	f := invitationSetup(t)
	release, err := f.store.LockServer(f.ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	cancelled, cancel := context.WithCancel(f.ctx)
	cancel()
	if err := f.store.CheckServer(cancelled); !errors.Is(err, context.Canceled) {
		t.Fatal("cancelled check did not stop")
	}
	if otherRelease, err := postgres.NewIdentityStore(f.pool).LockServer(f.ctx); err == nil {
		otherRelease()
		t.Fatal("cancelled request released the server lock")
	}
	terminate := func() {
		f.exec(`SELECT pg_terminate_backend(pid) FROM pg_locks WHERE locktype='advisory'
			AND classid=0 AND objid=762018002 AND database=(SELECT oid FROM pg_database WHERE datname=current_database())`)
		eventually(t, func() bool {
			return f.count(`SELECT count(*) FROM pg_locks WHERE locktype='advisory'
			AND classid=0 AND objid=762018002 AND database=(SELECT oid FROM pg_database WHERE datname=current_database())`) == 0
		})
	}
	terminate()
	if err := f.store.CheckServer(f.ctx); err != nil {
		t.Fatal("lock did not recover after connection loss", err)
	}
	other := postgres.NewIdentityStore(f.pool)
	if second, err := other.LockServer(f.ctx); err == nil {
		second()
		t.Fatal("another instance stole a recovered lock")
	}
	terminate()
	second, err := other.LockServer(f.ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer second()
	if err := f.store.CheckServer(f.ctx); err == nil {
		t.Fatal("old instance may write while another instance owns the lock")
	}
}
