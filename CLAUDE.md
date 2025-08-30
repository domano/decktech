# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## Project Overview

This is a local-first MTG (Magic: The Gathering) card similarity service using text embeddings and a vector database. The system recommends similar cards based on one or more input cards by generating embeddings from card text and mechanics, storing them in Weaviate, and performing nearest-neighbor search.

## Core Architecture

- **Vector Database**: Weaviate with HNSW index and cosine distance
- **Embeddings**: ModernBERT (`Alibaba-NLP/gte-modernbert-base`) generated offline
- **Data Source**: Scryfall bulk JSON (Oracle cards)
- **Backend**: Go REST service with GraphQL queries to Weaviate
- **UIs**: Bubble Tea TUIs and SSR web app for browsing/testing

## Key Commands

### Database Operations
```bash
# Start/stop Weaviate
make weaviate-up
make weaviate-down

# Apply database schema
make schema-apply
```

### Data Pipeline
```bash
# Download Scryfall bulk data
make data-download

# Generate sample embeddings and ingest
make embed-sample ingest-sample

# Continuous batched embedding with checkpointing
make embed-batches BATCH=1000

# Clean embeddings and restart
make clean-embeddings
```

### Build & Run Services
```bash
# Build binaries
make build          # REST server (similarityd)
make build-tui      # TUI importer (decktech)
make build-browser  # TUI browser (deckbrowser)
make build-web      # Web server (deckweb)

# Run services
make run     # REST server on :8088
make tui     # TUI importer interface
make browser # TUI database browser
make web     # Web server on :8090
```

### Development
```bash
# Install Go dependencies
make deps-go

# Install Python dependencies for embeddings
make deps-py

# Full smoke test (weaviate + schema + sample data + ingest)
make smoke
```

## Code Architecture

### Main Components
- **cmd/similarityd/**: REST API server (`/similar`, `/healthz`, `/config`)
  - `fetchVectorForName()`: Looks up card vectors by name (exact + LIKE fallback)
  - `averageVectors()`: Combines multiple card vectors with L2 normalization
  - `searchNearVector()`: Performs GraphQL nearVector similarity search
- **cmd/decktech/**: Bubble Tea TUI for data import and batch operations
- **cmd/deckbrowser/**: Bubble Tea TUI for browsing and searching cards
- **cmd/web/**: Server-side rendered web app with search and browse functionality
- **pkg/weaviateclient/**: Shared typed GraphQL client for Card queries
- **pkg/progress/**: Embedding checkpoint utilities for resumable batch processing

### Data Flow
1. **Ingestion**: Scryfall JSON → Python embedding scripts → Weaviate batch objects
2. **Query**: Card names → vector lookup → average vectors → nearVector search → similar cards

### Scripts Directory
- `embed_cards.py`: Generate embeddings for card batches with mechanic-aware tagging
- `embed_batches.sh`: Continuous batching with checkpoint resume
- `apply_schema.sh`: Robust schema application handling different Weaviate versions
- `ingest_batch.sh`: POST batch JSON to Weaviate
- `download_scryfall.py`: Fetch Scryfall bulk data
- `clean_embeddings.sh`: Reset local state and optionally wipe database

## Environment Variables

### Service Configuration
- `WEAVIATE_URL`: Vector database endpoint (default: `http://localhost:8080`)

### Embedding Pipeline
- `MODEL`: Embedding model name (default: `Alibaba-NLP/gte-modernbert-base`)
- `CHECKPOINT`: Progress checkpoint file path (default: `data/embedding_progress.json`)
- `OUTDIR`: Batch output directory (default: `data`)
- `EMBED_TAGS_WEIGHT`: Mechanic tag emphasis multiplier (default: `2`)
- `INCLUDE_NAME`: Include card names in embeddings (default: `false`)
- `MAX_STEPS`: Limit batches in continuous mode (optional)

## Key Data Structures

### Weaviate Card Class
- Core fields: `scryfall_id`, `name`, `type_line`, `oracle_text`, `mana_cost`
- Metadata: `colors`, `keywords`, `legalities`, `image_normal`, `edhrec_rank`
- Vector: 768-dim ModernBERT embeddings (cosine distance, HNSW indexed)

### API Structures
- **SimilarRequest**: `{"names": ["Card A"], "k": 10}`
- **CardResult**: Card data with `similarity` score (1 - distance)

## Development Patterns

### Embedding Input Generation
Cards are embedded using structured text including type, mana cost, colors, and oracle text. The system includes mechanic-aware tagging that extracts domain-specific tags (e.g., `tutor`, `etb_trigger`) and can emphasize them via `EMBED_TAGS_WEIGHT`.

### Checkpointing System
The embedding pipeline uses `data/embedding_progress.json` to track `next_offset` and enable stop/resume functionality across large datasets (~30k+ cards).

### Multi-Vector Queries
When multiple card names are provided, their vectors are averaged and renormalized before similarity search, allowing for "cards like A and B" queries.

## Testing Approach

Testing is primarily done through the TUIs and web interface:
- Use `make smoke` for end-to-end validation
- TUI browser provides interactive search and similarity testing
- Web interface offers visual card browsing with images and metadata
- REST API can be tested directly: `curl -X POST localhost:8088/similar -H 'content-type: application/json' -d '{"names":["Lightning Bolt"],"k":5}'`

## Common Workflows

### Initial Setup
1. `make weaviate-up schema-apply`
2. `make data-download` 
3. `make embed-sample ingest-sample`
4. `make run` to start the service

### Full Data Pipeline
1. `make embed-batches BATCH=1000` (runs continuously with checkpointing)
2. Monitor progress via TUI: `make tui` → Show Status

### Development Iteration
1. Make code changes to `cmd/similarityd/`
2. `make build run` to test REST API
3. `make tui` or `make browser` to test interactively