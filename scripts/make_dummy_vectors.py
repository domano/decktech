#!/usr/bin/env python3
"""
Create a Weaviate batch with placeholder vectors (for infra smoke tests).

Usage:
  python scripts/make_dummy_vectors.py \
    --scryfall-json data/oracle-cards.json \
    --batch-out data/weaviate_batch_dummy.json \
    --limit 500 --dim 768
"""

import argparse
import hashlib
import json
import math
import random
from typing import Any, Dict, List


def extract_props(card: Dict[str, Any]) -> Dict[str, Any]:
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

    oracle_text = card.get("oracle_text") or ""
    if not oracle_text:
        faces = card.get("card_faces") or []
        parts = [f.get("oracle_text") or "" for f in faces if f.get("oracle_text")]
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


def make_vec(seed: str, dim: int) -> List[float]:
    # Deterministic pseudo-random vector per card (normalized)
    h = hashlib.sha256(seed.encode("utf-8")).digest()
    rnd = random.Random(h)
    v = [rnd.uniform(-1.0, 1.0) for _ in range(dim)]
    n = math.sqrt(sum(x*x for x in v)) or 1.0
    return [x / n for x in v]


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("--scryfall-json", required=True)
    ap.add_argument("--batch-out", required=True)
    ap.add_argument("--limit", type=int, default=500)
    ap.add_argument("--dim", type=int, default=768)
    args = ap.parse_args()

    with open(args.scryfall_json, "r", encoding="utf-8") as f:
        cards = json.load(f)

    objs = []
    count = 0
    for c in cards:
        if not c.get("id"):
            continue
        props = extract_props(c)
        vec = make_vec(props.get("name", c["id"]) + props.get("type_line", ""), args.dim)
        clean_props = {k: v for k, v in props.items() if v is not None}
        objs.append({
            "class": "Card",
            "id": c["id"],
            "properties": clean_props,
            "vector": vec,
        })
        count += 1
        if args.limit and count >= args.limit:
            break

    out = {"objects": objs}
    with open(args.batch_out, "w", encoding="utf-8") as f:
        json.dump(out, f)
    print(f"Wrote dummy batch with {len(objs)} objects -> {args.batch_out}")


if __name__ == "__main__":
    main()

