package main

import (
	"context"
	"errors"
	"log"
	"time"
)

func poll(ctx context.Context, b *bot) error {
	var offset int64
	var refreshAt time.Time
	backoff := time.Second
	for ctx.Err() == nil {
		if time.Now().After(refreshAt) {
			if err := b.refreshLocked(ctx); err != nil {
				return err
			}
			refreshAt = time.Now().Add(time.Minute)
		}
		batch, err := fetchUpdates(ctx, b.api, offset, 25)
		if err != nil {
			if ctx.Err() != nil {
				break
			}
			delay := backoff
			if apiErr := telegramError(err); apiErr != nil {
				switch apiErr.Code {
				case 401:
					return errors.New("Telegram отклонил BOT_TOKEN")
				case 409:
					return errors.New("останови другой экземпляр бота или отключи его webhook")
				}
				if apiErr.RetryAfter > 0 {
					delay = max(delay, time.Duration(apiErr.RetryAfter)*time.Second)
				}
			}
			log.Printf("%v, повтор через %s", err, delay)
			waitFor(ctx, delay)
			backoff = min(30*time.Second, backoff*2)
			continue
		}
		backoff = time.Second
		batch = collectAlbumTail(ctx, b.api, offset, batch)
		for i := 0; i < len(batch) && ctx.Err() == nil; {
			u := batch[i]
			j := i + 1
			var album []*message
			if m := u.Message; m != nil && m.MediaGroup != "" {
				album = append(album, m)
				for j < len(batch) && sameAlbum(m, batch[j].Message) {
					album = append(album, batch[j].Message)
					j++
				}
			}
			b.mu.Lock()
			err := b.handle(ctx, u, album)
			b.mu.Unlock()
			if err != nil {
				return err
			}
			offset = batch[j-1].ID + 1
			i = j
		}
	}
	return nil
}

func fetchUpdates(ctx context.Context, api caller, offset int64, timeout int) ([]update, error) {
	var batch []update
	err := api.call(ctx, "getUpdates", fields{
		"offset": offset, "timeout": timeout, "allowed_updates": []string{"message", "callback_query"},
	}, &batch)
	return batch, err
}

func sameAlbum(a, b *message) bool {
	return b != nil && a.MediaGroup != "" && b.MediaGroup == a.MediaGroup && a.Chat.ID == b.Chat.ID && a.Topic == b.Topic
}

func collectAlbumTail(ctx context.Context, api caller, offset int64, batch []update) []update {
	if len(batch) == 0 || len(batch) >= 100 {
		return batch
	}
	last := batch[len(batch)-1].Message
	if last == nil || last.MediaGroup == "" || !waitFor(ctx, 350*time.Millisecond) {
		return batch
	}
	// Re-read at the same offset to avoid acknowledging unprocessed updates.
	ctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	full, err := fetchUpdates(ctx, api, offset, 0)
	if err == nil && len(full) > len(batch) && full[0].ID == batch[0].ID {
		return full
	}
	return batch
}

func waitFor(ctx context.Context, delay time.Duration) bool {
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-timer.C:
		return true
	}
}
