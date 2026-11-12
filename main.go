package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"
)

func main() {
	ownerID := flag.Bool("owner-id", false, "показать ID отправителя последней команды /start")
	flag.Parse()
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := run(ctx, *ownerID); err != nil && ctx.Err() == nil {
		log.Print(err)
		os.Exit(1)
	}
}

func run(ctx context.Context, ownerID bool) error {
	cfg, err := loadConfig(".env", !ownerID)
	if err != nil {
		return err
	}
	api := &telegram{
		url: "https://api.telegram.org/bot" + cfg.Token,
		client: &http.Client{
			Timeout: 35 * time.Second,
			CheckRedirect: func(*http.Request, []*http.Request) error {
				return http.ErrUseLastResponse
			},
		},
	}
	if ownerID {
		return printOwnerID(ctx, api)
	}
	var identity struct {
		ID        int64
		Username  string
		HasTopics bool `json:"has_topics_enabled"`
	}
	if err := api.call(ctx, "getMe", nil, &identity); err != nil {
		return err
	}
	if !identity.HasTopics {
		return errors.New("включи темы в личных чатах бота через @BotFather и запусти бота снова")
	}
	if identity.ID <= 0 || identity.Username == "" {
		return errors.New("Telegram не вернул ID или username бота")
	}
	s, err := openStore(cfg.StatePath)
	if err != nil {
		return err
	}
	b := &bot{api: api, s: s, cfg: cfg, botID: identity.ID, username: identity.Username, sticker: resolveSticker(ctx, api, cfg)}
	commands := []fields{
		{"command": "start", "description": "Новый чат"},
		{"command": "close", "description": "Завершить чат"},
	}
	b.optional(ctx, "setMyCommands", fields{"commands": commands})
	if s.People[cfg.Owner] != "" {
		b.ownerCommands(ctx)
	}
	log.Printf("@%s запущен", b.username)
	if cfg.Port != "" || cfg.MiniAppURL != "" {
		return serve(ctx, b)
	}
	return poll(ctx, b)
}

func (b *bot) ownerCommands(ctx context.Context) {
	b.optional(ctx, "setMyCommands", fields{
		"scope": fields{"type": "chat", "chat_id": b.cfg.Owner},
		"commands": []fields{
			{"command": "start", "description": "Новый чат"},
			{"command": "close", "description": "Завершить чат"},
			{"command": "admins", "description": "Управлять командой"},
		},
	})
}

func printOwnerID(ctx context.Context, api caller) error {
	var updates []update
	if err := api.call(ctx, "getUpdates", fields{"allowed_updates": []string{"message", "callback_query"}}, &updates); err != nil {
		return err
	}
	for i := len(updates) - 1; i >= 0; i-- {
		m := updates[i].Message
		if m == nil || !m.Chat.belongsTo(m.From) {
			continue
		}
		words := strings.Fields(m.Text)
		if len(words) != 0 {
			command, _, _ := strings.Cut(words[0], "@")
			if command == "/start" {
				fmt.Println(m.From.ID)
				return nil
			}
		}
	}
	return errors.New("отправь /start своему боту в личке и повтори команду")
}

func resolveSticker(ctx context.Context, api caller, cfg config) string {
	if cfg.WelcomeFile != "" || cfg.WelcomePack == "" {
		return cfg.WelcomeFile
	}
	var pack struct {
		Stickers []struct {
			File string `json:"file_id"`
		}
	}
	err := api.call(ctx, "getStickerSet", fields{"name": cfg.WelcomePack}, &pack)
	if err == nil && cfg.WelcomeNumber <= len(pack.Stickers) && pack.Stickers[cfg.WelcomeNumber-1].File != "" {
		return pack.Stickers[cfg.WelcomeNumber-1].File
	}
	log.Print("Приветственный стикер недоступен. Проверь WELCOME_STICKER_SET и WELCOME_STICKER_NUMBER.")
	return ""
}
