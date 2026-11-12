package main

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"fmt"
	"log"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode"
)

type caller interface {
	call(context.Context, string, fields, any) error
}

type bot struct {
	api               caller
	s                 *store
	cfg               config
	botID             int64
	mu                sync.Mutex
	failed            error
	username, sticker string
}

const (
	chooseCategory = "Привет! Выбери тему чата."
	staleTicket    = "Обращение уже недоступно."
	closedTicket   = "Чат завершён."
)

func (b *bot) call(ctx context.Context, method string, params fields, out any) error {
	err := b.api.call(ctx, method, params, out)
	apiErr := telegramError(err)
	markup, ok := params["reply_markup"].(*keyboard)
	if apiErr == nil || apiErr.Code != 400 || !ok || markup == nil {
		return err
	}
	rows := make([][]button, len(markup.Rows))
	hadIcon := false
	for i, row := range markup.Rows {
		rows[i] = slices.Clone(row)
		for j := range rows[i] {
			hadIcon = hadIcon || rows[i][j].Icon != ""
			rows[i][j].Icon = ""
		}
	}
	if !hadIcon {
		return err
	}
	params["reply_markup"] = &keyboard{Rows: rows}
	return b.api.call(ctx, method, params, out)
}

func (b *bot) optional(ctx context.Context, method string, params fields) {
	if err := b.call(ctx, method, params, nil); err != nil && ctx.Err() == nil {
		log.Print(err)
	}
}

func (b *bot) send(ctx context.Context, id, topic int64, text string, k *keyboard) (int64, error) {
	p := fields{"chat_id": id, "text": text, "link_preview_options": fields{"is_disabled": true}}
	if topic > 1 {
		p["message_thread_id"] = topic
	}
	if k != nil {
		p["reply_markup"] = k
	}
	var sent struct {
		ID int64 `json:"message_id"`
	}
	err := b.call(ctx, "sendMessage", p, &sent)
	return sent.ID, err
}

func (b *bot) say(ctx context.Context, id, topic int64, text string, k *keyboard) int64 {
	msgID, err := b.send(ctx, id, topic, text, k)
	if err != nil && ctx.Err() == nil {
		log.Print(err)
	}
	return msgID
}

func (b *bot) edit(ctx context.Context, c *callback, text string, k *keyboard) error {
	return b.call(ctx, "editMessageText", fields{
		"chat_id": c.From.ID, "message_id": c.Message.ID, "text": text, "reply_markup": k,
	}, nil)
}

func (b *bot) editOrSend(ctx context.Context, c *callback, text string, k *keyboard) {
	if err := b.edit(ctx, c, text, k); err != nil {
		if apiErr := telegramError(err); apiErr != nil && strings.Contains(apiErr.Description, "message is not modified") {
			return
		}
		b.say(ctx, c.From.ID, c.Message.Topic, text, k)
	}
}

func (b *bot) answer(ctx context.Context, c *callback, text string) {
	p := fields{"callback_query_id": c.ID}
	if text != "" {
		p["text"] = text
	}
	b.optional(ctx, "answerCallbackQuery", p)
}

func (b *bot) admin(id int64) bool { return id == b.cfg.Owner || b.s.Admins[id] }

func (b *bot) side(t *ticket, chatID, topicID int64) (valid, user bool) {
	if t == nil || topicID <= 1 {
		return false, false
	}
	if t.UserID == chatID && t.UserTopic == topicID {
		return true, true
	}
	return t.AdminID == chatID && t.AdminTopic == topicID && b.admin(chatID), false
}

func (b *bot) handle(ctx context.Context, u update, album []*message) error {
	if u.Callback != nil {
		return b.handleCallback(ctx, u.Callback)
	}
	if len(album) != 0 {
		return b.handleMessages(ctx, album)
	}
	if u.Message != nil {
		return b.handleMessages(ctx, []*message{u.Message})
	}
	return nil
}

func (b *bot) handleMessages(ctx context.Context, messages []*message) error {
	m := messages[0]
	if !m.Chat.belongsTo(m.From) {
		return nil
	}
	for _, part := range messages[1:] {
		if part == nil || part.Chat != m.Chat || !part.Chat.belongsTo(part.From) ||
			part.Topic != m.Topic || part.MediaGroup != m.MediaGroup {
			return nil
		}
	}
	id := m.From.ID
	command := strings.Fields(m.Text)
	name := ""
	if len(command) != 0 {
		raw, username, _ := strings.Cut(command[0], "@")
		if username == "" || strings.EqualFold(username, b.username) {
			name = strings.ToLower(raw)
		}
	}
	switch name {
	case "/start":
		var invite string
		if len(command) == 2 && strings.HasPrefix(command[1], "invite_") {
			invite = strings.TrimPrefix(command[1], "invite_")
		}
		return b.start(ctx, m, invite)
	case "/admins":
		if id == b.cfg.Owner {
			if err := b.remember(m.From); err != nil {
				return err
			}
			b.ownerCommands(ctx)
			b.say(ctx, id, 0, "Команда", b.adminPanel(0))
		}
		return nil
	}
	t, _ := b.s.byTopic(id, m.Topic)
	valid, user := b.side(t, id, m.Topic)
	if !valid {
		if m.Topic <= 1 && m.HasContent {
			b.say(ctx, id, 0, "Выбери тему, чтобы начать чат.", b.categories())
		}
		return nil
	}
	if t.State == "closed" {
		if m.HasContent {
			var menu *keyboard
			if user {
				menu = b.newChat()
			}
			b.say(ctx, id, m.Topic, closedTicket, menu)
		}
		return nil
	}
	if name == "/close" {
		return b.closeTicket(ctx, t)
	}
	if name == "/game" {
		if t.State == "active" && b.cfg.MiniAppURL != "" {
			b.say(ctx, id, m.Topic, "Крестики-нолики", &keyboard{Rows: [][]button{{b.gameButton(t.ID)}}})
		}
		return nil
	}
	if m.HasContent {
		return b.relay(ctx, t, user, messages)
	}
	return nil
}

func (b *bot) remember(p person) error {
	name := strings.Join(strings.Fields(strings.Map(func(r rune) rune {
		if unicode.Is(unicode.Cf, r) {
			return -1
		}
		return r
	}, p.FirstName+" "+p.LastName)), " ")
	if name == "" {
		name = strconv.FormatInt(p.ID, 10)
	}
	runes := []rune(name)
	name = string(runes[:min(len(runes), 64)])
	if p.Username != "" {
		name += " (@" + p.Username + ")"
	}
	if b.s.People[p.ID] != name {
		b.s.People[p.ID] = name
		return b.s.save()
	}
	return nil
}

func (b *bot) start(ctx context.Context, m *message, invite string) error {
	id := m.From.ID
	ownerStarted := id == b.cfg.Owner && b.s.People[id] != ""
	if err := b.remember(m.From); err != nil {
		return err
	}
	if invite != "" {
		if b.admin(id) {
			b.say(ctx, id, 0, "Ты уже в команде.", nil)
			return b.refresh(ctx)
		}
		now := time.Now()
		if !b.s.Invites[invite].After(now) {
			b.say(ctx, id, 0, "Ссылка уже использована или истекла.", nil)
			return nil
		}
		if b.s.JoinRequests[id].Expires.After(now) {
			b.say(ctx, id, 0, "Запрос уже ждёт подтверждения.", nil)
			return nil
		}
		delete(b.s.Invites, invite)
		b.s.JoinRequests[id] = joinRequest{Key: invite, Expires: now.Add(24 * time.Hour)}
		if err := b.s.save(); err != nil {
			return err
		}
		b.say(ctx, b.cfg.Owner, 0, "Запрос в команду от "+b.s.People[id], b.joinButtons(id, invite))
		b.say(ctx, id, 0, "Запрос отправлен владельцу.", nil)
		return nil
	}
	if b.sticker != "" && m.Topic <= 1 {
		b.optional(ctx, "sendSticker", fields{"chat_id": id, "sticker": b.sticker})
	}
	b.say(ctx, id, 0, chooseCategory, b.categories())
	if id == b.cfg.Owner {
		if !ownerStarted {
			b.ownerCommands(ctx)
		}
		b.say(ctx, id, 0, "Команда", b.adminPanel(0))
	}
	if b.admin(id) {
		return b.refresh(ctx)
	}
	return nil
}

func (b *bot) callbackMatches(c *callback) bool {
	m := c.Message
	if c.ID == "" || c.Data == "" || len(c.Data) > 64 || m == nil || m.ID <= 0 ||
		m.Date <= 0 || !m.Chat.belongsTo(c.From) || !m.From.Bot || m.From.ID != b.botID || m.Markup == nil {
		return false
	}
	for _, row := range m.Markup.Rows {
		for _, button := range row {
			if button.Data == c.Data {
				return true
			}
		}
	}
	return false
}

func (b *bot) handleCallback(ctx context.Context, c *callback) error {
	if !b.callbackMatches(c) {
		if c.ID != "" {
			b.answer(ctx, c, staleTicket)
		}
		return nil
	}
	action, arg, _ := strings.Cut(c.Data, ":")
	rawID, rawScore, _ := strings.Cut(arg, ":")
	ticketID, _ := strconv.ParseInt(rawID, 10, 64)
	t := b.s.Tickets[ticketID]
	id, topic := c.From.ID, c.Message.Topic
	valid, user := b.side(t, id, topic)
	switch action {
	case "cat":
		for _, category := range b.cfg.Categories {
			if category.ID == arg {
				b.answer(ctx, c, "")
				return b.selectCategory(ctx, c, category)
			}
		}
	case "new":
		b.answer(ctx, c, "")
		b.say(ctx, id, topic, chooseCategory, b.categories())
		return nil
	case "take":
		if topic <= 1 && t != nil && t.State == "pending" && t.UserID != id && b.admin(id) && t.Notices[id] == c.Message.ID {
			b.answer(ctx, c, "")
			return b.accept(ctx, c, t)
		}
	case "close":
		if valid && t.State != "closed" {
			b.answer(ctx, c, "")
			if err := b.closeTicket(ctx, t); err != nil {
				return err
			}
			b.optional(ctx, "editMessageReplyMarkup", fields{
				"chat_id": id, "message_id": c.Message.ID, "reply_markup": &keyboard{Rows: [][]button{}},
			})
			return nil
		}
	case "rate":
		score, _ := strconv.Atoi(rawScore)
		if valid && user && t.State == "closed" && t.AdminID != 0 && t.Rating == 0 && score >= 1 && score <= 5 {
			b.answer(ctx, c, "")
			t.Rating = score
			if err := b.s.save(); err != nil {
				return err
			}
			b.editOrSend(ctx, c, "Спасибо за оценку 🤍", b.newChat())
			return nil
		}
	case "invite", "admins", "revoke", "confirm-revoke":
		if id == b.cfg.Owner {
			b.answer(ctx, c, "")
			return b.adminAction(ctx, c, action, ticketID)
		}
	case "join", "reject":
		if id == b.cfg.Owner && ticketID > 0 {
			req := b.s.JoinRequests[ticketID]
			if rawScore != "" && req.Key == rawScore && req.Expires.After(time.Now()) && !b.admin(ticketID) {
				b.answer(ctx, c, "")
				delete(b.s.JoinRequests, ticketID)
				if action == "join" {
					b.s.Admins[ticketID] = true
				}
				if err := b.s.save(); err != nil {
					return err
				}
				if action == "join" {
					b.editOrSend(ctx, c, b.s.People[ticketID]+" теперь в команде.", &keyboard{Rows: [][]button{}})
					b.say(ctx, ticketID, 0, "Ты в команде. Новые обращения будут приходить сюда.", nil)
					return b.refresh(ctx)
				}
				b.editOrSend(ctx, c, "Запрос отклонён.", &keyboard{Rows: [][]button{}})
				return nil
			}
		}
	}
	b.answer(ctx, c, staleTicket)
	return nil
}

func (b *bot) adminPanel(page int64) *keyboard {
	const perPage = 8
	ids := make([]int64, 0, len(b.s.Admins))
	for id, active := range b.s.Admins {
		if active && id != b.cfg.Owner {
			ids = append(ids, id)
		}
	}
	slices.Sort(ids)
	lastPage := int64(max(0, len(ids)-1) / perPage)
	page = min(max(page, 0), lastPage)
	start := int(page) * perPage
	rows := [][]button{{{
		Text: "Пригласить админа", Data: "invite", Icon: b.cfg.Emoji["invite"], Style: "primary",
	}}}
	if page == 0 {
		now := time.Now()
		var pending []int64
		for id, req := range b.s.JoinRequests {
			if req.Expires.After(now) && !b.admin(id) {
				pending = append(pending, id)
			}
		}
		slices.Sort(pending)
		for _, id := range pending[:min(len(pending), 12)] {
			buttons := b.joinButtons(id, b.s.JoinRequests[id].Key).Rows[0]
			buttons[0].Text = "Добавить " + b.s.People[id]
			rows = append(rows, buttons)
		}
	}
	for _, id := range ids[start:min(start+perPage, len(ids))] {
		name := []rune(b.s.People[id])
		if len(name) == 0 {
			name = []rune(strconv.FormatInt(id, 10))
		}
		rows = append(rows, []button{{Text: string(name[:min(len(name), 48)]), Data: fmt.Sprintf("revoke:%d", id)}})
	}
	var nav []button
	if page > 0 {
		nav = append(nav, button{Text: "← Назад", Data: fmt.Sprintf("admins:%d", page-1)})
	}
	if page < lastPage {
		nav = append(nav, button{Text: "Дальше →", Data: fmt.Sprintf("admins:%d", page+1)})
	}
	if len(nav) > 0 {
		rows = append(rows, nav)
	}
	return &keyboard{Rows: rows}
}

func (b *bot) joinButtons(id int64, key string) *keyboard {
	return &keyboard{Rows: [][]button{{
		{Text: "Назначить админом", Data: fmt.Sprintf("join:%d:%s", id, key), Style: "success"},
		{Text: "Отклонить", Data: fmt.Sprintf("reject:%d:%s", id, key)},
	}}}
}

func (b *bot) adminAction(ctx context.Context, c *callback, action string, target int64) error {
	switch action {
	case "invite":
		random := make([]byte, 18)
		if _, err := rand.Read(random); err != nil {
			return err
		}
		now := time.Now()
		for token, expires := range b.s.Invites {
			if !expires.After(now) {
				delete(b.s.Invites, token)
			}
		}
		for id, req := range b.s.JoinRequests {
			if !req.Expires.After(now) {
				delete(b.s.JoinRequests, id)
			}
		}
		token := base64.RawURLEncoding.EncodeToString(random)
		b.s.Invites[token] = now.Add(24 * time.Hour)
		if err := b.s.save(); err != nil {
			return err
		}
		b.say(ctx, c.From.ID, c.Message.Topic, fmt.Sprintf(
			"Приглашение действует 24 часа и сработает один раз.\nhttps://t.me/%s?start=invite_%s", b.username, token), nil)
	case "admins":
		b.editOrSend(ctx, c, "Команда", b.adminPanel(target))
	case "revoke":
		if target != b.cfg.Owner && b.admin(target) {
			b.editOrSend(ctx, c, fmt.Sprintf("Снять доступ у «%s»? Текущие чаты этого админа завершатся.", b.s.People[target]),
				&keyboard{Rows: [][]button{{
					{Text: "Снять доступ", Style: "danger", Data: fmt.Sprintf("confirm-revoke:%d", target)},
					{Text: "Назад", Data: "admins:0"},
				}}})
		}
	case "confirm-revoke":
		if target != b.cfg.Owner && b.admin(target) {
			delete(b.s.Admins, target)
			if err := b.s.save(); err != nil {
				return err
			}
			if err := b.refresh(ctx); err != nil {
				return err
			}
		}
		b.editOrSend(ctx, c, "Команда", b.adminPanel(0))
	}
	return nil
}
