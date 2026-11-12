package main

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

type request struct {
	method string
	params fields
}

type fakeTelegram struct {
	id    int64
	calls []request
	fail  func(string, fields) error
}

func (f *fakeTelegram) call(_ context.Context, method string, params fields, out any) error {
	f.calls = append(f.calls, request{method: method, params: params})
	if f.fail != nil {
		if err := f.fail(method, params); err != nil {
			return err
		}
	}
	f.id++
	var data any
	switch method {
	case "createForumTopic":
		data = fields{"message_thread_id": f.id}
	case "sendMessage", "copyMessage":
		data = fields{"message_id": f.id}
	case "copyMessages":
		copied := make([]fields, len(params["message_ids"].([]int64)))
		for i := range copied {
			copied[i] = fields{"message_id": f.id + int64(i)}
		}
		data = copied
	}
	if out == nil {
		return nil
	}
	encoded, err := json.Marshal(data)
	if err != nil {
		return err
	}
	return json.Unmarshal(encoded, out)
}

func TestAcceptanceNoticeRetries(t *testing.T) {
	b, api := testBot(t)
	ticket := &ticket{ID: 1, State: "active", UserID: 33, UserTopic: 101, AdminID: 11, AdminTopic: 102, UserUpdate: true}
	b.s.Tickets[1] = ticket
	if err := b.s.save(); err != nil {
		t.Fatal(err)
	}
	api.fail = func(method string, _ fields) error {
		return &apiError{Method: method, Code: 429, RetryAfter: 1}
	}
	if err := b.refresh(context.Background()); err != nil || !ticket.UserUpdate {
		t.Fatalf("temporary failure should keep the notice pending: %v", err)
	}
	api.fail = nil
	if err := b.refresh(context.Background()); err != nil || ticket.UserUpdate {
		t.Fatalf("retry should deliver and clear the notice: %v", err)
	}
	sends := api.requests("sendMessage")
	if len(sends) != 2 || sends[1]["message_thread_id"] != int64(101) {
		t.Fatalf("wrong notice destination: %+v", sends)
	}
}

func (f *fakeTelegram) requests(method string) []fields {
	var found []fields
	for _, call := range f.calls {
		if call.method == method {
			found = append(found, call.params)
		}
	}
	return found
}

func testBot(t *testing.T) (*bot, *fakeTelegram) {
	t.Helper()
	s, err := openStore(filepath.Join(t.TempDir(), "state.json"))
	if err != nil {
		t.Fatal(err)
	}
	api := &fakeTelegram{id: 1000}
	return &bot{
		s: s, api: api, username: "SupportBot", botID: 101,
		cfg: config{Owner: 11, Categories: []category{{"talk", "Поговорить"}}},
	}, api
}

func TestPrivateTopicLifecycle(t *testing.T) {
	b, api := testBot(t)
	ctx := context.Background()
	b.s.People[11], b.s.People[22], b.s.Admins[22] = "Owner", "Admin", true
	send := func(u update, album ...*message) {
		t.Helper()
		if err := b.handle(ctx, u, album); err != nil {
			t.Fatal(err)
		}
	}
	msg := func(user, topic, id int64, text string) *message {
		return &message{ID: id, Topic: topic, Date: 1, Text: text, HasContent: true,
			Chat: chat{ID: user, Type: "private"}, From: person{ID: user}}
	}
	tap := func(user, topic, messageID int64, data string) {
		m := msg(user, topic, messageID, "")
		m.From = person{ID: b.botID, Bot: true}
		m.Markup = &keyboard{Rows: [][]button{{{Data: data}}}}
		send(update{Callback: &callback{ID: "test", From: person{ID: user}, Message: m, Data: data}})
	}

	send(update{Message: msg(33, 0, 1, "/start")})
	menuID := api.id
	tap(33, 0, menuID, "cat:talk")
	tap(33, 0, menuID, "cat:talk")
	ticket := b.s.Tickets[1]
	if ticket == nil || len(api.requests("createForumTopic")) != 1 ||
		ticket.State != "pending" || ticket.Notices[11] == 0 || ticket.Notices[22] == 0 {
		t.Fatalf("invalid pending state: %+v", ticket)
	}

	first, second := msg(33, ticket.UserTopic, 31, ""), msg(33, ticket.UserTopic, 32, "")
	first.MediaGroup, second.MediaGroup = "album", "album"
	send(update{}, first, second)
	if len(api.requests("copyMessages")) != 0 || len(ticket.Queue) != 1 {
		t.Fatal("album should wait for acceptance")
	}
	ownerNotice := ticket.Notices[11]
	tap(22, 0, ownerNotice, "take:1")
	tap(22, ticket.UserTopic, ticket.Notices[22], "take:1")
	if ticket.State != "pending" {
		t.Fatal("a callback from a different card or topic claimed the request")
	}
	adminNotice := ticket.Notices[22]
	tap(22, 0, adminNotice, "take:1")
	tap(11, 0, ownerNotice, "take:1")
	if ticket.AdminID != 22 || ticket.AdminTopic == ticket.UserTopic || len(api.requests("createForumTopic")) != 2 {
		t.Fatalf("invalid assignment: %+v", ticket)
	}
	cardCount := 0
	for _, edit := range api.requests("editMessageText") {
		if edit["message_id"] != adminNotice && edit["message_id"] != ownerNotice {
			continue
		}
		if !strings.Contains(fmt.Sprint(edit["text"]), "Чат принял админ Admin") {
			t.Fatalf("assignment card has no admin name: %+v", edit)
		}
		rows := edit["reply_markup"].(*keyboard).Rows
		if edit["chat_id"] == int64(22) && (len(rows) != 1 || rows[0][0].URL == "") {
			t.Fatal("assigned admin did not get the topic link")
		}
		if edit["chat_id"] == int64(11) && len(rows) != 0 {
			t.Fatal("another admin got a button for the assigned topic")
		}
		cardCount++
	}
	if cardCount != 2 {
		t.Fatalf("updated %d assignment cards, want 2", cardCount)
	}
	albums := api.requests("copyMessages")
	if len(albums) != 1 || albums[0]["chat_id"] != int64(22) ||
		albums[0]["message_thread_id"] != ticket.AdminTopic ||
		!reflect.DeepEqual(albums[0]["message_ids"], []int64{31, 32}) {
		t.Fatalf("wrong album routing: %+v", albums)
	}

	send(update{Message: msg(22, ticket.AdminTopic, 40, "Здравствуйте")})
	send(update{Message: msg(99, ticket.AdminTopic, 41, "Постороннее сообщение")})
	copies := api.requests("copyMessage")
	if len(copies) != 1 || copies[0]["chat_id"] != int64(33) || copies[0]["message_thread_id"] != ticket.UserTopic {
		t.Fatalf("wrong reply routing: %+v", copies)
	}
	tap(22, ticket.AdminTopic, 1200, "close:1")
	tap(22, ticket.AdminTopic, 1201, "rate:1:1")
	tap(33, ticket.UserTopic, 1202, "rate:1:5")
	tap(33, ticket.UserTopic, 1202, "rate:1:1")
	send(update{Message: msg(33, ticket.UserTopic, 42, "После закрытия")})
	if len(api.requests("copyMessage")) != 1 {
		t.Fatal("closed ticket relayed a message")
	}
	reloaded, err := openStore(b.s.path)
	if err != nil {
		t.Fatal(err)
	}
	if got := reloaded.Tickets[1]; got.State != "closed" || got.Rating != 5 || len(got.Queue) != 0 {
		t.Fatalf("invalid persisted close or rating: %+v", got)
	}
}

func TestAdminInviteNeedsOwnerApproval(t *testing.T) {
	b, api := testBot(t)
	ctx := context.Background()
	send := func(u update) {
		t.Helper()
		if err := b.handle(ctx, u, nil); err != nil {
			t.Fatal(err)
		}
	}
	tap := func(id int64, data string) {
		send(update{Callback: &callback{ID: "test", From: person{ID: id}, Data: data,
			Message: &message{ID: 1, Date: 1, Chat: chat{ID: id, Type: "private"}, From: person{ID: b.botID, Bot: true},
				Markup: &keyboard{Rows: [][]button{{{Data: data}}}}}}})
	}
	tap(33, "invite")
	if len(b.s.Invites) != 0 {
		t.Fatal("non-owner generated an invite")
	}
	tap(11, "invite")
	sends := api.requests("sendMessage")
	if len(sends) != 1 {
		t.Fatal("invite not sent")
	}
	_, token, ok := strings.Cut(fmt.Sprint(sends[0]["text"]), "?start=invite_")
	if !ok || token == "" {
		t.Fatal("invite link missing")
	}
	for _, id := range []int64{22, 33} {
		send(update{Message: &message{Chat: chat{ID: id, Type: "private"}, From: person{ID: id},
			Text: "/start invite_" + token, HasContent: true}})
	}
	if b.admin(22) || b.admin(33) || len(b.s.Invites) != 0 || len(b.s.JoinRequests) != 1 {
		t.Fatal("invite must create one request without granting an admin role")
	}
	approval := "join:22:" + token
	tap(22, approval)
	if b.admin(22) {
		t.Fatal("applicant approved their own request")
	}
	tap(11, "join:22:wrong")
	if b.admin(22) {
		t.Fatal("wrong approval key granted a role")
	}
	tap(11, approval)
	tap(11, approval)
	if !b.admin(22) || b.admin(33) || len(b.s.Invites) != 0 {
		t.Fatal("invite not consumed exactly once")
	}
}

func TestCallbackMustMatchBotMessageAndPrivateActor(t *testing.T) {
	b, _ := testBot(t)
	q := callback{ID: "test", Data: "invite", From: person{ID: b.cfg.Owner},
		Message: &message{ID: 10, Date: 1, Chat: chat{ID: b.cfg.Owner, Type: "private"},
			From: person{ID: b.botID, Bot: true}, Markup: &keyboard{Rows: [][]button{{{Data: "admins:0"}}}}}}
	if err := b.handle(context.Background(), update{Callback: &q}, nil); err != nil {
		t.Fatal(err)
	}
	if len(b.s.Invites) != 0 {
		t.Fatal("forged callback data was accepted")
	}
	q.Message.Markup.Rows[0][0].Data = q.Data
	q.Message.From.ID++
	if b.callbackMatches(&q) {
		t.Fatal("callback from a different bot was accepted")
	}
	q.Message.From.ID = b.botID
	q.Message.Chat.ID++
	if b.callbackMatches(&q) {
		t.Fatal("callback from a mismatched private chat was accepted")
	}
}
