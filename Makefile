SHELL := bash
.DEFAULT_GOAL := help

# Defaults (override via `make VAR=value`)
WEAVIATE_URL ?= http://localhost:8080
SCRYFALL_JSON ?= data/oracle-cards.json
CHECKPOINT ?= data/embedding_progress.json
OUTDIR ?= data
MODEL ?= Alibaba-NLP/gte-modernbert-base
BATCH ?= 1000

## help: Show this help
help:
	@grep -E '^[a-zA-Z_-]+:.*?## ' $(MAKEFILE_LIST) | sort | awk 'BEGIN {FS":.*?## "}; {printf "\033[36m%-22s\033[0m %s\n", $$1, $$2}'

## deps-go: Fetch Go module deps
deps-go:
	go mod tidy

## deps-py: Install Python deps (transformers, torch, tqdm)
deps-py:
	python3 -m pip install --upgrade pip
	python3 -m pip install --no-cache-dir transformers torch tqdm

## build: Build the REST server binary (similarityd)
build:
	go build -o similarityd ./cmd/similarityd

## build-tui: Build the TUI binary (decktech)
build-tui:
	go build -o decktech ./cmd/decktech

## run: Run the REST server (WEAVIATE_URL respected)
run: build
	WEAVIATE_URL=$(WEAVIATE_URL) ./similarityd

## tui: Run the TUI importer/batcher
tui: build-tui
	./decktech

## weaviate-up: Start Weaviate via Docker Compose
weaviate-up:
	docker compose -f ops/docker-compose.weaviate.yml up -d

## weaviate-down: Stop Weaviate
weaviate-down:
	docker compose -f ops/docker-compose.weaviate.yml down

## schema-apply: Apply/verify Weaviate schema
schema-apply:
	chmod +x scripts/apply_schema.sh || true
	WEAVIATE_URL=$(WEAVIATE_URL) ./scripts/apply_schema.sh

## data-download: Download Scryfall bulk data to $(SCRYFALL_JSON)
data-download:
	mkdir -p $(dir $(SCRYFALL_JSON))
	python3 scripts/download_scryfall.py -k oracle_cards -o $(SCRYFALL_JSON)

## embed-sample: Embed 100 cards (offset 1000) and write batch JSON
embed-sample:
	mkdir -p $(OUTDIR)
	python3 scripts/embed_cards.py \
	  --scryfall-json $(SCRYFALL_JSON) \
	  --batch-out $(OUTDIR)/weaviate_batch.sample_100.json \
	  --limit 100 --offset 1000 \
	  --checkpoint $(CHECKPOINT) \
	  --model $(MODEL)

## ingest-sample: Ingest the sample batch into Weaviate
ingest-sample:
	chmod +x scripts/ingest_batch.sh || true
	./scripts/ingest_batch.sh $(OUTDIR)/weaviate_batch.sample_100.json $(WEAVIATE_URL)

## embed-batches: Run continuous batched embedding + ingestion (BATCH=$(BATCH))
embed-batches:
	chmod +x scripts/embed_batches.sh || true
	WEAVIATE_URL=$(WEAVIATE_URL) MODEL=$(MODEL) OUTDIR=$(OUTDIR) CHECKPOINT=$(CHECKPOINT) \
	  ./scripts/embed_batches.sh $(SCRYFALL_JSON) $(BATCH)

## smoke: Quick end-to-end smoke (weaviate up, schema, sample embed+ingest)
smoke: weaviate-up schema-apply data-download embed-sample ingest-sample
	@echo "Smoke complete. Try: make run"

## clean: Remove built binaries (keeps data)
clean:
	rm -f similarityd decktech

## clean-embeddings: Remove local batches/checkpoint and try to wipe Card class
clean-embeddings:
	chmod +x scripts/clean_embeddings.sh || true
	WEAVIATE_URL=$(WEAVIATE_URL) OUTDIR=$(OUTDIR) CHECKPOINT=$(CHECKPOINT) ./scripts/clean_embeddings.sh

.PHONY: help deps-go deps-py build build-tui run tui \
	weaviate-up weaviate-down schema-apply data-download \
	embed-sample ingest-sample embed-batches smoke clean clean-embeddings
