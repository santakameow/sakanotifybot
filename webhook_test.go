package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
)

func TestWebhook(t *testing.T) {
	s, err := openStore(filepath.Join(t.TempDir(), "state.json"))
	if err != nil {
		t.Fatal(err)
	}
	const secret = "test-secret"
	h := webhookHandler(secret, s, "my_bot")
	post := func(method, path, header, body string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(method, path, strings.NewReader(body))
		r.Header.Set("X-Telegram-Bot-Api-Secret-Token", header)
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		return w
	}
	const add = `{"update_id":1,"message":{"chat":{"id":42,"type":"private"},"text":"/add Купить хлеб"}}`
	for _, tc := range []struct {
		method, path, header, body string
		code                       int
	}{
		{http.MethodGet, webhookPath, secret, add, http.StatusMethodNotAllowed},
		{http.MethodPost, "/other", secret, add, http.StatusNotFound},
		{http.MethodPost, webhookPath, "", add, http.StatusForbidden},
		{http.MethodPost, webhookPath, "wrong-secret", add, http.StatusForbidden},
		{http.MethodPost, webhookPath, secret, add + "{}", http.StatusBadRequest},
		{http.MethodPost, webhookPath, secret, strings.Repeat(" ", (1<<20)+1), http.StatusBadRequest},
		{http.MethodPost, webhookPath, secret, strings.Replace(add, "private", "group", 1), http.StatusNoContent},
	} {
		if w := post(tc.method, tc.path, tc.header, tc.body); w.Code != tc.code {
			t.Errorf("%s %s: got %d, want %d", tc.method, tc.path, w.Code, tc.code)
		}
	}
	if len(s.chat(42).Tasks) != 0 {
		t.Fatal("rejected requests changed tasks")
	}

	w := post(http.MethodPost, webhookPath, secret, add)
	if w.Code != http.StatusOK {
		t.Fatalf("got HTTP %d: %s", w.Code, w.Body.String())
	}
	var reply struct {
		Method string `json:"method"`
		ChatID int64  `json:"chat_id"`
		Text   string `json:"text"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &reply); err != nil {
		t.Fatal(err)
	}
	if reply.Method != "sendMessage" || reply.ChatID != 42 || reply.Text != "Добавлена задача №1." {
		t.Fatalf("unexpected webhook reply: %+v", reply)
	}
	if tasks := s.chat(42).Tasks; len(tasks) != 1 || tasks[0].Text != "Купить хлеб" {
		t.Fatalf("unexpected tasks: %v", tasks)
	}
}
