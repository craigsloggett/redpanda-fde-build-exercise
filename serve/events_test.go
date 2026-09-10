package main

import (
	"bufio"
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestEventsStreamsWakeupsUntilClosed(t *testing.T) {
	broker := newBroker(nil, slog.New(slog.NewTextHandler(io.Discard, nil)))
	srv := httptest.NewServer((&Server{Events: broker}).Handler())

	defer srv.Close()

	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, srv.URL+"/events", nil)
	if err != nil {
		t.Fatalf("build request: %v", err)
	}

	res, err := srv.Client().Do(req)
	if err != nil {
		t.Fatalf("GET /events: %v", err)
	}

	defer func() { _ = res.Body.Close() }()

	if got := res.Header.Get("Content-Type"); got != "text/event-stream" {
		t.Fatalf("Content-Type = %q, want text/event-stream", got)
	}

	reader := bufio.NewReader(res.Body)

	line, err := reader.ReadString('\n')
	if err != nil || !strings.HasPrefix(line, ":") {
		t.Fatalf("first line should be a comment that opens the stream, got %q (%v)", line, err)
	}

	broker.publish("42")
	broker.publish("43")

	var got strings.Builder

	for !strings.Contains(got.String(), "data: ") {
		if line, err = reader.ReadString('\n'); err != nil {
			t.Fatalf("stream ended before the wake-up arrived: %v", err)
		}

		got.WriteString(line)
	}

	if want := "event: verdict\ndata: 4"; !strings.Contains(got.String(), want) {
		t.Fatalf("stream = %q, want it to contain %q", got.String(), want)
	}

	broker.close()

	for {
		_, err = reader.ReadString('\n')
		if err != nil {
			break
		}
	}

	if !errors.Is(err, io.EOF) {
		t.Fatalf("closing the broker should end the stream cleanly, got %v", err)
	}
}
