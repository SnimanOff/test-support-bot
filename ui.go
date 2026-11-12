package main

import (
	"fmt"
	"strconv"
)

func (b *bot) categories() *keyboard {
	accents := map[string]string{"talk": "☁️", "help": "🫶", "other": "✉️"}
	rows := make([][]button, 0, len(b.cfg.Categories))
	for _, category := range b.cfg.Categories {
		label, icon := category.Label, b.cfg.Emoji[category.ID]
		if accent := accents[category.ID]; icon == "" && accent != "" {
			label = accent + "  " + label
		}
		rows = append(rows, []button{{Text: label, Data: "cat:" + category.ID, Icon: icon}})
	}
	return &keyboard{Rows: rows}
}

func (b *bot) newChat() *keyboard {
	return &keyboard{Rows: [][]button{{{Text: "Новый чат", Data: "new", Icon: b.cfg.Emoji["new"]}}}}
}

func (b *bot) closeButton(id int64) *keyboard {
	return &keyboard{Rows: [][]button{{{
		Text: "Завершить чат", Data: fmt.Sprintf("close:%d", id), Icon: b.cfg.Emoji["close"],
	}}}}
}

func (b *bot) gameButton(id int64) button {
	return button{Text: "Крестики-нолики", WebApp: &webAppInfo{URL: fmt.Sprintf("%s/game?t=%d", b.cfg.MiniAppURL, id)}}
}

func (b *bot) activeButtons(id int64) *keyboard {
	k := b.closeButton(id)
	if b.cfg.MiniAppURL != "" {
		k.Rows = append([][]button{{b.gameButton(id)}}, k.Rows...)
	}
	return k
}

func (b *bot) acceptButton(id int64) *keyboard {
	return &keyboard{Rows: [][]button{{{
		Text: "Принять чат", Data: fmt.Sprintf("take:%d", id), Icon: b.cfg.Emoji["take"], Style: "success",
	}}}}
}

func (b *bot) ratingButtons(id int64) *keyboard {
	ratings := make([]button, 5)
	for i := range ratings {
		ratings[i] = button{
			Text: strconv.Itoa(i + 1), Data: fmt.Sprintf("rate:%d:%d", id, i+1), Icon: b.cfg.Emoji["star"],
		}
	}
	return &keyboard{Rows: [][]button{ratings, b.newChat().Rows[0]}}
}

func (b *bot) openButton(topic int64) *keyboard {
	return &keyboard{Rows: [][]button{{{
		Text: "Открыть чат", URL: fmt.Sprintf("https://t.me/%s/%d", b.username, topic),
		Icon: b.cfg.Emoji["open"], Style: "primary",
	}}}}
}
