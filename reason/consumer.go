package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strconv"
	"time"

	"github.com/twmb/franz-go/pkg/kgo"
)

// Output is the record written to the verdicts topic: the input exactly as received plus the decision.
type Output struct {
	Input
	Verdict

	Model      string    `json:"model"`
	LatencyMS  int64     `json:"latency_ms"`
	ReasonedAt time.Time `json:"reasoned_at"`
}

type Consumer struct {
	Client   *kgo.Client
	TopicOut string
	Reasoner *Reasoner
	Model    string
	Log      *slog.Logger
}

var errClientClosed = errors.New("kafka client closed")

func newKafkaClient(cfg Config) (*kgo.Client, error) {
	client, err := kgo.NewClient(
		kgo.SeedBrokers(cfg.Brokers...),
		kgo.ClientID("reasoner"),
		kgo.ConsumeTopics(cfg.TopicIn),
		kgo.ConsumerGroup(cfg.ConsumerGroup),
		kgo.ConsumeResetOffset(kgo.NewOffset().AtStart()),
		kgo.DisableAutoCommit(),
	)
	if err != nil {
		return nil, fmt.Errorf("kafka client: %w", err)
	}

	return client, nil
}

// Run handles records one at a time. A record is committed only after its verdict was produced, so a
// crash mid-record replays it on restart and the Postgres upsert absorbs the duplicate.
func (c *Consumer) Run(ctx context.Context) error {
	for {
		fetches := c.Client.PollRecords(ctx, 1)

		if ctx.Err() != nil {
			return fmt.Errorf("poll records: %w", ctx.Err())
		}

		if fetches.IsClientClosed() {
			return errClientClosed
		}

		fetches.EachError(func(topic string, partition int32, err error) {
			c.Log.Error("fetch error", "topic", topic, "partition", partition, "err", err)
		})

		for _, rec := range fetches.Records() {
			if err := c.handle(ctx, rec); err != nil {
				return err
			}
		}
	}
}

func (c *Consumer) handle(ctx context.Context, rec *kgo.Record) error {
	var input Input
	if err := json.Unmarshal(rec.Value, &input); err != nil || input.RevID == 0 {
		// A record that cannot be decoded would block the partition forever if it were retried.
		c.Log.Warn("skipping undecodable record", "partition", rec.Partition, "offset", rec.Offset, "err", err)

		return c.commit(ctx, rec)
	}

	log := c.Log.With("rev_id", input.RevID, "title", input.Title)
	start := time.Now()

	verdict, err := retryUntilReachable(ctx, log, "model", func() (Verdict, error) {
		return c.Reasoner.Reason(ctx, input)
	})
	if err != nil {
		return err
	}

	out := Output{
		Input:      input,
		Verdict:    verdict,
		Model:      c.Model,
		LatencyMS:  time.Since(start).Milliseconds(),
		ReasonedAt: time.Now().UTC(),
	}

	value, err := json.Marshal(out)
	if err != nil {
		return fmt.Errorf("encode verdict: %w", err)
	}

	log.Info("verdict", "label", verdict.Label, "confidence", verdict.Confidence, "route", verdict.Route, "steps", verdict.Steps, "latency_ms", out.LatencyMS)

	record := &kgo.Record{Topic: c.TopicOut, Key: []byte(strconv.FormatInt(input.RevID, 10)), Value: value}

	_, err = retryUntilReachable(ctx, log, "produce", func() (struct{}, error) {
		return struct{}{}, c.Client.ProduceSync(ctx, record).FirstErr()
	})
	if err != nil {
		return err
	}

	return c.commit(ctx, rec)
}

func (c *Consumer) commit(ctx context.Context, rec *kgo.Record) error {
	if err := c.Client.CommitRecords(ctx, rec); err != nil {
		// The verdict is already on the topic, so a failed commit only means a replay after restart.
		c.Log.Warn("commit failed", "partition", rec.Partition, "offset", rec.Offset, "err", err)
	}

	if ctx.Err() != nil {
		return fmt.Errorf("commit: %w", ctx.Err())
	}

	return nil
}

// retryUntilReachable keeps trying an operation whose only failure mode is an unreachable dependency
// (model still loading, broker restarting). It gives up only when the service is shutting down.
func retryUntilReachable[T any](ctx context.Context, log *slog.Logger, what string, fn func() (T, error)) (T, error) {
	backoff := time.Second

	for {
		result, err := fn()
		if err == nil {
			return result, nil
		}

		if ctx.Err() != nil {
			return result, fmt.Errorf("%s: %w", what, ctx.Err())
		}

		log.Warn(what+" unavailable, retrying", "err", err, "backoff", backoff)

		select {
		case <-ctx.Done():
			return result, fmt.Errorf("%s: %w", what, ctx.Err())
		case <-time.After(backoff):
		}

		backoff = min(backoff*2, 30*time.Second)
	}
}
