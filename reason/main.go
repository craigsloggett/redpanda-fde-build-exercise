package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"
)

func main() {
	if err := run(); err != nil {
		slog.Error("reason stopped", "err", err)
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
		return fmt.Errorf("cannot reach brokers %s: %w", strings.Join(cfg.Brokers, ","), err)
	}

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

	log.Info(
		"reason started",
		"llm", cfg.LLMBaseURL,
		"model", cfg.LLMModel,
		"topic_in", cfg.TopicIn,
		"topic_out", cfg.TopicOut,
		"group", cfg.ConsumerGroup,
	)

	if err := consumer.Run(ctx); !errors.Is(err, context.Canceled) {
		return err
	}

	return nil
}
