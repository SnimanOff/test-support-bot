package main

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"slices"
	"time"
)

func serve(ctx context.Context, b *bot) error {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	addr := b.cfg.MiniAppAddr
	if b.cfg.Port != "" {
		addr = net.JoinHostPort("127.0.0.1", b.cfg.Port)
	}
	listener, err := net.Listen("tcp", addr)
	if err != nil {
		return err
	}
	failures := make(chan error, 3)
	srv := &http.Server{
		Handler: b.httpHandler(failures), ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout: 10 * time.Second, WriteTimeout: 45 * time.Second, IdleTimeout: 30 * time.Second,
		BaseContext: func(net.Listener) context.Context { return ctx },
	}
	go func() { failures <- srv.Serve(listener) }()
	go func() {
		if b.cfg.Port == "" {
			failures <- poll(ctx, b)
			return
		}
		failures <- b.periodicRefresh(ctx)
	}()

	select {
	case err = <-failures:
	case <-ctx.Done():
	}
	cancel()
	shutdownCtx, stop := context.WithTimeout(context.Background(), 8*time.Second)
	defer stop()
	if stopErr := srv.Shutdown(shutdownCtx); stopErr != nil {
		_ = srv.Close()
	}
	if errors.Is(err, http.ErrServerClosed) {
		return nil
	}
	return err
}

func (b *bot) periodicRefresh(ctx context.Context) error {
	ticker := time.NewTicker(750 * time.Millisecond)
	defer ticker.Stop()
	var refreshAt time.Time
	for ctx.Err() == nil {
		if time.Now().After(refreshAt) {
			if err := b.refreshLocked(ctx); err != nil {
				return err
			}
			refreshAt = time.Now().Add(time.Minute)
		} else if err := b.flushWebhookQueues(ctx); err != nil {
			return err
		}
		select {
		case <-ticker.C:
		case <-ctx.Done():
		}
	}
	return nil
}

func (b *bot) flushWebhookQueues(ctx context.Context) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.failed != nil {
		return b.failed
	}
	for _, t := range b.s.Tickets {
		if t.State == "active" && b.admin(t.AdminID) && (len(t.Queue) != 0 || len(t.AdminQueue) != 0) {
			if _, err := b.flush(ctx, t); err != nil {
				return err
			}
		}
	}
	return nil
}

func (b *bot) refreshLocked(ctx context.Context) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.failed != nil {
		return b.failed
	}
	return b.refresh(ctx)
}

func (b *bot) httpHandler(failures chan<- error) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, "ok\n")
	})
	if b.cfg.WebhookSecret != "" {
		mux.HandleFunc("POST /{$}", func(w http.ResponseWriter, r *http.Request) {
			b.webhook(w, r, failures)
		})
	}
	if b.cfg.MiniAppURL != "" {
		b.registerGame(mux)
	}
	return mux
}

func (b *bot) webhook(w http.ResponseWriter, r *http.Request, failures chan<- error) {
	got := r.Header.Values("X-Telegram-Bot-Api-Secret-Token")
	if len(got) != 1 || subtle.ConstantTimeCompare([]byte(got[0]), []byte(b.cfg.WebhookSecret)) != 1 {
		w.WriteHeader(http.StatusUnauthorized)
		return
	}
	var u update
	if err := decodeJSON(w, r, 1<<20, &u); err != nil || u.ID <= 0 {
		w.WriteHeader(http.StatusBadRequest)
		return
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.failed != nil || r.Context().Err() != nil {
		w.WriteHeader(http.StatusServiceUnavailable)
		return
	}
	if slices.Contains(b.s.RecentUpdates, u.ID) {
		w.WriteHeader(http.StatusOK)
		return
	}
	err := b.handle(r.Context(), u, nil)
	if err == nil {
		const retained = 2048
		if len(b.s.RecentUpdates) >= retained {
			b.s.RecentUpdates = b.s.RecentUpdates[len(b.s.RecentUpdates)-retained+1:]
		}
		b.s.RecentUpdates = append(b.s.RecentUpdates, u.ID)
		err = b.s.save()
	}
	if err != nil {
		b.failed = err
		select {
		case failures <- err:
		default:
		}
		w.WriteHeader(http.StatusServiceUnavailable)
		return
	}
	w.WriteHeader(http.StatusOK)
}

func decodeJSON(w http.ResponseWriter, r *http.Request, limit int64, out any) error {
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, limit))
	if err := dec.Decode(out); err != nil {
		return err
	}
	if dec.Decode(new(json.RawMessage)) != io.EOF {
		return errors.New("лишние данные после JSON")
	}
	return nil
}
