package main

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"unicode/utf8"
)

type category struct {
	ID    string `json:"id"`
	Label string `json:"label"`
}

type config struct {
	Port, WebhookSecret, MiniAppURL, MiniAppAddr string
	Owner                                        int64
	Token, StatePath, WelcomePack, WelcomeFile   string
	WelcomeNumber                                int
	Categories                                   []category
	Emoji                                        map[string]string
}

var (
	secretFormat = regexp.MustCompile(`^[A-Za-z0-9_-]{1,256}$`)
	tokenFormat  = regexp.MustCompile(`^[0-9]+:[A-Za-z0-9_-]+$`)
	categoryID   = regexp.MustCompile(`^[A-Za-z0-9_-]{1,40}$`)
	emojiID      = regexp.MustCompile(`^[0-9]+$`)
	envKey       = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)
)

func loadConfig(path string, needOwner bool) (config, error) {
	env, err := readEnv(path)
	if err != nil {
		return config{}, err
	}
	get := func(key, fallback string) string {
		if value, ok := os.LookupEnv(key); ok {
			return value
		}
		if value, ok := env[key]; ok {
			return value
		}
		return fallback
	}
	statePath := filepath.Join("data", "state.json")
	if dir := strings.TrimSpace(get("DATA_DIR", "")); dir != "" {
		statePath = filepath.Join(dir, "state.json")
	}
	cfg := config{
		Token:       strings.TrimSpace(get("BOT_TOKEN", "")),
		StatePath:   strings.TrimSpace(get("DB_PATH", statePath)),
		WelcomePack: strings.TrimSpace(get("WELCOME_STICKER_SET", "Bocchimemes2_by_fStikBot")),
		WelcomeFile: strings.TrimSpace(get("WELCOME_STICKER_FILE_ID", "")),
		Categories:  []category{{"talk", "Поговорить"}, {"help", "Нужна помощь"}, {"other", "Другое"}},
	}
	if !tokenFormat.MatchString(cfg.Token) {
		return config{}, errors.New("укажи токен из @BotFather в BOT_TOKEN")
	}
	if !needOwner {
		return cfg, nil
	}
	cfg.Port = strings.TrimSpace(get("PORT", ""))
	cfg.WebhookSecret = get("WEBHOOK_SECRET", "")
	if cfg.Port != "" || cfg.WebhookSecret != "" {
		port, err := strconv.Atoi(cfg.Port)
		if err != nil || port < 1 || port > 65535 || !secretFormat.MatchString(cfg.WebhookSecret) {
			return config{}, errors.New("для вебхука укажи корректные PORT и WEBHOOK_SECRET")
		}
	}
	cfg.MiniAppURL = strings.TrimSpace(get("MINI_APP_URL", ""))
	cfg.MiniAppAddr = strings.TrimSpace(get("MINI_APP_ADDR", "127.0.0.1:8080"))
	if cfg.MiniAppURL != "" {
		u, err := url.Parse(cfg.MiniAppURL)
		if err != nil || u.Scheme != "https" || u.Hostname() == "" || u.User != nil ||
			u.RawQuery != "" || u.ForceQuery || u.Fragment != "" || (u.Path != "" && u.Path != "/") {
			return config{}, errors.New("MINI_APP_URL должен быть публичным HTTPS-адресом без пути")
		}
		cfg.MiniAppURL = strings.TrimSuffix(u.String(), "/")
		if cfg.Port == "" && cfg.MiniAppAddr == "" {
			return config{}, errors.New("укажи адрес прослушивания MINI_APP_ADDR")
		}
	}
	cfg.Owner, err = strconv.ParseInt(strings.TrimSpace(get("OWNER_ID", "")), 10, 64)
	if err != nil || cfg.Owner <= 0 {
		return config{}, errors.New("укажи числовой Telegram ID в OWNER_ID")
	}
	if cfg.StatePath == "" {
		return config{}, errors.New("укажи путь к файлу состояния в DB_PATH")
	}
	cfg.WelcomeNumber, err = strconv.Atoi(strings.TrimSpace(get("WELCOME_STICKER_NUMBER", "1")))
	if err != nil || cfg.WelcomeNumber < 1 {
		return config{}, errors.New("WELCOME_STICKER_NUMBER должен быть целым числом от 1")
	}
	if raw := get("CATEGORIES_JSON", ""); raw != "" {
		if err := json.Unmarshal([]byte(raw), &cfg.Categories); err != nil {
			return config{}, errors.New("CATEGORIES_JSON должен содержать JSON-массив объектов id и label")
		}
	}
	if len(cfg.Categories) < 1 || len(cfg.Categories) > 12 {
		return config{}, errors.New("укажи от 1 до 12 категорий")
	}
	seen := make(map[string]bool, len(cfg.Categories))
	for i := range cfg.Categories {
		c := &cfg.Categories[i]
		c.Label = strings.TrimSpace(c.Label)
		if !categoryID.MatchString(c.ID) || seen[c.ID] || c.Label == "" || utf8.RuneCountInString(c.Label) > 90 {
			return config{}, fmt.Errorf("проверь id и label категории %d", i+1)
		}
		seen[c.ID] = true
	}
	if raw := get("EMOJI_JSON", ""); raw != "" {
		if err := json.Unmarshal([]byte(raw), &cfg.Emoji); err != nil || cfg.Emoji == nil {
			return config{}, errors.New("EMOJI_JSON должен содержать JSON-объект со строковыми ID")
		}
		for _, id := range cfg.Emoji {
			if !emojiID.MatchString(id) {
				return config{}, errors.New("ID эмодзи должны быть строками из цифр")
			}
		}
	}
	return cfg, nil
}

func readEnv(path string) (map[string]string, error) {
	values := map[string]string{}
	file, err := os.Open(path)
	if errors.Is(err, os.ErrNotExist) {
		return values, nil
	}
	if err != nil {
		return nil, err
	}
	defer file.Close()
	scanner := bufio.NewScanner(file)
	for n := 1; scanner.Scan(); n++ {
		line := strings.TrimSpace(strings.TrimPrefix(scanner.Text(), "\ufeff"))
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		key, value, found := strings.Cut(line, "=")
		key, value = strings.TrimSpace(key), strings.TrimSpace(value)
		if !found || !envKey.MatchString(key) {
			return nil, fmt.Errorf("неверная настройка в .env на строке %d", n)
		}
		if len(value) >= 2 && (value[0] == '"' || value[0] == '\'') && value[len(value)-1] == value[0] {
			value = value[1 : len(value)-1]
		}
		values[key] = value
	}
	return values, scanner.Err()
}
