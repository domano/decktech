#!/usr/bin/env python3
"""
Download Scryfall bulk data (oracle_cards or default_cards) without external tools.

Usage:
  python scripts/download_scryfall.py -k oracle_cards -o data/oracle-cards.json
"""

import argparse
import json
import sys
from urllib.request import urlopen


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("-k", "--kind", default="oracle_cards", choices=["oracle_cards", "default_cards"], help="Bulk type")
    ap.add_argument("-o", "--out", required=True, help="Output path")
    args = ap.parse_args()

    idx_url = "https://api.scryfall.com/bulk-data"
    with urlopen(idx_url) as r:
        idx = json.load(r)
    url = None
    for item in idx.get("data", []):
        if item.get("type") == args.kind:
            url = item.get("download_uri")
            break
    if not url:
        print(f"Could not find download_uri for kind={args.kind}", file=sys.stderr)
        sys.exit(1)
    with urlopen(url) as r:
        data = r.read()
    import os
    os.makedirs(os.path.dirname(args.out) or ".", exist_ok=True)
    with open(args.out, "wb") as f:
        f.write(data)
    print(f"Saved {args.kind} to {args.out}")


if __name__ == "__main__":
    main()

