package main

import (
	"context"
	"fmt"
	"log"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"
)

const (
	maxTasks = 20
	maxRunes = 90
	help     = "/start — включить напоминания\n" +
		"/add текст — добавить задачу\n" +
		"/list — показать активные задачи\n" +
		"/done номер — закрыть задачу\n" +
		"/stop — выключить напоминания (задачи сохранятся)\n" +
		"/subscribe URL — подписаться на RSS или Atom\n" +
		"/feeds — показать подписки на ленты\n" +
		"/unsubscribe номер — отписаться от ленты"
)

func formatTasks(tasks []task) string {
	if len(tasks) == 0 {
		return "Активных задач нет."
	}
	var b strings.Builder
	b.WriteString("Активные задачи:")
	for _, t := range tasks {
		fmt.Fprintf(&b, "\n%d. %s", t.ID, t.Text)
	}
	return b.String()
}

func command(s *store, id int64, username, input string) string {
	name, arg := strings.TrimSpace(input), ""
	if i := strings.IndexFunc(name, unicode.IsSpace); i >= 0 {
		name, arg = name[:i], strings.TrimSpace(name[i:])
	}
	if prefix, target, found := strings.Cut(name, "@"); found {
		if !strings.EqualFold(target, username) {
			return ""
		}
		name = prefix
	}
	switch name {
	case "/help":
		return help
	case "/list":
		return formatTasks(s.chat(id).Tasks)
	case "/subscribe", "/feeds", "/unsubscribe":
		return feedCommand(s, id, name, arg)
	case "/start", "/stop":
		enable := name == "/start"
		_, err := s.change(id, func(chat *chatState) bool {
			if chat.Subscribed == enable {
				return false
			}
			chat.Subscribed = enable
			return true
		})
		if err != nil {
			return storageError(err)
		}
		if enable {
			return "Напоминания включены. Буду писать каждый час, даже если задач нет.\n\n" + help
		}
		return "Напоминания выключены. Задачи сохранены."
	case "/add":
		arg = strings.Join(strings.Fields(arg), " ")
		if arg == "" || utf8.RuneCountInString(arg) > maxRunes {
			return fmt.Sprintf("Формат: /add текст (до %d символов).", maxRunes)
		}
		var added int
		ok, err := s.change(id, func(chat *chatState) bool {
			if len(chat.Tasks) >= maxTasks {
				return false
			}
			chat.NextID++
			added = chat.NextID
			chat.Tasks = append(chat.Tasks, task{ID: added, Text: arg})
			return true
		})
		if err != nil {
			return storageError(err)
		}
		if !ok {
			return fmt.Sprintf("Лимит: %d активных задач.", maxTasks)
		}
		return fmt.Sprintf("Добавлена задача №%d.", added)
	case "/done":
		taskID, err := strconv.Atoi(arg)
		if err != nil || taskID < 1 {
			return "Формат: /done номер. Номера можно посмотреть через /list."
		}
		found, err := s.change(id, func(chat *chatState) bool {
			for i, t := range chat.Tasks {
				if t.ID == taskID {
					chat.Tasks = append(chat.Tasks[:i], chat.Tasks[i+1:]...)
					return true
				}
			}
			return false
		})
		if err != nil {
			return storageError(err)
		}
		if !found {
			return "Задача не найдена."
		}
		return fmt.Sprintf("Задача №%d закрыта.", taskID)
	default:
		if strings.HasPrefix(name, "/") {
			return "Неизвестная команда. Список команд: /help."
		}
		return ""
	}
}

func storageError(err error) string {
	log.Printf("Ошибка записи состояния: %v", err)
	return "Не удалось сохранить изменения. Попробуйте ещё раз."
}

type sender func(context.Context, int64, string)

func sendReminders(ctx context.Context, s *store, send sender) {
	for id, chat := range s.subscriptions() {
		if ctx.Err() != nil {
			return
		}
		send(ctx, id, "⏰ Ежечасное напоминание\n\n"+formatTasks(chat.Tasks))
	}
}

func runReminders(ctx context.Context, s *store, send sender) {
	for ctx.Err() == nil {
		now := time.Now().UTC()
		timer := time.NewTimer(now.Truncate(time.Hour).Add(time.Hour).Sub(now))
		select {
		case <-ctx.Done():
			timer.Stop()
			return
		case <-timer.C:
			sendReminders(ctx, s, send)
		}
	}
}
