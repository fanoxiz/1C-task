package postgres

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"
)

type serverLock struct {
	mu     sync.Mutex
	config *pgx.ConnConfig
	conn   *pgx.Conn
	closed bool
}

func (lock *serverLock) ensure(ctx context.Context) error {
	lock.mu.Lock()
	defer lock.mu.Unlock()
	if lock.closed {
		return fmt.Errorf("server lock closed")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if lock.conn != nil {
		if err := lock.conn.Ping(ctx); err == nil {
			return nil
		}
		_ = lock.conn.Close(ctx)
		lock.conn = nil
	}
	conn, err := pgx.ConnectConfig(ctx, lock.config.Copy())
	if err != nil {
		return err
	}
	var acquired bool
	err = conn.QueryRow(ctx, "SELECT pg_try_advisory_lock(762018002)").Scan(&acquired)
	if err != nil || !acquired {
		_ = conn.Close(ctx)
		if err != nil {
			return err
		}
		return fmt.Errorf("another Mobius server is already using this database")
	}
	lock.conn = conn
	return nil
}

// Keep the lock separate from pooled queries and reacquire it after a DB restart.
func (s *IdentityStore) LockServer(ctx context.Context) (func(), error) {
	lock := &serverLock{config: s.pool.Config().ConnConfig.Copy()}
	if err := lock.ensure(ctx); err != nil {
		return nil, err
	}
	s.serverLock = lock
	return func() {
		lock.mu.Lock()
		defer lock.mu.Unlock()
		lock.closed = true
		if lock.conn != nil {
			closeCtx, cancel := context.WithTimeout(context.Background(), time.Second)
			defer cancel()
			_ = lock.conn.Close(closeCtx)
			lock.conn = nil
		}
	}, nil
}

func (s *IdentityStore) CheckServer(ctx context.Context) error {
	if s.serverLock == nil {
		return nil
	}
	ctx, cancel := context.WithTimeout(ctx, time.Second)
	defer cancel()
	return s.serverLock.ensure(ctx)
}
