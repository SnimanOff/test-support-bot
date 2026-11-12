package main

import (
	"crypto/hmac"
	"crypto/sha256"
	"embed"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"time"
)

//go:embed web/game.html web/game.css web/game.js
var gameAssets embed.FS

type game struct {
	Board   [9]uint8 `json:"board"`
	Version int64    `json:"version"`
}

func (g game) outcome() (turn, winner uint8, finished bool) {
	for _, line := range [][3]int{{0, 1, 2}, {3, 4, 5}, {6, 7, 8}, {0, 3, 6}, {1, 4, 7}, {2, 5, 8}, {0, 4, 8}, {2, 4, 6}} {
		if mark := g.Board[line[0]]; mark != 0 && mark == g.Board[line[1]] && mark == g.Board[line[2]] {
			return 0, mark, true
		}
	}
	moves := 0
	for _, mark := range g.Board {
		if mark != 0 {
			moves++
		}
	}
	if moves == len(g.Board) {
		return 0, 0, true
	}
	return uint8(moves%2 + 1), 0, false
}

func (b *bot) registerGame(mux *http.ServeMux) {
	for _, file := range []struct{ path, name, contentType string }{
		{"/game", "web/game.html", "text/html; charset=utf-8"},
		{"/game/style.css", "web/game.css", "text/css; charset=utf-8"},
		{"/game/app.js", "web/game.js", "text/javascript; charset=utf-8"},
	} {
		mux.HandleFunc("GET "+file.path, func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", file.contentType)
			w.Header().Set("X-Content-Type-Options", "nosniff")
			w.Header().Set("Referrer-Policy", "no-referrer")
			w.Header().Set("Content-Security-Policy", "default-src 'self'; script-src 'self' https://telegram.org; object-src 'none'; base-uri 'none'; form-action 'none'")
			body, _ := gameAssets.ReadFile(file.name)
			_, _ = w.Write(body)
		})
	}
	mux.HandleFunc("POST /game/api", b.gameAPI)
}

type gameRequest struct {
	InitData string `json:"initData"`
	Ticket   string `json:"ticket"`
	Action   string `json:"action"`
	Cell     *int   `json:"cell"`
	Version  *int64 `json:"version"`
}

func (b *bot) gameAPI(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	var req gameRequest
	if err := decodeJSON(w, r, 16<<10, &req); err != nil {
		w.WriteHeader(http.StatusBadRequest)
		return
	}
	user := miniAppUser(req.InitData, b.cfg.Token, time.Now())
	if user == 0 {
		w.WriteHeader(http.StatusUnauthorized)
		return
	}
	id, err := strconv.ParseInt(req.Ticket, 10, 64)
	if err != nil || id <= 0 {
		w.WriteHeader(http.StatusBadRequest)
		return
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.failed != nil {
		w.WriteHeader(http.StatusServiceUnavailable)
		return
	}
	t := b.s.Tickets[id]
	if t == nil || t.State != "active" || !b.admin(t.AdminID) || (user != t.UserID && user != t.AdminID) {
		w.WriteHeader(http.StatusNotFound)
		return
	}
	mark := uint8(1)
	if user == t.AdminID {
		mark = 2
	}
	if req.Action != "state" {
		if status := b.play(t, mark, req); status != http.StatusOK {
			w.WriteHeader(status)
			return
		}
	}
	turn, winner, finished := t.Game.outcome()
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(fields{
		"board": t.Game.Board, "you": mark, "turn": turn, "winner": winner, "finished": finished, "version": t.Game.Version,
	})
}

func (b *bot) play(t *ticket, mark uint8, req gameRequest) int {
	if req.Action != "move" && req.Action != "reset" {
		return http.StatusBadRequest
	}
	if req.Version == nil || *req.Version != t.Game.Version {
		return http.StatusConflict
	}
	turn, _, finished := t.Game.outcome()
	next := t.Game
	if req.Action == "reset" {
		if !finished {
			return http.StatusConflict
		}
		next.Board = [9]uint8{}
	} else {
		if req.Cell == nil || *req.Cell < 0 || *req.Cell >= len(next.Board) {
			return http.StatusBadRequest
		}
		if finished || turn != mark || next.Board[*req.Cell] != 0 {
			return http.StatusConflict
		}
		next.Board[*req.Cell] = mark
	}
	next.Version++
	previous := t.Game
	t.Game = next
	if err := b.s.save(); err != nil {
		t.Game = previous
		return http.StatusServiceUnavailable
	}
	return http.StatusOK
}

func miniAppUser(initData, token string, now time.Time) int64 {
	if initData == "" || token == "" {
		return 0
	}
	params, err := url.ParseQuery(initData)
	if err != nil {
		return 0
	}
	var signed []string
	for key, values := range params {
		if len(values) != 1 || key == "" || strings.ContainsAny(key, "\r\n=") || strings.ContainsAny(values[0], "\r\n") {
			return 0
		}
		if key != "hash" {
			signed = append(signed, key+"="+values[0])
		}
	}
	provided, err := hex.DecodeString(params.Get("hash"))
	if err != nil {
		return 0
	}
	slices.Sort(signed)
	key := hmac.New(sha256.New, []byte("WebAppData"))
	_, _ = key.Write([]byte(token))
	signature := hmac.New(sha256.New, key.Sum(nil))
	_, _ = signature.Write([]byte(strings.Join(signed, "\n")))
	if !hmac.Equal(provided, signature.Sum(nil)) {
		return 0
	}
	authDate, err := strconv.ParseInt(params.Get("auth_date"), 10, 64)
	if err != nil || authDate <= 0 || authDate < now.Add(-time.Hour).Unix() || authDate > now.Add(time.Minute).Unix() {
		return 0
	}
	var user person
	if err := json.Unmarshal([]byte(params.Get("user")), &user); err != nil || user.Bot || user.ID <= 0 {
		return 0
	}
	return user.ID
}
