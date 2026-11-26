package main

import (
	"context"
	"errors"
	"net/http"
	"net/netip"
	"net/url"
	"path/filepath"
	"strings"
	"testing"
)

const testFeedURL = "https://discordstatus.com/history.rss"

func mustParseFeed(t *testing.T, data string) []feedEntry {
	t.Helper()
	base, err := feedURL(testFeedURL)
	if err != nil {
		t.Fatal(err)
	}
	entries, err := parseFeed([]byte(data), base)
	if err != nil {
		t.Fatal(err)
	}
	return entries
}

func TestParseAtom(t *testing.T) {
	entries := mustParseFeed(t, `<feed xmlns="http://www.w3.org/2005/Atom">
		<entry><id>urn:incident:1</id><title>Incident &amp; recovery</title>
		<link rel="self" href="entry.xml"/><link href="/incidents/1"/>
		<content type="xhtml"><div xmlns="http://www.w3.org/1999/xhtml">Resolved</div></content></entry>
	</feed>`)
	if len(entries) != 1 || entries[0].key != feedHash("urn:incident:1") ||
		entries[0].link != "https://discordstatus.com/incidents/1" || entries[0].title != "Incident & recovery" {
		t.Fatalf("unexpected Atom entries: %+v", entries)
	}
	base, _ := url.Parse(testFeedURL)
	for _, data := range []string{`<html/>`, `<rss/>`, `<rss><channel>`} {
		if _, err := parseFeed([]byte(data), base); err == nil {
			t.Errorf("accepted invalid feed %q", data)
		}
	}
}

func TestFeedNotificationsAfterRestart(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.json")
	s, err := openStore(path)
	if err != nil {
		t.Fatal(err)
	}
	for _, chat := range []int64{1, 2} {
		command(s, chat, "bot", "/subscribe "+testFeedURL)
		command(s, chat, "bot", "/subscribe "+testFeedURL)
		if len(s.chat(chat).Feeds) != 1 {
			t.Fatal("duplicate subscription")
		}
	}
	if got := command(s, 1, "bot", "/feeds"); !strings.Contains(got, "1. "+testFeedURL) {
		t.Fatalf("unexpected /feeds: %s", got)
	}
	old := `<item><guid>1</guid><title>Voice issue</title><link>/incidents/1</link><description>Investigating</description></item>`
	newItem := `<item><guid>2</guid><title>Login issue</title><link>/incidents/2</link></item>`
	rss := func(items string) []feedEntry {
		return mustParseFeed(t, `<rss version="2.0"><channel>`+items+`</channel></rss>`)
	}
	entries := rss(old)
	var fetches int
	fetch := func(_ context.Context, _ string) ([]feedEntry, error) {
		fetches++
		return entries, nil
	}
	var sent []string
	send := func(_ context.Context, chat int64, message string) error {
		if chat != 1 {
			t.Errorf("sent to unexpected chat %d", chat)
		}
		sent = append(sent, message)
		return nil
	}
	pollFeeds(t.Context(), s, fetch, send)
	if len(sent) != 0 || fetches != 1 {
		t.Fatalf("first poll: %d fetches, messages %v", fetches, sent)
	}
	command(s, 2, "bot", "/unsubscribe 1")
	command(s, 1, "bot", "/stop")
	s, err = openStore(path)
	if err != nil {
		t.Fatal(err)
	}
	entries = rss(newItem + strings.Replace(old, "Investigating", "Resolved", 1))
	pollFeeds(t.Context(), s, fetch, send)
	if len(sent) != 2 || !strings.Contains(sent[0], "Обновление") || !strings.Contains(sent[0], "Voice issue\nhttps://discordstatus.com/incidents/1") ||
		!strings.Contains(sent[1], "Новая запись") || !strings.Contains(sent[1], "Login issue") {
		t.Fatalf("unexpected notifications: %v", sent)
	}
	s, err = openStore(path)
	if err != nil {
		t.Fatal(err)
	}
	pollFeeds(t.Context(), s, fetch, send)
	if len(sent) != 2 {
		t.Fatalf("duplicate notifications after restart: %v", sent)
	}
}

func TestEmptyFeedBaselineAndSendRetry(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.json")
	s, err := openStore(path)
	if err != nil {
		t.Fatal(err)
	}
	command(s, 1, "bot", "/subscribe "+testFeedURL)
	var entries []feedEntry
	fetch := func(context.Context, string) ([]feedEntry, error) { return entries, nil }
	attempts := 0
	send := func(context.Context, int64, string) error {
		attempts++
		if attempts == 1 {
			return errors.New("temporary send failure")
		}
		return nil
	}
	pollFeeds(t.Context(), s, fetch, send)
	s, err = openStore(path)
	if err != nil {
		t.Fatal(err)
	}
	entries = mustParseFeed(t, `<rss><channel><item><guid>1</guid><title>First</title></item></channel></rss>`)
	for range 3 {
		pollFeeds(t.Context(), s, fetch, send)
	}
	if attempts != 2 {
		t.Fatalf("wanted one failed send and one successful retry, got %d attempts", attempts)
	}
}

func TestPublicFeedRequestsOnly(t *testing.T) {
	for _, ip := range []string{"127.0.0.1", "10.2.3.4", "169.254.169.254", "100.64.0.1", "224.0.0.1", "::1", "fd00::1", "::ffff:192.168.0.1", "64:ff9b::a00:1", "2002:7f00:1::"} {
		if publicFeedIP(netip.MustParseAddr(ip)) {
			t.Errorf("allowed non-public IP %s", ip)
		}
	}
	for _, ip := range []string{"1.1.1.1", "2606:4700:4700::1111"} {
		if !publicFeedIP(netip.MustParseAddr(ip)) {
			t.Errorf("rejected public IP %s", ip)
		}
	}
	client := newFeedClient()
	for _, raw := range []string{"http://example.com/rss", "https://127.0.0.1/rss", "https://[::1]/rss", "https://example.com:444/rss", "https://user:pass@example.com/rss"} {
		if _, err := feedURL(raw); err == nil {
			t.Errorf("accepted URL %s", raw)
		}
		req, err := http.NewRequest(http.MethodGet, raw, nil)
		if err != nil {
			t.Fatal(err)
		}
		if err := client.CheckRedirect(req, nil); err == nil {
			t.Errorf("accepted redirect to %s", raw)
		}
	}
	if conn, err := dialPublicFeed(t.Context(), "tcp", "127.0.0.1:443"); err == nil {
		conn.Close()
		t.Fatal("dialed a loopback address")
	}
}
