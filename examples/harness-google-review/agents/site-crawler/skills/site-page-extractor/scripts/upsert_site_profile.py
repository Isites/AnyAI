#!/usr/bin/env python3
"""Upsert one extracted page into a harness-google-review site profile."""

from __future__ import annotations

import argparse
import json
import os
import re
import sys
import time
from collections import Counter
from pathlib import Path
from urllib.parse import urlparse


def parse_args() -> argparse.Namespace:
    parser = argparse.ArgumentParser(description="Upsert a crawled page into 02-site-profile.json")
    parser.add_argument("--profile", required=True, help="Absolute or relative path to 02-site-profile.json")
    parser.add_argument("--site-url", required=True, help="Canonical site URL, for example https://www.ai-tol.top/")
    parser.add_argument("--page-json", help="Extracted page JSON object or MCP output containing one JSON object")
    parser.add_argument("--page-json-file", help="File containing extracted page JSON or MCP output")
    parser.add_argument("--failed-url", help="URL to record as a failed crawl")
    parser.add_argument("--error", help="Failure message for --failed-url")
    parser.add_argument("--status", choices=["partial", "completed"], default="partial")
    parser.add_argument("--note", action="append", default=[], help="Discovery note to append")
    parser.add_argument("--max-pages", type=int, default=200)
    return parser.parse_args()


def utc_now() -> str:
    return time.strftime("%Y-%m-%dT%H:%M:%SZ", time.gmtime())


def read_text(path: str) -> str:
    with open(path, "r", encoding="utf-8") as handle:
        return handle.read()


def extract_json_object(raw: str) -> dict:
    text = raw.strip()
    if not text:
        raise ValueError("page JSON is empty")
    if text.startswith("```"):
        text = re.sub(r"^```(?:json)?\s*", "", text, flags=re.IGNORECASE)
        text = re.sub(r"\s*```$", "", text)
    if "```json" in text:
        match = re.search(r"```json\s*(\{.*?\})\s*```", text, flags=re.IGNORECASE | re.DOTALL)
        if match:
            text = match.group(1)
    if not text.lstrip().startswith("{"):
        start = text.find("{")
        end = text.rfind("}")
        if start >= 0 and end > start:
            text = text[start : end + 1]
    data = json.loads(text)
    if not isinstance(data, dict):
        raise ValueError("page JSON must decode to an object")
    return data


def normalize_page(page: dict, site_url: str) -> dict:
    page = dict(page)
    full_url = str(page.get("full_url") or "").strip()
    rel_url = str(page.get("url") or "").strip()
    if not full_url and rel_url:
        full_url = site_url.rstrip("/") + "/" + rel_url.lstrip("/")
    if full_url:
        parsed = urlparse(full_url)
        full_url = parsed._replace(fragment="").geturl()
        page["full_url"] = full_url
        if not rel_url:
            path = parsed.path or "/"
            if parsed.query:
                path += "?" + parsed.query
            page["url"] = path
    if "extracted_at" not in page:
        page["extracted_at"] = utc_now()
    if "status_code" not in page:
        page["status_code"] = 200
    if not page.get("type"):
        page["type"] = "other"
    return page


def empty_profile(site_url: str) -> dict:
    parsed = urlparse(site_url)
    return {
        "crawled_at": utc_now(),
        "crawl_metadata": {
            "status": "partial",
            "started_at": utc_now(),
            "completed_at": None,
            "total_urls_attempted": 0,
            "successful_crawls": 0,
            "failed_crawls": 0,
            "failed_urls": [],
            "checkpoint_writes": 0,
            "discovery_notes": [],
        },
        "sites": [
            {
                "url": site_url,
                "domain": parsed.netloc,
                "robots_txt": {},
                "sitemap_xml": {},
                "pages": [],
                "statistics": {},
            }
        ],
        "cross_site_comparison": {"enabled": False, "similar_pages": []},
    }


def load_profile(path: Path, site_url: str) -> dict:
    if path.exists() and path.stat().st_size > 0:
        with path.open("r", encoding="utf-8") as handle:
            profile = json.load(handle)
        if isinstance(profile, dict):
            profile.setdefault("crawl_metadata", {})
            profile.setdefault("sites", [])
            if not profile["sites"]:
                profile["sites"].append(empty_profile(site_url)["sites"][0])
            profile.setdefault("cross_site_comparison", {"enabled": False, "similar_pages": []})
            return profile
    return empty_profile(site_url)


def page_key(page: dict) -> str:
    key = str(page.get("full_url") or page.get("url") or "").strip()
    if "#" in key:
        key = key.split("#", 1)[0]
    return key


def recompute_statistics(profile: dict) -> None:
    site = profile["sites"][0]
    pages = site.setdefault("pages", [])
    failed_urls = profile["crawl_metadata"].get("failed_urls", [])
    type_counts = Counter(str(page.get("type") or "other") for page in pages)
    core_pages = type_counts.get("core", 0) + type_counts.get("tool", 0)
    status_counts = Counter(str(page.get("status_code") or "unknown") for page in pages)
    site["statistics"] = {
        "total_pages": len(pages),
        "page_types": dict(sorted(type_counts.items())),
        "status_codes": dict(sorted(status_counts.items())),
        "core_pages": core_pages,
        "tool_pages": core_pages,
        "content_pages": type_counts.get("content", 0),
        "trust_pages": type_counts.get("trust", 0),
        "thin_pages": type_counts.get("thin", 0),
        "error_pages": type_counts.get("error", 0),
        "trust_pages_found": type_counts.get("trust", 0) > 0,
        "pages_with_ads": sum(1 for page in pages if page.get("has_ads") is True),
        "internal_links_discovered": len(
            {
                link.get("url") or link.get("path")
                for page in pages
                for link in page.get("internal_links", [])
                if isinstance(link, dict) and (link.get("url") or link.get("path"))
            }
        ),
        "failed_urls": len(failed_urls),
    }


def atomic_write_json(path: Path, data: dict) -> None:
    path.parent.mkdir(parents=True, exist_ok=True)
    tmp = path.with_suffix(path.suffix + ".tmp")
    with tmp.open("w", encoding="utf-8") as handle:
        json.dump(data, handle, ensure_ascii=False, indent=2)
        handle.write("\n")
    os.replace(tmp, path)


def main() -> int:
    args = parse_args()
    profile_path = Path(args.profile)
    profile = load_profile(profile_path, args.site_url)
    meta = profile.setdefault("crawl_metadata", {})
    site = profile["sites"][0]
    pages = site.setdefault("pages", [])

    changed = False
    if args.page_json or args.page_json_file:
        raw = args.page_json if args.page_json is not None else read_text(args.page_json_file)
        page = normalize_page(extract_json_object(raw), args.site_url)
        key = page_key(page)
        if not key:
            raise ValueError("page JSON must include full_url or url")
        pages[:] = [existing for existing in pages if page_key(existing) != key]
        pages.append(page)
        if args.max_pages > 0 and len(pages) > args.max_pages:
            del pages[args.max_pages :]
        changed = True

    if args.failed_url:
        failed = {
            "url": args.failed_url,
            "error": args.error or "crawl failed",
            "recorded_at": utc_now(),
        }
        failed_urls = meta.setdefault("failed_urls", [])
        failed_urls[:] = [item for item in failed_urls if item.get("url") != args.failed_url]
        failed_urls.append(failed)
        changed = True

    notes = meta.setdefault("discovery_notes", [])
    for note in args.note:
        note = note.strip()
        if note and note not in notes:
            notes.append(note)
            changed = True

    meta["status"] = args.status
    if args.status == "completed":
        meta["completed_at"] = utc_now()
    else:
        meta.setdefault("completed_at", None)
    meta["total_urls_attempted"] = len(pages) + len(meta.get("failed_urls", []))
    meta["successful_crawls"] = sum(1 for page in pages if int(page.get("status_code") or 200) < 400)
    meta["failed_crawls"] = len(meta.get("failed_urls", []))
    meta["checkpoint_writes"] = int(meta.get("checkpoint_writes", 0)) + 1
    profile["crawled_at"] = utc_now()

    recompute_statistics(profile)
    atomic_write_json(profile_path, profile)

    print(
        json.dumps(
            {
                "profile": str(profile_path),
                "status": meta["status"],
                "pages": len(pages),
                "failed_urls": len(meta.get("failed_urls", [])),
                "checkpoint_writes": meta["checkpoint_writes"],
                "bytes": profile_path.stat().st_size,
                "changed": changed,
            },
            ensure_ascii=False,
        )
    )
    return 0


if __name__ == "__main__":
    try:
        raise SystemExit(main())
    except Exception as exc:
        print(json.dumps({"error": str(exc)}, ensure_ascii=False), file=sys.stderr)
        raise
