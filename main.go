// Command telegram-anthropic-chat runs a 1-on-1 Telegram chat bot backed by
// the Anthropic API with persistent per-user memories.
package main

import (
	"context"
	"flag"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/go-telegram/bot"

	"github.com/zoo/telegram-anthropic-chat/internal/app"
	"github.com/zoo/telegram-anthropic-chat/internal/config"
	"github.com/zoo/telegram-anthropic-chat/internal/llm"
	"github.com/zoo/telegram-anthropic-chat/internal/log"
	"github.com/zoo/telegram-anthropic-chat/internal/storage"
	"github.com/zoo/telegram-anthropic-chat/internal/tavily"
)

func main() {
	configPath := flag.String("config", "config.jsonc", "path to config file")
	flag.Parse()

	cfg, err := config.Load(*configPath)
	if err != nil {
		log.Print("main", "config load failed: %v", err)
		os.Exit(1)
	}

	store, err := storage.New("data")
	if err != nil {
		log.Print("main", "storage init failed: %v", err)
		os.Exit(1)
	}

	llmClient, err := llm.New(cfg.APIKey, cfg.Model, cfg.MaxTokens, cfg.DumpRequestsPath)
	if err != nil {
		log.Print("main", "llm init failed: %v", err)
		os.Exit(1)
	}
	if cfg.DumpRequestsPath != "" {
		log.Print("main", "dumping Anthropic requests to %s", cfg.DumpRequestsPath)
	}

	// Web search (Tavily) is optional: only enabled when tavilyApiKey is set.
	var tvClient *tavily.Client
	if cfg.TavilyAPIKey != "" {
		tvClient = tavily.New(cfg.TavilyAPIKey)
	}

	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()

	// Build the Telegram bot.
	botOpts := []bot.Option{
		bot.WithDefaultHandler(nil), // replaced below
		// Route the library's internal error/debug logs through the app's
		// logger so they follow the standard [timestamp] [topic] message format.
		bot.WithErrorsHandler(func(err error) {
			log.Print("tgbot", "%v", err)
		}),
		bot.WithDebugHandler(func(format string, args ...any) {
			log.Print("tgbot", format, args...)
		}),
	}
	if cfg.BotUpdateMethod == "webhook" && cfg.WebhookSecretToken != "" {
		botOpts = append(botOpts, bot.WithWebhookSecretToken(cfg.WebhookSecretToken))
	}

	tgbot, err := bot.New(cfg.BotToken, botOpts...)
	if err != nil {
		log.Print("main", "telegram bot init failed: %v", err)
		os.Exit(1)
	}

	application, err := app.New(cfg, store, llmClient, tgbot, tvClient)
	if err != nil {
		log.Print("main", "app init failed: %v", err)
		os.Exit(1)
	}
	tgbot.RegisterHandler(bot.HandlerTypeMessageText, "", bot.MatchTypeContains, application.Handler)

	log.Print("main", "bot starting as @%s via %s", botUsername(tgbot), cfg.BotUpdateMethod)

	if cfg.BotUpdateMethod == "webhook" {
		go runWebhook(ctx, cfg, tgbot)
	} else {
		go runPolling(ctx, tgbot)
	}

	<-ctx.Done()
	log.Print("main", "shutdown signal received, flushing sessions...")
	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 2*time.Minute)
	application.FlushAll(shutdownCtx)
	shutdownCancel()
	log.Print("main", "bye")
	os.Stdout.Sync()
}

func runPolling(ctx context.Context, tgbot *bot.Bot) {
	tgbot.Start(ctx)
}

func runWebhook(ctx context.Context, cfg *config.Config, tgbot *bot.Bot) {
	if cfg.WebhookPublicURL != "" {
		params := &bot.SetWebhookParams{URL: cfg.WebhookPublicURL}
		if cfg.WebhookSecretToken != "" {
			params.SecretToken = cfg.WebhookSecretToken
		}
		_, err := tgbot.SetWebhook(ctx, params)
		if err != nil {
			log.Print("main", "SetWebhook failed: %v", err)
		}
	}
	go tgbot.StartWebhook(ctx)

	mux := http.NewServeMux()
	mux.Handle("/", tgbot.WebhookHandler())
	addr := addrForPort(cfg.WebhookPort)
	srv := &http.Server{Addr: addr, Handler: mux}
	log.Print("main", "webhook server listening on %s", addr)

	// Shut down the HTTP server when the context is cancelled (e.g. on
	// SIGTERM/SIGINT) so ListenAndServe returns and the process can exit.
	errCh := make(chan error, 1)
	go func() {
		errCh <- srv.ListenAndServe()
	}()

	select {
	case <-ctx.Done():
		log.Print("main", "webhook context cancelled, shutting down HTTP server")
		shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer shutdownCancel()
		if err := srv.Shutdown(shutdownCtx); err != nil {
			log.Print("main", "webhook server shutdown error: %v", err)
		}
	case err := <-errCh:
		if err != nil && err != http.ErrServerClosed {
			log.Print("main", "webhook server error: %v", err)
		}
	}
}

func addrForPort(port int) string {
	return ":" + itoa(port)
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var b [12]byte
	pos := len(b)
	neg := n < 0
	if neg {
		n = -n
	}
	for n > 0 {
		pos--
		b[pos] = byte('0' + n%10)
		n /= 10
	}
	if neg {
		pos--
		b[pos] = '-'
	}
	return string(b[pos:])
}

func botUsername(b *bot.Bot) string {
	me, err := b.GetMe(context.Background())
	if err != nil {
		return "?"
	}
	return me.Username
}
