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

func newKafkaClient(cfg Config) (*kgo.Client, error) {
	return kgo.NewClient(
		kgo.SeedBrokers(cfg.Brokers...),
		kgo.ClientID("reasoner"),
		kgo.ConsumeTopics(cfg.TopicIn),
		kgo.ConsumerGroup(cfg.ConsumerGroup),
		kgo.ConsumeResetOffset(kgo.NewOffset().AtStart()),
		kgo.DisableAutoCommit(),
	)
}

// Run handles records one at a time. A record is committed only after its verdict was produced, so a
// crash mid-record replays it on restart and the Postgres upsert absorbs the duplicate.
func (c *Consumer) Run(ctx context.Context) error {
	for {
		fetches := c.Client.PollRecords(ctx, 1)
		if ctx.Err() != nil {
			return ctx.Err()
		}

		if fetches.IsClientClosed() {
			return errors.New("kafka client closed")
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
	var in Input
	if err := json.Unmarshal(rec.Value, &in); err != nil || in.RevID == 0 {
		// A record that cannot be decoded would block the partition forever if it were retried.
		c.Log.Warn("skipping undecodable record", "partition", rec.Partition, "offset", rec.Offset, "err", err)
		return c.commit(ctx, rec)
	}

	log := c.Log.With("rev_id", in.RevID, "title", in.Title)

	start := time.Now()

	v, err := retryUntilReachable(ctx, log, "model", func() (Verdict, error) {
		return c.Reasoner.Reason(ctx, in)
	})
	if err != nil {
		return err
	}

	out := Output{
		Input:      in,
		Verdict:    v,
		Model:      c.Model,
		LatencyMS:  time.Since(start).Milliseconds(),
		ReasonedAt: time.Now().UTC(),
	}

	value, err := json.Marshal(out)
	if err != nil {
		return fmt.Errorf("encode verdict: %w", err)
	}

	log.Info("verdict", "label", v.Label, "confidence", v.Confidence, "route", v.Route, "steps", v.Steps, "latency_ms", out.LatencyMS)

	record := &kgo.Record{Topic: c.TopicOut, Key: []byte(strconv.FormatInt(in.RevID, 10)), Value: value}

	if _, err := retryUntilReachable(ctx, log, "produce", func() (struct{}, error) {
		return struct{}{}, c.Client.ProduceSync(ctx, record).FirstErr()
	}); err != nil {
		return err
	}

	return c.commit(ctx, rec)
}

func (c *Consumer) commit(ctx context.Context, rec *kgo.Record) error {
	if err := c.Client.CommitRecords(ctx, rec); err != nil {
		// The verdict is already on the topic, so a failed commit only means a replay after restart.
		c.Log.Warn("commit failed", "partition", rec.Partition, "offset", rec.Offset, "err", err)
	}

	return ctx.Err()
}

// retryUntilReachable keeps trying an operation whose only failure mode is an unreachable dependency
// (model still loading, broker restarting). It gives up only when the service is shutting down.
func retryUntilReachable[T any](ctx context.Context, log *slog.Logger, what string, fn func() (T, error)) (T, error) {
	backoff := time.Second

	for {
		v, err := fn()
		if err == nil {
			return v, nil
		}

		if ctx.Err() != nil {
			return v, ctx.Err()
		}

		log.Warn(what+" unavailable, retrying", "err", err, "backoff", backoff)

		select {
		case <-ctx.Done():
			return v, ctx.Err()
		case <-time.After(backoff):
		}

		backoff = min(backoff*2, 30*time.Second)
	}
}
