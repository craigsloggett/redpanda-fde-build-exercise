package main

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

func main() {
	if err := run(); err != nil {
		slog.Error("reasoner stopped", "err", err)
		os.Exit(1)
	}
}

func run() error {
	level := slog.LevelInfo
	if strings.EqualFold(os.Getenv("LOG_LEVEL"), "debug") {
		level = slog.LevelDebug
	}
	log := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: level}))
	slog.SetDefault(log)

	cfg, err := loadConfig()
	if err != nil {
		return err
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	client, err := newKafkaClient(cfg)
	if err != nil {
		return err
	}
	defer client.Close()
	pingCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	if err := client.Ping(pingCtx); err != nil {
		return errors.New("cannot reach brokers " + strings.Join(cfg.Brokers, ",") + ": " + err.Error())
	}

	db, err := pgxpool.New(ctx, cfg.PostgresDSN)
	if err != nil {
		return err
	}
	defer db.Close()

	reasoner := &Reasoner{
		LLM: &ChatClient{
			BaseURL: cfg.LLMBaseURL,
			Model:   cfg.LLMModel,
			APIKey:  cfg.LLMAPIKey,
			HTTP:    &http.Client{Timeout: cfg.LLMTimeout},
		},
		MaxAttempts:    cfg.MaxAttempts,
		HighConfidence: cfg.HighConfidence,
		LowConfidence:  cfg.LowConfidence,
		Log:            log,
	}
	consumer := &Consumer{Client: client, TopicOut: cfg.TopicOut, Reasoner: reasoner, Model: cfg.LLMModel, Log: log}
	srv := &http.Server{
		Addr:              cfg.HTTPAddr,
		Handler:           NewServer(db, log).Handler(),
		ReadHeaderTimeout: 5 * time.Second,
	}

	errs := make(chan error, 2)
	go func() { errs <- consumer.Run(ctx) }()
	go func() {
		if err := srv.ListenAndServe(); !errors.Is(err, http.ErrServerClosed) {
			errs <- err
		}
	}()
	log.Info("reasoner started",
		"http", cfg.HTTPAddr, "llm", cfg.LLMBaseURL, "model", cfg.LLMModel,
		"topic_in", cfg.TopicIn, "topic_out", cfg.TopicOut, "group", cfg.ConsumerGroup)

	var runErr error
	select {
	case <-ctx.Done():
		log.Info("shutting down")
	case runErr = <-errs:
	}
	shutdownCtx, cancelShutdown := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancelShutdown()
	_ = srv.Shutdown(shutdownCtx)
	if errors.Is(runErr, context.Canceled) {
		return nil
	}
	return runErr
}
