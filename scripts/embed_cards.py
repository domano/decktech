#!/usr/bin/env python3
"""
Embed Scryfall cards with Alibaba-NLP/gte-modernbert-base (or chosen model),
and produce a Weaviate batch file with bring-your-own vectors.

Usage:
  python scripts/embed_cards.py \
    --scryfall-json path/to/default-cards.json \
    --batch-out weaviate_batch.json \
    [--model Alibaba-NLP/gte-modernbert-base] \
    [--include-name]

Notes:
  - Requires: sentence-transformers, torch (CPU ok), tqdm (optional).
  - Vectors are L2-normalized for cosine distance in Weaviate.
  - Multi-face cards: concatenate face texts for embedding input; store original texts.
"""

import argparse
import json
import math
import os
import sys
from typing import Any, Dict, List, Optional


def _is_quiet() -> bool:
    return os.environ.get("EMBED_QUIET", "") == "1"


def load_model(name: str):
    """Try SentenceTransformer; fallback to transformers mean pooling."""
    try:
        from sentence_transformers import SentenceTransformer  # type: ignore
        return ("st", SentenceTransformer(name))
    except Exception:
        if not _is_quiet():
            print("Sentence-Transformers unavailable or model not ST-compatible; falling back to transformers.", file=sys.stderr)
        try:
            from transformers import AutoTokenizer, AutoModel  # type: ignore
            import torch  # type: ignore
        except Exception:
            print("ERROR: transformers/torch not installed. pip install transformers torch", file=sys.stderr)
            raise

        tokenizer = AutoTokenizer.from_pretrained(name)
        model = AutoModel.from_pretrained(name)

        class HFEncoder:
            def __init__(self, tokenizer, model):
                self.tok = tokenizer
                self.model = model

            def encode(self, texts: List[str], batch_size: int = 32, **kwargs):
                all_vecs = []
                for i in range(0, len(texts), batch_size):
                    batch = texts[i:i+batch_size]
                    enc = self.tok(batch, padding=True, truncation=True, return_tensors='pt', max_length=512)
                    with torch.no_grad():
                        out = self.model(**enc)
                    last_hidden = out.last_hidden_state  # (B, T, H)
                    mask = enc['attention_mask'].unsqueeze(-1)  # (B, T, 1)
                    masked = last_hidden * mask
                    sums = masked.sum(dim=1)  # (B, H)
                    lens = mask.sum(dim=1).clamp(min=1)
                    mean = sums / lens
                    all_vecs.extend(mean.cpu().numpy())
                return all_vecs

        return ("hf", HFEncoder(tokenizer, model))


def l2_normalize(vec: List[float]) -> List[float]:
    s = sum(v * v for v in vec)
    if s <= 0:
        return vec
    n = math.sqrt(s)
    return [v / n for v in vec]


def colors_to_words(colors: List[str]) -> str:
    mapping = {"W": "White", "U": "Blue", "B": "Black", "R": "Red", "G": "Green"}
    if not colors:
        return "Colorless"
    return "/".join(mapping.get(c, c) for c in colors)


def build_embed_text(card: Dict[str, Any], include_name: bool) -> str:
    name = card.get("name", "")
    type_line = card.get("type_line", "")
    mana_cost = card.get("mana_cost", "")
    colors = card.get("colors", []) or []
    colors_str = colors_to_words(colors)

    oracle_text = card.get("oracle_text")
    if not oracle_text:
        faces = card.get("card_faces") or []
        parts = []
        for f in faces:
            tl = f.get("type_line") or ""
            ot = f.get("oracle_text") or ""
            if tl or ot:
                parts.append(f"{tl} :: {ot}")
        oracle_text = " || ".join(parts)
    oracle_text = oracle_text or ""

    fields = []
    if include_name and name:
        fields.append(f"Name: {name}")
    if type_line:
        fields.append(f"Type: {type_line}")
    if mana_cost:
        fields.append(f"ManaCost: {mana_cost}")
    fields.append(f"Colors: {colors_str}")
    if oracle_text:
        fields.append(f"Oracle: {oracle_text}")
    return "\n".join(fields)


def extract_props(card: Dict[str, Any]) -> Dict[str, Any]:
    # Map Scryfall fields into Weaviate Card properties
    def get_image(card: Dict[str, Any], key: str) -> str:
        iu = card.get("image_uris") or {}
        if key in iu:
            return iu.get(key) or ""
        faces = card.get("card_faces") or []
        for f in faces:
            fiu = f.get("image_uris") or {}
            if key in fiu:
                return fiu.get(key) or ""
        return ""

    # Oracle: prefer top-level; else join faces
    oracle_text = card.get("oracle_text")
    if not oracle_text:
        faces = card.get("card_faces") or []
        parts = []
        for f in faces:
            ot = f.get("oracle_text") or ""
            if ot:
                parts.append(ot)
        oracle_text = " || ".join(parts)

    legalities = card.get("legalities")
    legalities_str = json.dumps(legalities, separators=(",", ":")) if legalities else ""

    return {
        "scryfall_id": card.get("id"),
        "name": card.get("name"),
        "mana_cost": card.get("mana_cost") or "",
        "cmc": float(card.get("cmc")) if card.get("cmc") is not None else None,
        "type_line": card.get("type_line") or "",
        "oracle_text": oracle_text or "",
        "power": card.get("power") or "",
        "toughness": card.get("toughness") or "",
        "colors": card.get("colors") or [],
        "color_identity": card.get("color_identity") or [],
        "keywords": card.get("keywords") or [],
        "edhrec_rank": int(card.get("edhrec_rank")) if card.get("edhrec_rank") is not None else None,
        "set": card.get("set") or "",
        "collector_number": card.get("collector_number") or "",
        "rarity": card.get("rarity") or "",
        "layout": card.get("layout") or "",
        "image_small": get_image(card, "small"),
        "image_normal": get_image(card, "normal"),
        "legalities": legalities_str,
    }


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("--scryfall-json", required=True, help="Path to Scryfall bulk JSON (Default/Oracle cards)")
    ap.add_argument("--batch-out", required=True, help="Output path for Weaviate batch JSON")
    ap.add_argument("--model", default="Alibaba-NLP/gte-modernbert-base", help="HF model name")
    ap.add_argument("--include-name", action="store_true", help="Include card name in embedding input")
    ap.add_argument("--limit", type=int, default=0, help="Limit number of cards for quick runs")
    ap.add_argument("--offset", type=int, default=0, help="Start index into the Scryfall list")
    ap.add_argument("--checkpoint", type=str, default="", help="Path to a progress JSON file to resume (stores next offset)")
    args = ap.parse_args()

    kind, model = load_model(args.model)
    try:
        from tqdm import tqdm  # type: ignore
    except Exception:
        def tqdm(x, **kwargs):  # type: ignore
            return x
    # Suppress progress bars in quiet mode
    if _is_quiet():
        def tqdm(x, **kwargs):  # type: ignore
            return x

    # Load Scryfall bulk JSON (list of card dicts)
    with open(args.scryfall_json, "r", encoding="utf-8") as f:
        cards = json.load(f)

    # Resolve offset via checkpoint if provided
    start_offset = args.offset
    if args.checkpoint:
        try:
            with open(args.checkpoint, "r", encoding="utf-8") as cf:
                state = json.load(cf)
            cp_off = int(state.get("next_offset", 0))
            if start_offset == 0 and cp_off > 0:
                start_offset = cp_off
        except FileNotFoundError:
            pass
        except Exception as e:
            print(f"WARN: failed to read checkpoint: {e}")

    objects = []
    texts = []
    idx_map = []  # (id, props)
    # Apply offset/limit window
    total_cards = len(cards)
    i = 0
    processed = 0
    for c in cards:
        if i < start_offset:
            i += 1
            continue
        cid = c.get("id")
        if not cid:
            continue
        props = extract_props(c)
        text = build_embed_text(c, args.include_name)
        texts.append(text)
        idx_map.append((cid, props))
        processed += 1
        i += 1
        if args.limit and processed >= args.limit:
            break

    # Batch encode
    batch_size = 32 if kind == "hf" else 64
    vectors: List[List[float]] = []
    for i in tqdm(range(0, len(texts), batch_size), desc="Embedding"):
        batch = texts[i:i+batch_size]
        if kind == "st":
            embs = model.encode(batch, batch_size=len(batch), normalize_embeddings=False, convert_to_numpy=True)
            for row in embs:
                vec = [float(x) for x in row.tolist()]
                vec = l2_normalize(vec)
                vectors.append(vec)
        else:
            embs = model.encode(batch, batch_size=len(batch))
            for row in embs:
                vec = [float(x) for x in list(row)]
                vec = l2_normalize(vec)
                vectors.append(vec)

    assert len(vectors) == len(idx_map)

    for (cid, props), vec in zip(idx_map, vectors):
        # Remove None values from properties (Weaviate rejects nulls for numeric types)
        clean_props = {k: v for k, v in props.items() if v is not None}
        obj = {
            "class": "Card",
            "id": cid,
            "properties": clean_props,
            "vector": vec,
        }
        objects.append(obj)

    out = {"objects": objects}
    with open(args.batch_out, "w", encoding="utf-8") as f:
        json.dump(out, f)

    print(f"Wrote Weaviate batch with {len(objects)} objects to {args.batch_out}")

    # Update checkpoint with next_offset
    if args.checkpoint:
        next_offset = start_offset + len(objects)
        state = {
            "next_offset": next_offset,
            "total": total_cards,
            "last_batch_out": args.batch_out,
            "model": args.model,
            "include_name": bool(args.include_name),
            "kind": kind,
        }
        try:
            with open(args.checkpoint, "w", encoding="utf-8") as cf:
                json.dump(state, cf)
            print(f"Updated checkpoint {args.checkpoint} -> next_offset={next_offset}/{total_cards}")
        except Exception as e:
            print(f"WARN: failed to write checkpoint: {e}")


if __name__ == "__main__":
    main()
