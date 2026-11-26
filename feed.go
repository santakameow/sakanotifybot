package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"log"
	"maps"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	maxFeeds       = 5
	maxFeedURL     = 512
	maxFeedSize    = 2 << 20
	maxFeedEntries = 500
	feedInterval   = 5 * time.Minute
)

func feedCommand(s *store, id int64, name, arg string) string {
	switch name {
	case "/subscribe":
		u, err := feedURL(arg)
		if err != nil || len(arg) > maxFeedURL {
			return "Формат: /subscribe https://example.com/feed.xml (публичная HTTPS-лента)."
		}
		var added, duplicate int
		ok, err := s.change(id, func(chat *chatState) bool {
			for _, sub := range chat.Feeds {
				if sub.URL == u.String() {
					duplicate = sub.ID
					return false
				}
			}
			if len(chat.Feeds) >= maxFeeds {
				return false
			}
			chat.NextFeedID++
			added = chat.NextFeedID
			chat.Feeds = append(chat.Feeds, feedSubscription{ID: added, URL: u.String()})
			return true
		})
		if err != nil {
			return storageError(err)
		}
		if duplicate != 0 {
			return fmt.Sprintf("Вы уже подписаны на ленту №%d.", duplicate)
		}
		if !ok {
			return fmt.Sprintf("Лимит: %d подписок на ленты.", maxFeeds)
		}
		return fmt.Sprintf("Добавлена лента №%d. После первой успешной проверки буду присылать новые записи и обновления без старых публикаций.", added)
	case "/feeds":
		feeds := s.chat(id).Feeds
		if len(feeds) == 0 {
			return "Нет подписок на ленты. Добавить: /subscribe URL"
		}
		var b strings.Builder
		b.WriteString("Подписки на ленты:")
		for _, sub := range feeds {
			fmt.Fprintf(&b, "\n%d. %s", sub.ID, sub.URL)
			if sub.Seen == nil {
				b.WriteString(" (ожидает первой успешной проверки)")
			}
		}
		return b.String()
	case "/unsubscribe":
		feedID, err := strconv.Atoi(arg)
		if err != nil || feedID < 1 {
			return "Формат: /unsubscribe номер. Номера лент: /feeds."
		}
		found, err := s.change(id, func(chat *chatState) bool {
			for i, sub := range chat.Feeds {
				if sub.ID == feedID {
					chat.Feeds = append(chat.Feeds[:i], chat.Feeds[i+1:]...)
					return true
				}
			}
			return false
		})
		if err != nil {
			return storageError(err)
		}
		if !found {
			return "Подписка на ленту не найдена."
		}
		return fmt.Sprintf("Подписка на ленту №%d удалена.", feedID)
	}
	return ""
}

type feedBody struct {
	XML string `xml:",innerxml"`
}

type feedItem struct {
	GUID        string   `xml:"guid"`
	ID          string   `xml:"id"`
	Title       string   `xml:"title"`
	Description feedBody `xml:"description"`
	Summary     feedBody `xml:"summary"`
	Content     feedBody `xml:"content"`
	Encoded     feedBody `xml:"http://purl.org/rss/1.0/modules/content/ encoded"`
	Links       []struct {
		Text string `xml:",chardata"`
		Href string `xml:"href,attr"`
		Rel  string `xml:"rel,attr"`
	} `xml:"link"`
}

type feedEntry struct {
	key, revision, title, link string
}

func feedHash(parts ...string) string {
	h := sha256.New()
	for _, part := range parts {
		fmt.Fprintf(h, "%d:%s", len(part), part)
	}
	return hex.EncodeToString(h.Sum(nil))
}

func parseFeed(data []byte, base *url.URL) ([]feedEntry, error) {
	var doc struct {
		XMLName xml.Name
		Channel *struct {
			Items []feedItem `xml:"item"`
		} `xml:"channel"`
		Entries []feedItem `xml:"entry"`
	}
	if err := xml.Unmarshal(data, &doc); err != nil {
		return nil, err
	}
	items := doc.Entries
	switch {
	case doc.XMLName.Local == "rss" && doc.Channel != nil:
		items = doc.Channel.Items
	case doc.XMLName.Local == "feed" && doc.XMLName.Space == "http://www.w3.org/2005/Atom":
	default:
		return nil, errors.New("not an RSS or Atom feed")
	}
	if len(items) > maxFeedEntries {
		return nil, errors.New("too many entries in feed")
	}
	entries := make([]feedEntry, 0, len(items))
	keys := make(map[string]bool, len(items))
	for _, item := range items {
		var link string
		for _, candidate := range item.Links {
			if candidate.Rel != "" && candidate.Rel != "alternate" {
				continue
			}
			raw := strings.TrimSpace(candidate.Href)
			if raw == "" {
				raw = strings.TrimSpace(candidate.Text)
			}
			if raw != "" {
				u, err := base.Parse(raw)
				if err == nil && (u.Scheme == "https" || u.Scheme == "http") && u.Hostname() != "" && u.User == nil {
					link = u.String()
					break
				}
			}
		}
		title := strings.Join(strings.Fields(item.Title), " ")
		revision := feedHash(title, link, item.Description.XML, item.Summary.XML, item.Content.XML, item.Encoded.XML)
		key := strings.TrimSpace(item.GUID)
		if key == "" {
			key = strings.TrimSpace(item.ID)
		}
		if key == "" {
			key = link
		}
		if key == "" {
			key = revision
		}
		key = feedHash(key)
		if !keys[key] {
			keys[key] = true
			entries = append(entries, feedEntry{key, revision, title, link})
		}
	}
	return entries, nil
}

func fetchFeed(ctx context.Context, client *http.Client, raw string) ([]feedEntry, error) {
	u, err := feedURL(raw)
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", "sakanotifybot/1.0 (RSS/Atom reader)")
	req.Header.Set("Accept", "application/rss+xml, application/atom+xml, application/xml, text/xml, */*;q=0.1")
	res, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("unexpected HTTP status %d", res.StatusCode)
	}
	data, err := io.ReadAll(io.LimitReader(res.Body, maxFeedSize+1))
	if err != nil {
		return nil, err
	}
	if len(data) > maxFeedSize {
		return nil, errors.New("feed response too large")
	}
	return parseFeed(data, res.Request.URL)
}

type feedSubscriber struct {
	chatID int64
	sub    feedSubscription
}

type feedSender func(context.Context, int64, string) error

type feedFetcher func(context.Context, string) ([]feedEntry, error)

func limitFeedText(s string, maxRunes int) string {
	for i := range s {
		if maxRunes == 0 {
			return s[:i] + "…"
		}
		maxRunes--
	}
	return s
}

func deliverFeed(ctx context.Context, s *store, subscriber feedSubscriber, entries []feedEntry, send feedSender) error {
	id, sub := subscriber.chatID, subscriber.sub
	next := make(map[string]string, len(entries))
	for _, entry := range entries {
		if sub.Seen == nil {
			// Establish a baseline without sending the existing history.
			next[entry.key] = entry.revision
		} else if previous, ok := sub.Seen[entry.key]; ok {
			next[entry.key] = previous
		}
	}
	if sub.Seen != nil {
		sent := 0
		// Most feeds put the newest entries first. Deliver the older ones first.
		for i := len(entries) - 1; i >= 0 && sent < 10 && ctx.Err() == nil; i-- {
			entry := entries[i]
			previous, seen := sub.Seen[entry.key]
			if seen && previous == entry.revision {
				continue
			}
			if !s.hasFeed(id, sub.ID) {
				return nil
			}
			kind := "📰 Новая запись в ленте"
			if seen {
				kind = "📰 Обновление записи в ленте"
			}
			title, link := entry.title, entry.link
			if title == "" {
				title = "Запись без заголовка"
			}
			if link == "" || len(link) > 2000 {
				link = sub.URL
			}
			if err := send(ctx, id, kind+"\n"+limitFeedText(title, 500)+"\n"+link); err != nil {
				break // Persist only successful sends; try again at the next poll.
			}
			next[entry.key] = entry.revision
			sent++
		}
	}
	if sub.Seen != nil && maps.Equal(sub.Seen, next) {
		return nil
	}
	_, err := s.change(id, func(chat *chatState) bool {
		for i, current := range chat.Feeds {
			if current.ID == sub.ID {
				chat.Feeds[i].Seen = next
				return true
			}
		}
		return false
	})
	return err
}

func pollFeeds(ctx context.Context, s *store, fetch feedFetcher, send feedSender) {
	var workers sync.WaitGroup
	// Fetch each distinct URL once per poll, with bounded concurrency.
	jobs := make(chan []feedSubscriber)
	for range 4 {
		workers.Go(func() {
			for subscribers := range jobs {
				if ctx.Err() != nil {
					return
				}
				entries, err := fetch(ctx, subscribers[0].sub.URL)
				if err != nil {
					// The URL (including its query string) may be private to the user.
					continue
				}
				for _, subscriber := range subscribers {
					if err := deliverFeed(ctx, s, subscriber, entries, send); err != nil {
						log.Printf("Ошибка записи состояния ленты: %v", err)
					}
				}
			}
		})
	}
queue:
	for _, subscribers := range s.feedSubscriptions() {
		select {
		case jobs <- subscribers:
		case <-ctx.Done():
			break queue
		}
	}
	close(jobs)
	workers.Wait()
}

func runFeeds(ctx context.Context, s *store, send feedSender) {
	client := newFeedClient()
	defer client.CloseIdleConnections()
	ticker := time.NewTicker(feedInterval)
	defer ticker.Stop()
	for ctx.Err() == nil {
		pollFeeds(ctx, s, func(ctx context.Context, raw string) ([]feedEntry, error) {
			return fetchFeed(ctx, client, raw)
		}, send)
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}
