package main

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

const (
	notifyChannel    = "verdicts"
	keepalivePeriod  = 25 * time.Second
	maxListenBackoff = 30 * time.Second
)

type Broker struct {
	DB  *pgxpool.Pool
	Log *slog.Logger

	mu     sync.Mutex
	subs   map[chan string]struct{}
	closed bool
}

func newBroker(pool *pgxpool.Pool, log *slog.Logger) *Broker {
	return &Broker{DB: pool, Log: log, subs: make(map[chan string]struct{})}
}

func (b *Broker) Run(ctx context.Context) {
	defer b.close()

	backoff := time.Second

	for ctx.Err() == nil {
		connected, err := b.listen(ctx)
		if ctx.Err() != nil {
			return
		}

		if connected {
			backoff = time.Second
		}

		b.Log.Warn(
			"verdict listener stopped, retrying",
			"err", err,
			"backoff", backoff,
		)

		select {
		case <-ctx.Done():
			return
		case <-time.After(backoff):
		}

		backoff = min(backoff*2, maxListenBackoff)
	}
}

func (b *Broker) listen(ctx context.Context) (bool, error) {
	conn, err := pgx.Connect(ctx, b.DB.Config().ConnString())
	if err != nil {
		return false, fmt.Errorf("connect: %w", err)
	}

	defer func() { _ = conn.Close(context.Background()) }()

	if _, err = conn.Exec(ctx, "LISTEN "+notifyChannel); err != nil {
		return false, fmt.Errorf("listen: %w", err)
	}

	// Rows written while the listener was down were never announced, so every page refetches.
	b.publish("")

	for {
		notification, err := conn.WaitForNotification(ctx)
		if err != nil {
			return true, fmt.Errorf("wait for notification: %w", err)
		}

		b.publish(notification.Payload)
	}
}

func (b *Broker) Subscribe() (<-chan string, func()) {
	wakeups := make(chan string, 1)

	b.mu.Lock()
	defer b.mu.Unlock()

	if b.closed {
		close(wakeups)

		return wakeups, func() {}
	}

	b.subs[wakeups] = struct{}{}

	return wakeups, func() {
		b.mu.Lock()
		defer b.mu.Unlock()

		delete(b.subs, wakeups)
	}
}

func (b *Broker) publish(payload string) {
	b.mu.Lock()
	defer b.mu.Unlock()

	for wakeups := range b.subs {
		select {
		case wakeups <- payload:
		default:
			// A wake-up is already waiting and the page fetches everything newer than its cursor.
		}
	}
}

func (b *Broker) close() {
	b.mu.Lock()
	defer b.mu.Unlock()

	b.closed = true

	for wakeups := range b.subs {
		close(wakeups)
		delete(b.subs, wakeups)
	}
}

func (s *Server) events(w http.ResponseWriter, r *http.Request) {
	wakeups, unsubscribe := s.Events.Subscribe()
	defer unsubscribe()

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")

	control := http.NewResponseController(w)

	_, err := fmt.Fprint(w, ": connected\n\n")
	if err != nil {
		return
	}

	if err = control.Flush(); err != nil {
		return
	}

	keepalive := time.NewTicker(keepalivePeriod)
	defer keepalive.Stop()

	for {
		select {
		case <-r.Context().Done():
			return
		case <-keepalive.C:
			_, err = fmt.Fprint(w, ": keepalive\n\n")
		case revID, open := <-wakeups:
			if !open {
				return
			}

			_, err = fmt.Fprintf(w, "event: verdict\ndata: %s\n\n", revID)
		}

		if err != nil {
			return
		}

		if err = control.Flush(); err != nil {
			return
		}
	}
}
