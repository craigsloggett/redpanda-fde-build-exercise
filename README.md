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
docker compose up --build
```

> [!TIP]
> `make up` does the same detached, opens the page in your browser once every service is up, and follows the logs. `make down` stops the stack.

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

## Surprises and Production Notes

## Why This Matters
