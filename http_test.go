package main

import (
	"bytes"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestWebhookSecretAndDeduplication(t *testing.T) {
	b, api := testBot(t)
	b.cfg.WebhookSecret = "test-secret"
	handler := b.httpHandler(nil)
	data := `{"update_id":50,"message":{"message_id":30,"chat":{"id":33,"type":"private"},"from":{"id":33},"text":"/start"}}`
	send := func(secret string) int {
		r := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(data))
		r.Header.Set("X-Telegram-Bot-Api-Secret-Token", secret)
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, r)
		return w.Code
	}
	if send("wrong") != http.StatusUnauthorized || len(api.calls) != 0 {
		t.Fatal("unsigned webhook reached the bot")
	}
	if send("test-secret") != http.StatusOK || send("test-secret") != http.StatusOK ||
		len(api.requests("sendMessage")) != 1 {
		t.Fatal("webhook retry executed the update again")
	}
}

func TestWebhookCoalescesAlbumBeforeDelivery(t *testing.T) {
	b, api := testBot(t)
	b.cfg.Port, b.cfg.WebhookSecret = "8080", "test-secret"
	b.s.Admins[22] = true
	ticket := &ticket{ID: 1, State: "active", UserID: 33, UserTopic: 101, AdminID: 22, AdminTopic: 102}
	b.s.Tickets[1] = ticket
	handler := b.httpHandler(nil)
	for i, messageID := range []int{41, 40} {
		data := fmt.Sprintf(`{"update_id":%d,"message":{"message_id":%d,"message_thread_id":102,"chat":{"id":22,"type":"private"},"from":{"id":22},"media_group_id":"g","photo":[]}}`, i+1, messageID)
		r := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(data))
		r.Header.Set("X-Telegram-Bot-Api-Secret-Token", b.cfg.WebhookSecret)
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, r)
		if w.Code != http.StatusOK {
			t.Fatalf("webhook returned %d", w.Code)
		}
	}
	if len(ticket.AdminQueue) != 1 || len(api.requests("copyMessage")) != 0 {
		t.Fatal("album parts were not buffered together")
	}
	ticket.AdminQueue[0].Ready = 0
	if err := b.flushWebhookQueues(t.Context()); err != nil {
		t.Fatal(err)
	}
	copies := api.requests("copyMessages")
	if len(copies) != 1 || !reflect.DeepEqual(copies[0]["message_ids"], []int64{40, 41}) ||
		copies[0]["message_thread_id"] != int64(101) {
		t.Fatalf("wrong album delivery: %+v", copies)
	}
}

func signedInit(token string, id int64, at time.Time) string {
	authDate, user := fmt.Sprint(at.Unix()), fmt.Sprintf(`{"id":%d}`, id)
	key := hmac.New(sha256.New, []byte("WebAppData"))
	key.Write([]byte(token))
	sig := hmac.New(sha256.New, key.Sum(nil))
	sig.Write([]byte("auth_date=" + authDate + "\nuser=" + user))
	return url.Values{"auth_date": {authDate}, "user": {user}, "hash": {hex.EncodeToString(sig.Sum(nil))}}.Encode()
}

func TestMiniAppRestrictsPlayersAndTurns(t *testing.T) {
	b, _ := testBot(t)
	b.cfg.MiniAppURL, b.cfg.Token = "https://game.example.com", "1:test"
	b.s.Admins[22] = true
	b.s.Tickets[1] = &ticket{ID: 1, State: "active", UserID: 33, AdminID: 22}
	h := b.httpHandler(nil)
	now := time.Now()
	initData := func(id int64) string { return signedInit(b.cfg.Token, id, now) }
	send := func(auth, action string, cell int, version int64) int {
		t.Helper()
		data, err := json.Marshal(gameRequest{InitData: auth, Ticket: "1", Action: action, Cell: &cell, Version: &version})
		if err != nil {
			t.Fatal(err)
		}
		r := httptest.NewRequest(http.MethodPost, "/game/api", bytes.NewReader(data))
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		return w.Code
	}
	tests := []struct {
		auth, action string
		cell         int
		version      int64
		status       int
	}{
		{initData(33) + "&auth_date=1", "state", 0, 0, 401},
		{signedInit(b.cfg.Token, 33, now.Add(-2*time.Hour)), "state", 0, 0, 401},
		{signedInit("wrong", 33, now), "state", 0, 0, 401},
		{initData(99), "state", 0, 0, 404},
		{initData(11), "state", 0, 0, 404},
		{initData(22), "move", 1, 0, 409},
		{initData(33), "move", 0, 0, 200},
		{initData(33), "move", 2, 0, 409},
		{initData(22), "move", 0, 1, 409},
		{initData(22), "move", 3, 1, 200},
		{initData(33), "move", 1, 2, 200},
		{initData(22), "move", 4, 3, 200},
		{initData(33), "move", 2, 4, 200},
		{initData(22), "move", 5, 5, 409},
		{initData(22), "reset", 0, 5, 200},
	}
	for i, tc := range tests {
		if got := send(tc.auth, tc.action, tc.cell, tc.version); got != tc.status {
			t.Fatalf("request %d (%s): got %d, want %d", i, tc.action, got, tc.status)
		}
	}
	delete(b.s.Admins, 22)
	if got := send(initData(33), "state", 0, 6); got != http.StatusNotFound {
		t.Fatal("game remained accessible after the admin role was removed")
	}
}
