package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"time"
)

type fields map[string]any

type telegram struct {
	url    string
	client *http.Client
}

type apiError struct {
	Method, Description string
	Code, RetryAfter    int
}

func (e *apiError) Error() string { return fmt.Sprintf("Telegram %s (%d)", e.Method, e.Code) }

func (t *telegram) call(ctx context.Context, method string, params fields, output any) error {
	if params == nil {
		params = fields{}
	}
	body, err := json.Marshal(params)
	if err != nil {
		return err
	}
	if method != "getUpdates" {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, 12*time.Second)
		defer cancel()
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, t.url+"/"+method, bytes.NewReader(body))
	if err != nil {
		return &apiError{Method: method}
	}
	req.Header.Set("Content-Type", "application/json")
	res, err := t.client.Do(req)
	if err != nil {
		return &apiError{Method: method}
	}
	defer res.Body.Close()
	var reply struct {
		OK          bool
		Result      json.RawMessage
		Code        int `json:"error_code"`
		Description string
		Parameters  struct {
			RetryAfter int `json:"retry_after"`
		}
	}
	if err := json.NewDecoder(res.Body).Decode(&reply); err != nil {
		return &apiError{Method: method, Code: res.StatusCode}
	}
	if !reply.OK {
		if reply.Code == 0 && res.StatusCode >= 400 {
			reply.Code = res.StatusCode
		}
		return &apiError{method, reply.Description, reply.Code, reply.Parameters.RetryAfter}
	}
	if output != nil {
		if err := json.Unmarshal(reply.Result, output); err != nil {
			return &apiError{Method: method}
		}
	}
	return nil
}

func telegramError(err error) *apiError {
	var apiErr *apiError
	errors.As(err, &apiErr)
	return apiErr
}

type person struct {
	ID        int64
	FirstName string `json:"first_name"`
	LastName  string `json:"last_name"`
	Username  string
	Bot       bool `json:"is_bot"`
}

type chat struct {
	ID   int64
	Type string
}

func (c chat) belongsTo(p person) bool {
	return c.Type == "private" && c.ID == p.ID && !p.Bot && p.ID > 0
}

type message struct {
	ID         int64 `json:"message_id"`
	Topic      int64 `json:"message_thread_id"`
	Date       int64
	Chat       chat
	From       person
	Text       string
	MediaGroup string    `json:"media_group_id"`
	Markup     *keyboard `json:"reply_markup"`
	HasContent bool      `json:"-"`
}

func (m *message) UnmarshalJSON(data []byte) error {
	type plain message
	if err := json.Unmarshal(data, (*plain)(m)); err != nil {
		return err
	}
	var content map[string]json.RawMessage
	if err := json.Unmarshal(data, &content); err != nil {
		return err
	}
	for _, key := range []string{
		"text", "animation", "audio", "document", "photo", "live_photo", "sticker", "story", "video",
		"video_note", "voice", "contact", "dice", "game", "poll", "venue", "location",
		"paid_media", "invoice", "giveaway", "giveaway_winners", "rich_message", "widget",
	} {
		if _, ok := content[key]; ok {
			m.HasContent = true
			break
		}
	}
	return nil
}

type callback struct {
	ID      string
	From    person
	Message *message
	Data    string
}

type update struct {
	ID       int64 `json:"update_id"`
	Message  *message
	Callback *callback `json:"callback_query"`
}

type button struct {
	Text   string      `json:"text"`
	Data   string      `json:"callback_data,omitempty"`
	URL    string      `json:"url,omitempty"`
	Icon   string      `json:"icon_custom_emoji_id,omitempty"`
	Style  string      `json:"style,omitempty"`
	WebApp *webAppInfo `json:"web_app,omitempty"`
}

type webAppInfo struct {
	URL string `json:"url"`
}

type keyboard struct {
	Rows [][]button `json:"inline_keyboard"`
}
