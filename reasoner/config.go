package main

import (
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"
)

// Config is read once at startup so a bad value fails before any record is consumed.
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

	PostgresDSN string
	HTTPAddr    string
}

func loadConfig() (Config, error) {
	c := Config{
		Brokers:       strings.Split(envOr("REDPANDA_BROKERS", "redpanda:9092"), ","),
		TopicIn:       envOr("TOPIC_IN", "wiki.edits.enriched"),
		TopicOut:      envOr("TOPIC_OUT", "wiki.edits.verdicts"),
		ConsumerGroup: envOr("CONSUMER_GROUP", "reasoner"),
		LLMBaseURL:    strings.TrimRight(envOr("LLM_BASE_URL", "http://ollama:11434/v1"), "/"),
		LLMModel:      envOr("LLM_MODEL", "gemma4:e4b"),
		LLMAPIKey:     os.Getenv("LLM_API_KEY"),
		PostgresDSN:   envOr("POSTGRES_DSN", "postgres://wiki:wiki@postgres:5432/wiki?sslmode=disable"),
		HTTPAddr:      envOr("HTTP_ADDR", ":8080"),
	}

	var err error
	// A cold Ollama loads the model on the first request, which can take minutes on CPU.
	if c.LLMTimeout, err = envDuration("LLM_TIMEOUT", 5*time.Minute); err != nil {
		return c, err
	}
	if c.MaxAttempts, err = envInt("MAX_ATTEMPTS", 3); err != nil {
		return c, err
	}
	if c.HighConfidence, err = envFloat("HIGH_CONFIDENCE", 0.8); err != nil {
		return c, err
	}
	if c.LowConfidence, err = envFloat("LOW_CONFIDENCE", 0.5); err != nil {
		return c, err
	}

	if c.MaxAttempts < 1 {
		return c, fmt.Errorf("MAX_ATTEMPTS must be at least 1, got %d", c.MaxAttempts)
	}
	if !(0 <= c.LowConfidence && c.LowConfidence < c.HighConfidence && c.HighConfidence <= 1) {
		return c, fmt.Errorf("need 0 <= LOW_CONFIDENCE < HIGH_CONFIDENCE <= 1, got %v and %v", c.LowConfidence, c.HighConfidence)
	}
	return c, nil
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
