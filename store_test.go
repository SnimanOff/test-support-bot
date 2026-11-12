package main

import (
	"path/filepath"
	"reflect"
	"testing"
)

func TestStoreRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "store.json")

	s, err := openStore(path)
	if err != nil {
		t.Fatal(err)
	}
	s.NextID = 100
	s.Admins = map[int64]bool{7: true, 9: true}
	s.Tickets = map[int64]*ticket{
		1: &ticket{
			ID:        1,
			UserID:    2,
			UserTopic: 3,
			State:     "pending",
			Category:  "album",
			Queue:     []queued{{IDs: []int64{10, 11}, Group: "group"}},
		},
		2: &ticket{
			ID:         2,
			UserID:     4,
			UserTopic:  5,
			AdminID:    9,
			AdminTopic: 6,
			State:      "active",
			Category:   "album",
			Queue:      []queued{{IDs: []int64{12, 13, 14}}},
		},
	}

	if err := s.save(); err != nil {
		t.Fatal(err)
	}
	s.Tickets[2].State = "closed"
	s.Tickets[2].Queue = nil
	s.Tickets[2].Rating = 5
	if err := s.save(); err != nil {
		t.Fatal(err)
	}

	got, err := openStore(path)
	if err != nil {
		t.Fatal(err)
	}

	if got.NextID != s.NextID {
		t.Fatalf("NextID = %d, want %d", got.NextID, s.NextID)
	}
	if !reflect.DeepEqual(got.Admins, s.Admins) {
		t.Fatalf("Admins = %#v, want %#v", got.Admins, s.Admins)
	}
	if !reflect.DeepEqual(got.Tickets, s.Tickets) {
		t.Fatalf("Tickets = %#v, want %#v", got.Tickets, s.Tickets)
	}
}
