package main

import "os"

type Config struct {
	PostgresDSN string
	HTTPAddr    string
}

func loadConfig() Config {
	return Config{
		PostgresDSN: envOr("POSTGRES_DSN", "postgres://wiki:wiki@postgres:5432/wiki?sslmode=disable"),
		HTTPAddr:    envOr("HTTP_ADDR", ":8080"),
	}
}

func envOr(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}
