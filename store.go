package main

import (
	"encoding/json"
	"errors"
	"io/fs"
	"maps"
	"os"
	"path/filepath"
	"sync"
)

type task struct {
	ID   int    `json:"id"`
	Text string `json:"text"`
}

type feedSubscription struct {
	ID   int               `json:"id"`
	URL  string            `json:"url"`
	Seen map[string]string `json:"seen"` // nil until the first successful poll
}

type chatState struct {
	Subscribed bool               `json:"subscribed"`
	NextID     int                `json:"next_id"`
	Tasks      []task             `json:"tasks"`
	NextFeedID int                `json:"next_feed_id,omitempty"`
	Feeds      []feedSubscription `json:"feeds,omitempty"`
}

type store struct {
	mu    sync.Mutex
	path  string
	chats map[int64]chatState
}

func openStore(path string) (*store, error) {
	s := &store{path: path, chats: make(map[int64]chatState)}
	data, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return s, nil
	}
	if err != nil {
		return nil, err
	}
	if err := json.Unmarshal(data, &s.chats); err != nil {
		return nil, err
	}
	if s.chats == nil {
		return nil, errors.New("expected a JSON object in state file")
	}
	return s, nil
}

// change publishes a new state only after the atomic file replacement succeeds.
func (s *store) change(id int64, fn func(*chatState) bool) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	previous, exists := s.chats[id]
	next := copyChat(previous)
	if !fn(&next) {
		return false, nil
	}
	s.chats[id] = next
	if err := s.save(); err != nil {
		if exists {
			s.chats[id] = previous
		} else {
			delete(s.chats, id)
		}
		return false, err
	}
	return true, nil
}

func copyChat(chat chatState) chatState {
	chat.Tasks = append([]task(nil), chat.Tasks...)
	chat.Feeds = append([]feedSubscription(nil), chat.Feeds...)
	for i := range chat.Feeds {
		chat.Feeds[i].Seen = maps.Clone(chat.Feeds[i].Seen)
	}
	return chat
}

func (s *store) chat(id int64) chatState {
	s.mu.Lock()
	defer s.mu.Unlock()
	return copyChat(s.chats[id])
}

func (s *store) subscriptions() map[int64]chatState {
	s.mu.Lock()
	defer s.mu.Unlock()
	active := make(map[int64]chatState)
	for id, chat := range s.chats {
		if chat.Subscribed {
			active[id] = copyChat(chat)
		}
	}
	return active
}

func (s *store) feedSubscriptions() map[string][]feedSubscriber {
	s.mu.Lock()
	defer s.mu.Unlock()
	feeds := make(map[string][]feedSubscriber)
	for id, chat := range s.chats {
		for _, sub := range chat.Feeds {
			// Seen snapshots are replaced, never mutated after publication.
			feeds[sub.URL] = append(feeds[sub.URL], feedSubscriber{id, sub})
		}
	}
	return feeds
}

func (s *store) hasFeed(id int64, feedID int) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, sub := range s.chats[id].Feeds {
		if sub.ID == feedID {
			return true
		}
	}
	return false
}

func (s *store) save() error {
	dir := filepath.Dir(s.path)
	if err := os.MkdirAll(dir, 0700); err != nil {
		return err
	}
	f, err := os.CreateTemp(dir, ".state-*.json")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	writeErr := json.NewEncoder(f).Encode(s.chats)
	if writeErr == nil {
		writeErr = f.Sync()
	}
	closeErr := f.Close()
	if writeErr != nil {
		return writeErr
	}
	if closeErr != nil {
		return closeErr
	}
	return os.Rename(f.Name(), s.path)
}
