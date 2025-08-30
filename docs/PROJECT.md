# MTG Card Similarity Service — Project Doc

## Overview
- Goal: Return similar Magic: The Gathering cards for one or more input cards using vector embeddings over card text/mechanics.
- Approach: Generate text embeddings from curated Scryfall fields, store vectors + metadata in Weaviate, and query with nearest‑neighbor search (top‑K). Backend in Go.

## Architecture
- Data source: Scryfall bulk JSON (Oracle/Default).
- Embeddings: Offline batch generation (bring‑your‑own vector). Prefer open‑source Sentence-Transformers family.
- Vector DB: Weaviate (vectorizer: none, HNSW index). Store full card metadata as scalar props.
- Service: Go app that resolves input card(s) → vector(s), optionally averages vectors, queries Weaviate, returns top‑K similar cards.

## Data Model (Scryfall → Weaviate)
- Class: `Card` (vectorizer: none, vector index: HNSW; cosine metric recommended).
- Core properties (subset, extendable):
  - `scryfall_id: text` (UUID, use as object ID)
  - `name: text`
  - `mana_cost: text`
  - `cmc: number`
  - `type_line: text`
  - `oracle_text: text`
  - `power: text`, `toughness: text`
  - `colors: text[]`, `color_identity: text[]`
  - `keywords: text[]`
  - `edhrec_rank: int`
  - `set: text`, `collector_number: text`, `rarity: text`, `layout: text`
  - `image_small: text`, `image_normal: text` (URIs)
  - `legalities: text` (JSON string; optional)

## Embedding Input (fields to encode)
- Include: `type_line`, `mana_cost` (coarsely), `oracle_text`, and `colors` as words.
- Optional: `name` (can add mild theme signal; default: exclude initially).
- Exclude: flavor text, artist, collector info (kept only as metadata).
- Example input string:
  - `Type: Instant\nManaCost: {R}\nColors: Red\nOracle: Lightning Bolt deals 3 damage to any target.`

Mechanic‑Aware Tagging (enhancement):
- Extract domain tags from `type_line` and `oracle_text` (e.g., `tutor`, `tutor_to_battlefield`, `attack_trigger`, `etb_trigger`, `mv_leq_3`, `type_enchantment`, `kw_aura`).
- Inject tags into the embedding text, optionally repeated via `EMBED_TAGS_WEIGHT` to emphasize mechanics. This improves similarity for nuanced effects (e.g., Zur‑style attack‑triggered tutors).

## Embedding Generation
- Model options (local/offline):
  - Fast: `all-MiniLM-L6-v2` (384 dims)
  - Better: `all-mpnet-base-v2` (768 dims)
  - Modern: `gte-modernbert-base` (768 dims)
- Normalize vectors to unit length for cosine similarity.
- Batch encode ~30–35k cards; store vectors alongside `scryfall_id`.

## Ingestion Pipeline
1) Download Scryfall bulk JSON (Oracle/Default).
2) Build embedding input text per card (handle multi-face by concatenating face texts).
3) Encode with chosen model; L2-normalize vectors.
4) Start Weaviate (vectorizer: none).
5) Create `Card` class schema.
6) Batch upsert cards with metadata + `vector` (bring-your-own vector).

## Query Flow (Service)
- Single card: lookup by `name` → get `_additional { vector id }` → nearVector search → return top‑K, excluding input ID.
- Multiple cards: fetch each vector, compute element-wise average (optionally weights), nearVector with combined vector.
- Optional filters: colors, type, legality via GraphQL where filters.

## API Sketch (Go service)
- REST (initial):
  - `POST /similar` body: `{ "names": ["Card1", "Card2"], "k": 10, "filters": { ... } }`
  - Response: list of cards with `name`, `type_line`, `oracle_text`, `image_normal`, `similarity`.
- GraphQL (later): `similarCards(cardNames: [String!]!, k: Int): [Card!]!`

## Local Dev
- Start Weaviate via Docker: see `ops/docker-compose.weaviate.yml`.
- Apply schema: see `weaviate/schema.json` and `scripts/apply_schema.sh`.
- Ingest data: run embedding pipeline (Python recommended) and batch upsert (curl or Go client).

## Embedding & Ingestion (Batched)
- One-time deps: `pip install transformers torch tqdm`.
- Download bulk data: `python scripts/download_scryfall.py -k oracle_cards -o data/oracle-cards.json`.
- Single batch (example: offset 1000, limit 100):
  - `python scripts/embed_cards.py --scryfall-json data/oracle-cards.json --batch-out data/weaviate_batch.sample_100.json --limit 100 --offset 1000 --checkpoint data/embedding_progress.json --model Alibaba-NLP/gte-modernbert-base`
  - `./scripts/ingest_batch.sh data/weaviate_batch.sample_100.json`
- Continuous batches with checkpointing:
  - `./scripts/embed_batches.sh data/oracle-cards.json 1000`
  - Environment:
    - `WEAVIATE_URL`: DB endpoint (default `http://localhost:8080`)
    - `MODEL`: override model name
    - `EMBED_TAGS_WEIGHT`: emphasize mechanic tags (default 2; higher = stronger emphasis)
    - `CHECKPOINT`: where progress is stored (default `data/embedding_progress.json`)
    - `OUTDIR`: where batch files are written (default `data`)
    - `MAX_STEPS`: limit number of loops in one run (optional)

UI Components (for local exploration):
- TUI Importer (`cmd/decktech`): menu for Download, Apply Schema, Single/Continuous batches, Clean Embeddings, Re‑embed Full, Status, Config (Model/Batch/Tags weight/Include name).
- TUI Browser (`cmd/deckbrowser`): search by name, browse with pagination, run “Similar” from a selection.
- Web SSR (`cmd/web`): search, browse, detailed card page (images, legalities/keywords), and Similar results in browser.

## Decisions (confirmed)
- Embedding model: `Alibaba-NLP/gte-modernbert-base`.
- Include card `name` in embeddings: No (exclude for now).
- API shape: Start with REST; add GraphQL later.
- Legalities representation: store as JSON string.

## Milestones
1) Infra ready: Dockerized Weaviate + schema applied.
2) Embedding pipeline implemented; vectors generated locally.
3) Data ingested (full card set) into Weaviate.
4) Go service: /similar endpoint returning top‑K results.
5) Filters and multi-card averaging.
6) Optional: GraphQL server and NL query support.

## Risks & Mitigations
- Embedding quality: iterate on input formatting and model choice.
- Name ambiguity: add exact/fuzzy lookup; store `scryfall_id` and set.
- Multi-face handling: concatenate face texts; store `card_faces` if needed (defer).
- Local resources: batch size tuning; CPU-only encoding is slower (acceptable for one-off run).

## References
- Scryfall API (bulk data, card schema)
- Weaviate docs (bring-your-own vectors, GraphQL nearVector)
- Max Woolf’s MTG embeddings writeup (effective field choices and results)
