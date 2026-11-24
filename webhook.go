package main

import (
	"crypto/sha256"
	"crypto/subtle"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"regexp"

	"github.com/mymmrac/telego"
)

const webhookPath = "/telegram/webhook"

var validWebhookSecret = regexp.MustCompile(`^[A-Za-z0-9_-]{1,256}$`)

func validWebhookURL(raw string) bool {
	u, err := url.Parse(raw)
	return err == nil && u.Scheme == "https" && u.Hostname() != "" &&
		u.User == nil && u.EscapedPath() == webhookPath && u.RawQuery == "" &&
		!u.ForceQuery && u.Fragment == ""
}

func webhookHandler(secret string, s *store, username string) http.Handler {
	expected := sha256.Sum256([]byte(secret))
	mux := http.NewServeMux()
	mux.HandleFunc(webhookPath, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			w.Header().Set("Allow", http.MethodPost)
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		got := sha256.Sum256([]byte(r.Header.Get("X-Telegram-Bot-Api-Secret-Token")))
		if subtle.ConstantTimeCompare(got[:], expected[:]) != 1 {
			w.WriteHeader(http.StatusForbidden)
			return
		}
		body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 1<<20))
		if err != nil {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		var update telego.Update
		if err := json.Unmarshal(body, &update); err != nil {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		m := update.Message
		if m == nil || m.Chat.Type != "private" || m.Text == "" {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		response := command(s, m.Chat.ID, username, m.Text)
		if response == "" {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		// Telegram executes this Bot API method from the webhook response.
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(struct {
			Method string `json:"method"`
			ChatID int64  `json:"chat_id"`
			Text   string `json:"text"`
		}{"sendMessage", m.Chat.ID, response})
	})
	return mux
}
