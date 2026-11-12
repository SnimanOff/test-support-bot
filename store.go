package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"time"
)

type queued struct {
	IDs   []int64 `json:"ids"`
	Group string  `json:"group,omitempty"`
	Ready int64   `json:"ready,omitempty"`
}

type joinRequest struct {
	Key     string
	Expires time.Time
}

type ticket struct {
	ID, UserID, UserTopic, Origin int64
	AdminID, AdminTopic           int64
	Category, State               string
	AcceptedBy                    string
	Rating                        int
	Notices                       map[int64]int64
	Queue                         []queued
	AdminQueue                    []queued
	LastUserMessage               int64
	LastAdminMessage              int64
	UserUpdate, AdminUpdate       bool
	Game                          game
}

type store struct {
	path          string
	NextID        int64
	Tickets       map[int64]*ticket
	People        map[int64]string
	Admins        map[int64]bool
	Invites       map[string]time.Time
	JoinRequests  map[int64]joinRequest
	RecentUpdates []int64
}

func openStore(path string) (*store, error) {
	s := &store{
		path: path, NextID: 1, Tickets: map[int64]*ticket{}, People: map[int64]string{},
		Admins: map[int64]bool{}, Invites: map[string]time.Time{}, JoinRequests: map[int64]joinRequest{},
	}
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return s, nil
	}
	if err != nil {
		return nil, fmt.Errorf("прочитать состояние: %w", err)
	}
	if err := json.Unmarshal(data, s); err != nil {
		return nil, fmt.Errorf("прочитать состояние: %w", err)
	}
	if s.NextID < 1 || s.Tickets == nil || s.People == nil || s.Admins == nil || s.Invites == nil || s.JoinRequests == nil {
		return nil, errors.New("неверный формат файла состояния")
	}
	return s, nil
}

// A failed save stops the polling loop so it never acknowledges unsaved changes.
func (s *store) save() error {
	dir := filepath.Dir(s.path)
	if err := os.MkdirAll(dir, 0700); err != nil {
		return err
	}
	temp, err := os.CreateTemp(dir, ".bot-state-*")
	if err != nil {
		return err
	}
	defer os.Remove(temp.Name())
	err = json.NewEncoder(temp).Encode(s)
	if err == nil {
		err = temp.Sync()
	}
	err = errors.Join(err, temp.Close())
	if err != nil {
		return err
	}
	return os.Rename(temp.Name(), s.path)
}

func (s *store) byTopic(chat, topic int64) (*ticket, bool) {
	if topic == 0 {
		return nil, false
	}
	for _, t := range s.Tickets {
		if t.UserID == chat && t.UserTopic == topic {
			return t, true
		}
		if t.AdminID == chat && t.AdminTopic == topic {
			return t, false
		}
	}
	return nil, false
}

func (s *store) orderedTickets() []int64 {
	ids := make([]int64, 0, len(s.Tickets))
	for id := range s.Tickets {
		ids = append(ids, id)
	}
	slices.Sort(ids)
	return ids
}
