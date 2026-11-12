package main

import (
	"context"
	"slices"
	"strings"
	"time"
)

func (b *bot) relay(ctx context.Context, t *ticket, user bool, messages []*message) error {
	if user || b.cfg.Port != "" {
		queue, last, from, topic, _, _ := direction(t, user)
		group := messages[0].MediaGroup
		sameGroup := group != "" && len(*queue) != 0 && (*queue)[len(*queue)-1].Group == group
		var ids []int64
		for _, m := range messages {
			if m.ID > *last || (sameGroup && !slices.Contains((*queue)[len(*queue)-1].IDs, m.ID)) {
				ids = append(ids, m.ID)
			}
		}
		if len(ids) == 0 {
			return nil
		}
		slices.Sort(ids)
		ids = slices.Compact(ids)
		*last = max(*last, ids[len(ids)-1])
		part := queued{IDs: ids, Group: group}
		if b.cfg.Port != "" && group != "" {
			part.Ready = time.Now().Add(650 * time.Millisecond).UnixMilli()
		}
		if sameGroup {
			part.IDs = append((*queue)[len(*queue)-1].IDs, part.IDs...)
			slices.Sort(part.IDs)
			part.IDs = slices.Compact(part.IDs)
			(*queue)[len(*queue)-1] = part
		} else {
			*queue = append(*queue, part)
		}
		if err := b.s.save(); err != nil {
			return err
		}
		if t.State == "active" {
			retry, err := b.flushDirection(ctx, t, user)
			if retry {
				b.say(ctx, from, topic, "Сообщение пока в очереди.", nil)
			}
			return err
		}
		return nil
	}

	ids := make([]int64, 0, len(messages))
	for _, m := range messages {
		ids = append(ids, m.ID)
	}
	slices.Sort(ids)
	n, err := b.copy(ctx, t.AdminID, t.UserID, t.UserTopic, ids)
	switch {
	case err != nil && !uncopyable(err):
		b.say(ctx, t.AdminID, t.AdminTopic, "Сообщение не доставлено. Попробуй отправить его ещё раз.", nil)
	case n != len(ids):
		b.say(ctx, t.AdminID, t.AdminTopic, unsupportedMessage(n), nil)
	}
	return nil
}

func (b *bot) flush(ctx context.Context, t *ticket) (retry bool, err error) {
	first, err := b.flushDirection(ctx, t, true)
	if err != nil {
		return first, err
	}
	second, err := b.flushDirection(ctx, t, false)
	return first || second, err
}

func direction(t *ticket, user bool) (queue *[]queued, last *int64, from, fromTopic, to, toTopic int64) {
	if user {
		return &t.Queue, &t.LastUserMessage, t.UserID, t.UserTopic, t.AdminID, t.AdminTopic
	}
	return &t.AdminQueue, &t.LastAdminMessage, t.AdminID, t.AdminTopic, t.UserID, t.UserTopic
}

func (b *bot) flushDirection(ctx context.Context, t *ticket, user bool) (bool, error) {
	queue, _, from, fromTopic, to, toTopic := direction(t, user)
	for len(*queue) > 0 && ctx.Err() == nil {
		head := &(*queue)[0]
		if head.Ready > time.Now().UnixMilli() {
			break
		}
		ids := head.IDs
		n, err := b.copy(ctx, from, to, toTopic, ids)
		if err != nil && !uncopyable(err) {
			if b.cfg.Port != "" {
				delay := time.Minute
				if apiErr := telegramError(err); apiErr != nil && apiErr.RetryAfter > 0 {
					delay = max(delay, time.Duration(apiErr.RetryAfter)*time.Second)
				}
				head.Ready = time.Now().Add(delay).UnixMilli()
				return true, b.s.save()
			}
			return true, nil
		}
		*queue = (*queue)[1:]
		if err := b.s.save(); err != nil {
			return false, err
		}
		if n != len(ids) {
			b.say(ctx, from, fromTopic, unsupportedMessage(n), nil)
		}
	}
	return false, nil
}

func (b *bot) copy(ctx context.Context, from, to, topic int64, ids []int64) (int, error) {
	p := fields{"from_chat_id": from, "chat_id": to, "message_thread_id": topic}
	if len(ids) == 1 {
		p["message_id"] = ids[0]
		if err := b.call(ctx, "copyMessage", p, nil); err != nil {
			return 0, err
		}
		return 1, nil
	}
	p["message_ids"] = ids
	var copied []struct {
		ID int64 `json:"message_id"`
	}
	err := b.call(ctx, "copyMessages", p, &copied)
	return len(copied), err
}

func uncopyable(err error) bool {
	apiErr := telegramError(err)
	if apiErr == nil || apiErr.Code != 400 {
		return false
	}
	description := strings.ToLower(apiErr.Description)
	return strings.Contains(description, "can't be copied") ||
		strings.Contains(description, "cannot be copied") ||
		strings.Contains(description, "message to copy not found") ||
		strings.Contains(description, "message_id_invalid") ||
		strings.Contains(description, "protected content")
}

func unsupportedMessage(copied int) string {
	if copied > 0 {
		return "Telegram не смог отправить часть вложений."
	}
	return "Telegram не смог отправить это сообщение."
}
