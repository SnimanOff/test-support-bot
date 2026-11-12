package main

import (
	"context"
	"errors"
	"fmt"
	"slices"
)

func (b *bot) refresh(ctx context.Context) error {
	for _, id := range b.s.orderedTickets() {
		if ctx.Err() != nil {
			break
		}
		t := b.s.Tickets[id]
		if t.State == "active" && !b.admin(t.AdminID) {
			if err := b.closeTicket(ctx, t); err != nil {
				return err
			}
			continue
		}
		if err := b.updateParticipants(ctx, t); err != nil {
			return err
		}
		if t.State != "pending" {
			if err := b.syncNotices(ctx, t); err != nil {
				return err
			}
		}
		switch t.State {
		case "pending":
			if err := b.notify(ctx, t); err != nil {
				return err
			}
		case "active":
			if _, err := b.flush(ctx, t); err != nil {
				return err
			}
		}
	}
	return nil
}

func (b *bot) topic(ctx context.Context, chatID int64, name string) (int64, error) {
	var topic struct {
		ID int64 `json:"message_thread_id"`
	}
	if err := b.call(ctx, "createForumTopic", fields{"chat_id": chatID, "name": name}, &topic); err != nil {
		return 0, err
	}
	if topic.ID <= 1 {
		return 0, errors.New("Telegram не вернул ID темы")
	}
	return topic.ID, nil
}

func (b *bot) saveNewTopic(ctx context.Context, chatID, topicID int64) error {
	if err := b.s.save(); err != nil {
		b.optional(ctx, "deleteForumTopic", fields{"chat_id": chatID, "message_thread_id": topicID})
		return err
	}
	return nil
}

func (b *bot) selectCategory(ctx context.Context, c *callback, category category) error {
	id := c.From.ID
	for _, t := range b.s.Tickets {
		if t.UserID == id && t.Origin == c.Message.ID {
			b.editOrSend(ctx, c, "Тема создана.", b.openButton(t.UserTopic))
			return nil
		}
	}
	topic, err := b.topic(ctx, id, category.Label)
	if err != nil {
		logTopicFailure(ctx, b, id, c.Message.Topic, err)
		return nil
	}
	t := &ticket{
		ID: b.s.NextID, UserID: id, UserTopic: topic, Origin: c.Message.ID,
		State: "pending", Category: category.Label, Notices: map[int64]int64{}, UserUpdate: true,
	}
	b.s.NextID++
	b.s.Tickets[t.ID] = t
	if err := b.saveNewTopic(ctx, id, topic); err != nil {
		return err
	}
	b.editOrSend(ctx, c, "Тема создана.", b.openButton(topic))
	if err := b.updateParticipants(ctx, t); err != nil {
		return err
	}
	return b.notify(ctx, t)
}

func noticeText(t *ticket) string {
	return fmt.Sprintf("Обращение №%d · %s", t.ID, t.Category)
}

func (b *bot) notify(ctx context.Context, t *ticket) error {
	if t.Notices == nil {
		t.Notices = map[int64]int64{}
	}
	changed := false
	for id, msg := range t.Notices {
		if !b.admin(id) {
			b.clearKeyboard(ctx, id, msg)
			delete(t.Notices, id)
			changed = true
		}
	}
	var ids []int64
	for id := range b.s.People {
		if id != t.UserID && b.admin(id) && t.Notices[id] == 0 {
			ids = append(ids, id)
		}
	}
	slices.Sort(ids)
	for _, id := range ids {
		if ctx.Err() != nil {
			break
		}
		msg := b.say(ctx, id, 0, noticeText(t), b.acceptButton(t.ID))
		if msg != 0 {
			t.Notices[id] = msg
			changed = true
		}
	}
	if changed {
		return b.s.save()
	}
	return nil
}

func (b *bot) accept(ctx context.Context, c *callback, t *ticket) error {
	id := c.From.ID
	if t.State != "pending" || id == t.UserID || !b.admin(id) {
		return nil
	}
	topic, err := b.topic(ctx, id, noticeText(t))
	if err != nil {
		logTopicFailure(ctx, b, id, 0, err)
		return nil
	}
	t.State, t.AdminID, t.AdminTopic = "active", id, topic
	t.AcceptedBy = b.s.People[id]
	t.UserUpdate, t.AdminUpdate = true, true
	if err := b.saveNewTopic(ctx, id, topic); err != nil {
		return err
	}
	if err := b.syncNotices(ctx, t); err != nil {
		return err
	}
	if err := b.updateParticipants(ctx, t); err != nil {
		return err
	}
	_, err = b.flush(ctx, t)
	return err
}

func (b *bot) closeTicket(ctx context.Context, t *ticket) error {
	if t.State == "closed" {
		return nil
	}
	pending := t.State == "pending"
	t.State, t.Queue, t.AdminQueue = "closed", nil, nil
	t.UserUpdate, t.AdminUpdate = true, !pending
	if err := b.s.save(); err != nil {
		return err
	}
	if err := b.syncNotices(ctx, t); err != nil {
		return err
	}
	return b.updateParticipants(ctx, t)
}

func (b *bot) syncNotices(ctx context.Context, t *ticket) error {
	if len(t.Notices) == 0 {
		return nil
	}
	text := noticeText(t) + "\nОбращение отменено."
	if t.AdminID != 0 {
		name := t.AcceptedBy
		if name == "" {
			name = b.s.People[t.AdminID]
		}
		if name == "" {
			name = fmt.Sprint(t.AdminID)
		}
		text = noticeText(t) + "\nЧат принял админ " + name
	}
	changed := false
	for admin, messageID := range t.Notices {
		if ctx.Err() != nil {
			break
		}
		k := &keyboard{Rows: [][]button{}}
		params := fields{"chat_id": admin, "message_id": messageID, "reply_markup": k}
		method := "editMessageReplyMarkup"
		if b.admin(admin) {
			method = "editMessageText"
			params["text"] = text
			if admin == t.AdminID {
				params["reply_markup"] = b.openButton(t.AdminTopic)
			}
		}
		if !retryTelegram(b.call(ctx, method, params, nil)) {
			delete(t.Notices, admin)
			changed = true
		}
	}
	if changed {
		return b.s.save()
	}
	return nil
}

func (b *bot) updateParticipants(ctx context.Context, t *ticket) error {
	if (!t.UserUpdate && !t.AdminUpdate) || ctx.Err() != nil {
		return nil
	}
	userBefore, adminBefore := t.UserUpdate, t.AdminUpdate
	if t.UserUpdate {
		var text string
		var k *keyboard
		switch t.State {
		case "pending":
			text, k = "Напиши сюда. Админ подключится, как только сможет.", b.closeButton(t.ID)
		case "active":
			text, k = "Админ подключился 💬", b.activeButtons(t.ID)
		case "closed":
			if t.AdminID == 0 {
				text, k = "Обращение отменено.", b.newChat()
			} else if t.Rating == 0 {
				text, k = "Чат завершён. Как тебе общение?", b.ratingButtons(t.ID)
			} else {
				text, k = closedTicket, b.newChat()
			}
		}
		t.UserUpdate = b.statusNeedsRetry(ctx, t.UserID, t.UserTopic, text, k)
	}
	if t.AdminUpdate {
		text := closedTicket
		var k *keyboard
		if t.State == "active" {
			text, k = "Чат принят. Ответь в этой теме.", b.activeButtons(t.ID)
		}
		t.AdminUpdate = b.statusNeedsRetry(ctx, t.AdminID, t.AdminTopic, text, k)
	}
	if userBefore != t.UserUpdate || adminBefore != t.AdminUpdate {
		return b.s.save()
	}
	return nil
}

func (b *bot) statusNeedsRetry(ctx context.Context, id, topic int64, text string, k *keyboard) bool {
	_, err := b.send(ctx, id, topic, text, k)
	return retryTelegram(err)
}

func retryTelegram(err error) bool {
	if err == nil {
		return false
	}
	apiErr := telegramError(err)
	return apiErr == nil || apiErr.Code == 0 || apiErr.Code == 429 || apiErr.Code >= 500
}

func (b *bot) clearKeyboard(ctx context.Context, id, messageID int64) {
	b.optional(ctx, "editMessageReplyMarkup", fields{
		"chat_id": id, "message_id": messageID, "reply_markup": &keyboard{Rows: [][]button{}},
	})
}

func logTopicFailure(ctx context.Context, b *bot, id, topic int64, err error) {
	if ctx.Err() != nil {
		return
	}
	text := "Не удалось открыть тему. Попробуй ещё раз."
	if apiErr := telegramError(err); apiErr != nil && apiErr.Code == 429 {
		text = "Telegram просит немного подождать. Попробуй ещё раз позже."
	}
	b.say(ctx, id, topic, text, nil)
}
