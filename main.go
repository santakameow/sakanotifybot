package main

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/joho/godotenv"
	"github.com/mymmrac/telego"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := run(ctx); err != nil {
		log.Print(err)
		os.Exit(1)
	}
}

func run(ctx context.Context) error {
	if err := godotenv.Load(); err != nil && !errors.Is(err, os.ErrNotExist) {
		return errors.New("не удалось прочитать .env: проверьте права доступа и формат файла")
	}
	token := strings.TrimSpace(os.Getenv("BOT_TOKEN"))
	if token == "" {
		return errors.New("задайте BOT_TOKEN в окружении или в .env")
	}
	webhookURL := os.Getenv("BOT_WEBHOOK_URL")
	if !validWebhookURL(webhookURL) {
		return errors.New("задайте BOT_WEBHOOK_URL в формате https://ваш-домен/telegram/webhook")
	}
	secret := os.Getenv("BOT_WEBHOOK_SECRET")
	if !validWebhookSecret.MatchString(secret) {
		return errors.New("задайте BOT_WEBHOOK_SECRET: 1–256 символов A–Z, a–z, 0–9, _ или -")
	}
	addr := os.Getenv("BOT_LISTEN_ADDR")
	if addr == "" {
		addr = "127.0.0.1:8080"
	}
	dataFile := os.Getenv("BOT_DATA_FILE")
	if dataFile == "" {
		dataFile = "data/state.json"
	}
	s, err := openStore(dataFile)
	if err != nil {
		return fmt.Errorf("не удалось загрузить состояние: %w", err)
	}

	bot, err := telego.NewBot(token,
		telego.WithHTTPClient(&http.Client{Timeout: 40 * time.Second}),
		telego.WithDefaultLogger(false, true),
	)
	if err != nil {
		return errors.New("некорректный BOT_TOKEN")
	}
	initCtx, cancelInit := context.WithTimeout(ctx, 10*time.Second)
	me, err := bot.GetMe(initCtx)
	cancelInit()
	if err != nil {
		return fmt.Errorf("Telegram GetMe: %s", strings.ReplaceAll(err.Error(), token, "[TOKEN]"))
	}

	listener, err := net.Listen("tcp", addr)
	if err != nil {
		return fmt.Errorf("не удалось открыть порт вебхука: %w", err)
	}
	server := &http.Server{
		Handler:           webhookHandler(secret, s, me.Username),
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       10 * time.Second,
		WriteTimeout:      15 * time.Second,
		IdleTimeout:       60 * time.Second,
	}
	serveErr := make(chan error, 1)
	go func() { serveErr <- server.Serve(listener) }()
	defer func() {
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if server.Shutdown(shutdownCtx) != nil {
			_ = server.Close()
		}
	}()

	registerCtx, cancelRegister := context.WithTimeout(ctx, 10*time.Second)
	err = bot.SetWebhook(registerCtx, &telego.SetWebhookParams{
		URL:            webhookURL,
		SecretToken:    secret,
		MaxConnections: 1,
		AllowedUpdates: []string{"message"},
	})
	cancelRegister()
	if err != nil {
		redact := strings.NewReplacer(token, "[TOKEN]", secret, "[SECRET]")
		return fmt.Errorf("Telegram SetWebhook: %s", redact.Replace(err.Error()))
	}
	send := func(ctx context.Context, id int64, text string) error {
		sendCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
		defer cancel()
		_, err := bot.SendMessage(sendCtx, &telego.SendMessageParams{
			ChatID: telego.ChatID{ID: id}, Text: text,
		})
		if err != nil && ctx.Err() == nil {
			log.Printf("Telegram SendMessage: %s", strings.ReplaceAll(err.Error(), token, "[TOKEN]"))
		}
		return err
	}
	workerCtx, cancelWorkers := context.WithCancel(ctx)
	var workers sync.WaitGroup
	workers.Go(func() {
		runReminders(workerCtx, s, func(ctx context.Context, id int64, text string) {
			_ = send(ctx, id, text)
		})
	})
	workers.Go(func() { runFeeds(workerCtx, s, send) })
	defer func() {
		cancelWorkers()
		workers.Wait()
	}()

	log.Printf("Бот @%s запущен, вебхук слушает %s", me.Username, listener.Addr())
	select {
	case <-ctx.Done():
		return nil
	case err := <-serveErr:
		return fmt.Errorf("HTTP-сервер вебхука: %w", err)
	}
}
