package main

import (
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"
)

type Config struct {
	Brokers       []string
	TopicIn       string
	TopicOut      string
	ConsumerGroup string

	LLMBaseURL string
	LLMModel   string
	LLMAPIKey  string
	LLMTimeout time.Duration

	MaxAttempts    int
	HighConfidence float64
	LowConfidence  float64
}

var errConfig = errors.New("invalid config")

func loadConfig() (Config, error) {
	cfg := Config{
		Brokers:       strings.Split(envOr("REDPANDA_BROKERS", "redpanda:9092"), ","),
		TopicIn:       envOr("TOPIC_IN", "wiki.edits.enriched"),
		TopicOut:      envOr("TOPIC_OUT", "wiki.edits.verdicts"),
		ConsumerGroup: envOr("CONSUMER_GROUP", "reasoner"),
		LLMBaseURL:    strings.TrimRight(envOr("LLM_BASE_URL", "http://ollama:11434/v1"), "/"),
		LLMModel:      envOr("LLM_MODEL", "gemma4:e4b"),
		LLMAPIKey:     os.Getenv("LLM_API_KEY"),
	}

	var err error

	if cfg.LLMTimeout, err = envDuration("LLM_TIMEOUT", 5*time.Minute); err != nil {
		return cfg, err
	}

	if cfg.MaxAttempts, err = envInt("MAX_ATTEMPTS", 3); err != nil {
		return cfg, err
	}

	if cfg.HighConfidence, err = envFloat("HIGH_CONFIDENCE", 0.8); err != nil {
		return cfg, err
	}

	if cfg.LowConfidence, err = envFloat("LOW_CONFIDENCE", 0.5); err != nil {
		return cfg, err
	}

	if cfg.MaxAttempts < 1 {
		return cfg, fmt.Errorf("%w: MAX_ATTEMPTS must be at least 1, got %d", errConfig, cfg.MaxAttempts)
	}

	if !(0 <= cfg.LowConfidence && cfg.LowConfidence < cfg.HighConfidence && cfg.HighConfidence <= 1) {
		return cfg, fmt.Errorf("%w: need 0 <= LOW_CONFIDENCE < HIGH_CONFIDENCE <= 1, got %v and %v", errConfig, cfg.LowConfidence, cfg.HighConfidence)
	}

	return cfg, nil
}

func envOr(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}

	return def
}

func envInt(key string, def int) (int, error) {
	v := os.Getenv(key)
	if v == "" {
		return def, nil
	}

	n, err := strconv.Atoi(v)
	if err != nil {
		return 0, fmt.Errorf("%s: %w", key, err)
	}

	return n, nil
}

func envFloat(key string, def float64) (float64, error) {
	v := os.Getenv(key)
	if v == "" {
		return def, nil
	}

	f, err := strconv.ParseFloat(v, 64)
	if err != nil {
		return 0, fmt.Errorf("%s: %w", key, err)
	}

	return f, nil
}

func envDuration(key string, def time.Duration) (time.Duration, error) {
	v := os.Getenv(key)
	if v == "" {
		return def, nil
	}

	d, err := time.ParseDuration(v)
	if err != nil {
		return 0, fmt.Errorf("%s: %w", key, err)
	}

	return d, nil
}
