# redpanda-fde-build-exercise

Judge live Wikipedia edits with an LLM to enable a human to triage the damaging ones first.

```mermaid
flowchart LR
  WM["Wikimedia<br/>recent changes"] --> IN["Connect<br/>ingest"]
  subgraph RP["Redpanda"]
    RAW["wiki.edits.raw"]
    EN["wiki.edits.enriched"]
    VD["wiki.edits.verdicts"]
  end
  IN --> RAW
  RAW --> TR["Connect<br/>transform"]
  TR <-.-> MW["MediaWiki<br/>compare API"]
  TR --> EN
  EN --> RS["Reason<br/>triage → ground → challenge"]
  RS <-.-> OL["Ollama<br/>gemma4:e4b"]
  RS --> VD
  VD --> SK["Connect<br/>sink"]
  SK --> PG[("Postgres<br/>verdicts")]
  PG -- "rows + NOTIFY" --> SV["Serve"]
  SV -- "HTML + SSE" --> BR["Browser<br/>localhost:8080"]
```

A Connect [`http_client`](https://docs.redpanda.com/connect/components/inputs/http_client/) ingests the Wikimedia recent-changes firehose and ingests human edits to English articles to the `wiki.edits.raw` topic.

A Connect [`processor`](https://docs.redpanda.com/connect/components/processors/processors/) group then filters and tiers the raw changes and fetches the diff behind each one as a separate (rate-limited) external call. The filtered set of raw events are then stripped to include only the relevant fields, enriches them with the diff details and persists them to the `wiki.edits.enriched` topic. This step is necessary since the feed itself only says who changed which page and by how many bytes.

Reason then has a local model judge each diff, makes it quote its evidence, argues the close calls a second time, and writes a verdict back to the `wiki.edits.verdicts` topic.

A Connect [`sql_insert`](https://docs.redpanda.com/cloud-data-platform/develop/connect/components/outputs/sql_insert/) output sinks the verdicts into Postgres, and Serve turns that table into a live view at http://localhost:8080.

## Getting Started

You need Docker with Compose and about 12 GB of memory available to containers (the local model is 9.6 GB). On macOS that means raising the Docker Desktop or Colima memory limit.

The first start pulls `gemma4:e4b` into the Ollama volume, which takes a few minutes. `reason` waits until it is there.

```sh
docker compose up --build --detach
```

```sh
open http://localhost:8080
```

> [!TIP]
> `make up` does the same detached, opens the page in your browser once every service is up, and follows the logs.
> `make down` stops the stack.

Then open:
- http://localhost:8080 for the verdicts page, which updates as verdicts land
- http://localhost:8080/api/verdicts for JSON, filtered by `route`, `label`, `min_confidence`, and `limit`
- http://localhost:8080/api/stats for counts by route and label
- http://localhost:8081 for Redpanda Console, to look at the topics

On CPU the model takes tens of seconds per edit, so the first verdicts appear a minute or two after it finishes loading. Edits from logged-out editors, with an empty summary, or with a large byte change always reach the model. The rest are sampled at 5%.

> [!TIP]
> To use a hosted model instead, copy [`.env.example`](.env.example) to `.env` and set `LLM_BASE_URL`, `LLM_MODEL`, and `LLM_API_KEY`. Setting `OLLAMA_MODEL=` skips the download. Any provider that speaks the OpenAI chat-completions shape works. Ollama running natively on the host (`LLM_BASE_URL=http://host.docker.internal:11434/v1`) is much faster than Ollama in a container on a Mac.

### Things to Look At

**Loop** in [`reason/reason.go`](reason/reason.go) turns each edit into a routed verdict:

```mermaid
flowchart TD
  %% Edge order per node drives the Dagre layout: outcome, main path, retry.
  edit[Enriched edit] --> gate{Diff fetched?}
  gate -- no --> skipped([skipped])
  gate -- yes --> triage[Triage prompt]
  triage --> reply[Model reply]
  reply --> parse{Usable JSON?}
  parse -- "no, out of attempts" --> unusable([review as unreviewed])
  parse -- yes --> ground{Quote in diff?}
  parse -- "no, attempts left" --> repair[Send the error back]
  repair --> reply
  ground -- "no, again" --> cap[Cap confidence at low]
  cap --> review([review])
  ground -- yes --> close{Unclear or below high?}
  ground -- "no, first miss" --> requote[Ask for an exact quote]
  requote --> reply
  close -- no --> route{Route}
  close -- "yes, once" --> challenge[Challenge in a fresh conversation]
  challenge --> reply
  route -- otherwise --> review
  %% Longer links keep these labels clear of the otherwise label.
  route -- "damaging, at or above high" ---> flagged([flagged])
  route -- "constructive, at or above low" ---> ok([ok])
  %% Invisible links stack the retries in one column so the return arrows never cross.
  repair ~~~ requote
  requote ~~~ challenge
```

The model sees the title, the editor type, the summary, the byte delta, and the diff, and answers with a label, a confidence, a one-sentence reason, and a verbatim quote from the diff. The parser pulls the first JSON object that decodes out of whatever the model wrote, and the last attempt forces JSON mode. A quote counts as found when most of it appears in the diff as one unbroken run, since a small model mangles the odd character. The challenge must argue the opposite case before deciding. Its answer replaces the first unless it is unusable, in which case the first stands.

Labels are `constructive`, `vandalism`, `spam`, `unsourced_claim`, and `unclear`. `unreviewed` marks records the loop gave up on.

Reason commits an offset only after the verdict is on `wiki.edits.verdicts`. When it cannot reach the model or the broker it retries with backoff rather than recording a verdict ([`reason/consumer.go`](reason/consumer.go)).

**Prompts** in [`reason/prompt.go`](reason/prompt.go) are the system prompt that defines the labels and the JSON shape, the repair prompt for an unusable reply, the grounding prompt for a quote that is not in the diff, and the challenge prompt. Edit and rebuild `reason` to change them.

**Pipelines** are the three Connect configs. Everything that is not reasoning lives here.
- [`ingest/ingest.yaml`](ingest/ingest.yaml) reads the SSE firehose, keeps human edits to English Wikipedia articles, and writes them to `wiki.edits.raw`.
- [`transform/transform.yaml`](transform/transform.yaml) drops repeats and reverts, passes priority edits and a revision-id sample of the rest, fetches the unified diff from the MediaWiki compare API at 5 requests per second, and writes `wiki.edits.enriched`. A failed fetch is recorded on the message, not dropped, so the gate can skip it.
- [`serve/sink.yaml`](serve/sink.yaml) upserts verdicts into Postgres by revision id, one statement per verdict.

**Schema** in [`serve/schema.sql`](serve/schema.sql) is the `verdicts` table the sink upserts and Serve reads, plus the trigger that tells Serve about each upsert. The sink applies it on start.

**Env** vars are optional and go in `.env`:

| Variable          | Default | Description                                                                                                    |
| ----------------- | ------- | -------------------------------------------------------------------------------------------------------------- |
| `SAMPLE_PERMILLE` | `50`    | Non-priority edits that reach the model, per thousand, picked by revision id so a replay makes the same choice |
| `DIFF_MAX_CHARS`  | `4000`  | Longest diff sent to the model, in characters                                                                  |
| `HIGH_CONFIDENCE` | `0.8`   | Confidence at which a damaging verdict is flagged without a challenge                                          |
| `LOW_CONFIDENCE`  | `0.5`   | Confidence below which a constructive verdict goes to review                                                   |
| `MAX_ATTEMPTS`    | `3`     | Model calls per stage before the loop gives up                                                                 |
| `LOG_LEVEL`       | `info`  | `debug` logs every unusable model reply                                                                        |

**Testing** is `make test`. [`reason/reason_test.go`](reason/reason_test.go) drives the loop with a scripted model: dirty output, repairs, hallucinated evidence, the challenge pass, the exhausted budget, and the transport failure that must not produce a verdict. [`reason/parse_test.go`](reason/parse_test.go) covers the parser on its own. CI runs the same tests plus lint and a vulnerability check.

**Rebuild the table** with `make reset-sink`. It drops `verdicts`, deletes the sink's consumer group, and restarts the sink so it replays the topic from the beginning.

## Tradeoffs

### Connect as the Sink vs App-Side Writes From the Reasoning Service

The verdicts topic is the system of record. Postgres is a projection of it, and the sink can rebuild that projection for as long as the topic still holds the verdicts (a week at the default retention). Verdicts are produced to `wiki.edits.verdicts`, and a Connect [`sql_insert`](https://docs.redpanda.com/redpanda-connect/components/outputs/sql_insert) output in [`serve/sink.yaml`](serve/sink.yaml) upserts them into Postgres keyed on `rev_id`. The reasoning service does not open a database connection.

The alternative is what most services do first: Reason writes the row itself. That is fewer moving parts, and the page sees a verdict as soon as it is written. The problem is that Reason would then commit twice, once to the topic and once to Postgres, with no transaction across the two. The first failed write leaves the topic and the table disagreeing, and neither is the record any more.

With the topic as the record, a second consumer is a second Connect configuration, and `make reset-sink` rebuilds the table from the log. Replaying the topic surfaced one constraint on the sink. Postgres rejects a multi-row upsert that touches the same key twice, and Connect retries a failed batch indefinitely, so the sink writes one row per statement. That costs nothing here. The transform fetches diffs at five a second, so live traffic is at most five verdicts a second, and a replay is bounded by Postgres, which handles thousands of single-row upserts a second.

I would move the write into Reason if it ever had to be transactional with other application state (a reviewer claiming an edit, or a decision the next request must see). A sink connector cannot give that guarantee.

### One Classification Call vs the Multi-Step Loop

A key design decision I made early is that the model cannot flag an edit, or clear one, on evidence that is not in the diff. A verdict with an invented quote still reaches the topic, with its confidence capped and its route set to review. The worst a fabricated quote can do is cost a reviewer a look. Reason triages the edit, repairs malformed output with JSON mode reserved for the last attempt, and checks that the quoted evidence appears in the diff. When the quote is found, a challenge pass runs only if the label is `unclear` or the confidence is below the high threshold.

The alternative is one call: parse what the model says and route on its confidence. This is half the code, half the tokens on the edits that would otherwise need two calls, and latency that is easier to reason about. What stopped me is that a small local model produces confident labels with evidence that is not in the diff, and a one-call system would flag those on confidence alone. The grounding check sends a fabricated quote to `review`, never to `flagged`. The [`reason/reason_test.go`](reason/reason_test.go) case with the invented quote shows this (the model is retried once, then distrusted).

The loop records enough to tell me when to remove it. Every row stores `steps`, and the challenge adds `challenge:overturned` whenever it changes the label or the route the triage pass would have given, so how often the second call changes the outcome is one query against the table.

Cost is also bounded one layer up, in the transform pipeline, before anything reaches the model. Priority edits (large byte changes, blank summaries, temporary accounts) are never sampled out. The remainder is sampled at a tunable rate, deterministically by `rev_id` and spread evenly in time. This sample is a cost control and it is the benign control group. It is the only view of how the model behaves on ordinary edits, and therefore the only read on false positives among them.

I would drop the second call when that overturn rate approaches zero on a stronger model. I would move the bulk of the edits to a fine-tuned classifier once the sample has been labeled into an eval set, and keep the LLM for the unclear slice. That classifier is the shape Wikimedia arrived at with [ORES](https://www.mediawiki.org/wiki/ORES) and the revert-risk models on its successor, [Lift Wing](https://wikitech.wikimedia.org/wiki/Machine_Learning/LiftWing).

## Surprises and Production Notes

Surprisingly, the Wikipedia recent-changes feed carries no diff content, so the transform pipeline has to fetch every diff itself from the compare API. Doing that politely meant following Wikimedia's [User-Agent](https://foundation.wikimedia.org/wiki/Policy:Wikimedia_Foundation_User-Agent_Policy) and [rate](https://www.mediawiki.org/wiki/API:Etiquette) rules.

In production, the gaps in order of how much they would hurt:
1. There is no eval set, so I can say the machinery is sound but not what its precision or recall is. The sampled benign stream is the raw material that set would be built from. It also closes the loop on the tradeoff above: the overturn rate says when the challenge has stopped changing outcomes, and only an eval set says whether it was right when it did.
2. Ingest does not send `Last-Event-ID` when it reconnects, so a restart or a dropped connection loses events that [EventStreams](https://wikitech.wikimedia.org/wiki/Event_Platform/EventStreams) could have replayed (it keeps at least a week of history).
3. Topics have one partition and the reasoner handles one edit at a time, so against the unfiltered firehose the backlog grows without bound. The fix is more partitions and reasoner replicas, and only after that does sink batching matter.
4. Any 4xx from the model is retried as if it were a transport failure, which stalls the partition.
5. Dedupe is an in-process cache that a restart wipes. Every topic is keyed on revision id, so both copies of an edit reach the same consumer and replicas only miss a duplicate across a rebalance. The sink upsert is the backstop either way.

The eval set is the gap I would close first, and the labels are already in the feed. When a patroller reverts an edit, MediaWiki tags the reverted revision `mw-reverted`, and that tag change is an event on the same EventStreams service the ingest reads (`mediawiki.revision-tags-change`, which carries the `rev_id`). A fourth Connect config that keeps those events and writes a `reverted_at` onto the verdicts row by `rev_id` turns the table into the eval set without anyone labeling anything. Reverted within a couple of days is the proxy for damaging and survived is the proxy for fine, which is the proxy the revert-risk models are trained on, noisy in both directions. The two tiers then answer different questions. The priority tier is enriched for damage, so it gives recall on the edits that matter. The sampled tier is the base rate, so it gives the false-positive rate on ordinary edits, and without it precision would look better than it is. From there the numbers in the tradeoffs stop being opinions. Precision and recall of `flagged` are two queries, the high and low confidence thresholds become a curve instead of two env vars, and the rows tagged `challenge:overturned` can be checked against the revert to see whether the second call was right when it changed the outcome. The same rows, diff and label together, are the training set for the classifier that would take the bulk.

## Why This Matters

At a bank, more transactions and account changes flow through every day than the fraud and risk teams could review by hand. This system looks at everything that trips a risk rule and at a deliberate slice of the rest, judges each one from what actually changed, and puts the ones that look suspicious into an analyst's queue in real time, with a plain-language reason and the specific evidence attached. It cannot flag anything without that evidence: when the model quotes something that is not in the record, the case goes to a human, never to the flagged queue. The analyst spends their time on the cases most likely to be real instead of chasing random alerts or waiting for the overnight batch report. The slice of ordinary activity is there on purpose. It is the only way to measure how often the system bothers good customers. When it is wrong the cost is contained and visible (a few minutes on a false positive, or a risky transaction that clears before someone catches it). When there is no system like this the cost is worse and hidden. Either the team drowns in alerts and misses the real fraud, or tightens the rules and blocks good customers. Here it judges Wikipedia edits as a public stand-in, but the shape is the same as a transaction-monitoring or access-review queue.
