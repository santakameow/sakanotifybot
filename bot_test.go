package main

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestHourlyRemindersAfterRestart(t *testing.T) {
	path := filepath.Join(t.TempDir(), "data", "state.json")
	s, err := openStore(path)
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct {
		id   int64
		text string
	}{
		{1, "/start"}, {2, "/start"}, {2, "/add Купить хлеб"},
		{3, "/start"}, {3, "/add Спящая задача"}, {3, "/stop"},
		{4, "/add Без подписки"},
	} {
		command(s, c.id, "my_bot", c.text)
	}

	s, err = openStore(path)
	if err != nil {
		t.Fatal(err)
	}
	for range 2 {
		got := map[int64]string{}
		sendReminders(t.Context(), s, func(_ context.Context, id int64, text string) {
			if _, exists := got[id]; exists {
				t.Errorf("duplicate reminder for %d", id)
			}
			got[id] = text
		})
		if len(got) != 2 || !strings.Contains(got[1], "Активных задач нет.") ||
			!strings.Contains(got[2], "1. Купить хлеб") {
			t.Fatalf("unexpected reminders: %v", got)
		}
	}

	command(s, 2, "my_bot", "/done 1")
	if got := command(s, 2, "my_bot", "/list"); got != "Активных задач нет." {
		t.Fatalf("unexpected list after /done: %q", got)
	}
	if got := command(s, 3, "my_bot", "/list"); !strings.Contains(got, "Спящая задача") {
		t.Fatalf("/stop unexpectedly removed tasks: %q", got)
	}
	if got := command(s, 2, "my_bot", "/add@other_bot игнорировать"); got != "" {
		t.Fatalf("responded to a command for another bot: %q", got)
	}
	if got := command(s, 2, "my_bot", "/add@MY_BOT новая задача"); got != "Добавлена задача №2." {
		t.Fatalf("unexpected /add result: %q", got)
	}
}

func TestFailedSaveDoesNotChangeState(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.json")
	s, err := openStore(path)
	if err != nil {
		t.Fatal(err)
	}
	command(s, 1, "my_bot", "/add keep me")
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(path, 0700); err != nil {
		t.Fatal(err)
	}
	if got := command(s, 1, "my_bot", "/done 1"); !strings.Contains(got, "Не удалось сохранить") {
		t.Fatalf("save failure was not reported: %q", got)
	}
	if got := command(s, 1, "my_bot", "/list"); !strings.Contains(got, "1. keep me") {
		t.Fatalf("failed update changed state: %q", got)
	}
}
