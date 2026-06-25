#!/usr/bin/env python3
"""Scripted browser crawler for harness-google-review site profiles.

The site-crawler agent should call this once per crawl instead of driving Chrome
page-by-page from the model. The script launches a real Chromium/Chrome browser
through the Chrome DevTools Protocol, waits for JS-rendered pages to stabilize,
extracts compact page JSON, and checkpoints every page through
upsert_site_profile.py.
"""

from __future__ import annotations

import argparse
import base64
import hashlib
import json
import os
import random
import re
import shutil
import socket
import struct
import subprocess
import sys
import tempfile
import time
import urllib.error
import urllib.parse
import urllib.request
from pathlib import Path
from collections import Counter
from typing import Any, Optional


SCRIPT_DIR = Path(__file__).resolve().parent
DEFAULT_UPSERT = SCRIPT_DIR / "upsert_site_profile.py"

SKIP_EXTENSIONS = {
    ".7z",
    ".avi",
    ".bmp",
    ".css",
    ".csv",
    ".doc",
    ".docx",
    ".eot",
    ".gif",
    ".gz",
    ".ico",
    ".jpeg",
    ".jpg",
    ".js",
    ".json",
    ".mov",
    ".mp3",
    ".mp4",
    ".otf",
    ".pdf",
    ".png",
    ".rar",
    ".svg",
    ".tar",
    ".ttf",
    ".webm",
    ".webp",
    ".woff",
    ".woff2",
    ".xls",
    ".xlsx",
    ".zip",
}


CRAWL_BUCKET_ORDER = ["home", "trust", "core", "help_docs", "content", "other"]
CRAWL_BUCKET_WEIGHTS = {
    "home": 1,
    "trust": 5,
    "core": 30,
    "help_docs": 24,
    "content": 12,
    "other": 8,
}
CRAWL_BUCKET_MINIMUMS = {
    "trust": 4,
    "core": 12,
    "help_docs": 8,
    "content": 4,
}


EXTRACT_PAGE_JS = r"""
(() => {
  const absolute = (href) => {
    try {
      return new URL(href, location.href).href;
    } catch {
      return null;
    }
  };

  const text = (selector) => {
    const node = document.querySelector(selector);
    return node ? node.textContent.replace(/\s+/g, " ").trim() : "";
  };

  const attr = (selector, name) => {
    const node = document.querySelector(selector);
    return node ? node.getAttribute(name) || "" : "";
  };

  const bodyText = (document.body?.innerText || "").replace(/\s+/g, " ").trim();
  const anchors = Array.from(document.querySelectorAll("a[href]"));
  const internalLinks = [];
  const externalLinks = [];

  for (const anchor of anchors) {
    const href = absolute(anchor.getAttribute("href"));
    if (!href) continue;
    const url = new URL(href);
    const item = {
      url: href,
      path: url.pathname + url.search,
      text: anchor.textContent.replace(/\s+/g, " ").trim().slice(0, 120)
    };
    if (url.origin === location.origin) internalLinks.push(item);
    else externalLinks.push(item);
  }

  const uniqueByUrl = (items, limit) => {
    const seen = new Set();
    const out = [];
    for (const item of items) {
      if (seen.has(item.url)) continue;
      seen.add(item.url);
      out.push(item);
      if (out.length >= limit) break;
    }
    return out;
  };

  const images = Array.from(document.querySelectorAll("img")).slice(0, 30).map((img) => ({
    src: absolute(img.getAttribute("src")) || "",
    alt: (img.getAttribute("alt") || "").trim().slice(0, 160),
    width: img.naturalWidth || img.width || 0,
    height: img.naturalHeight || img.height || 0
  }));

  const headings = Array.from(document.querySelectorAll("h1,h2,h3")).slice(0, 40).map((node) => ({
    level: node.tagName.toLowerCase(),
    text: node.textContent.replace(/\s+/g, " ").trim().slice(0, 180)
  }));

  const statusText = document.title || text("h1") || location.pathname;
  const looks404 = /(^|\b)(404|not found|page not found)(\b|$)/i.test(statusText + " " + bodyText.slice(0, 500));
  const hasAds = Boolean(document.querySelector("[id*='ad'], [class*='ad'], ins.adsbygoogle, iframe[src*='ads']"));
  const noindex = /noindex/i.test(attr("meta[name='robots']", "content"));

  let type = "other";
  const path = location.pathname.toLowerCase();
  if (looks404) type = "error";
  else if (/privacy|terms|contact|about|cookie|policy|disclaimer/.test(path)) type = "trust";
  else if (/blog|guide|docs|article|learn|tutorial|resource|compare|versus|vs/.test(path)) type = "content";
  else if (/product|service|feature|solution|pricing|app|tool|generator|formatter|converter|editor|tester|calculator|compressor|optimizer|chart|cron|uuid|json|markdown|regex|timestamp|base64|password|qrcode|hash|jwt|gradient|color|image|pdf|diff|unit|random|css|svg|favicon|flowchart|plotter/.test(path) || document.querySelector("textarea,input,button,select")) type = "core";
  else if (bodyText.length < 500) type = "thin";

  return {
    url: location.pathname + location.search,
    full_url: location.href,
    type,
    status_code: looks404 ? 404 : 200,
    title: document.title || "",
    meta_description: attr("meta[name='description']", "content"),
    h1: text("h1"),
    headings,
    content_length: bodyText.length,
    content_sample: bodyText.slice(0, 600),
    has_ads: hasAds,
    canonical: attr("link[rel='canonical']", "href"),
    robots: attr("meta[name='robots']", "content") || (noindex ? "noindex" : null),
    internal_links: uniqueByUrl(internalLinks, 80),
    external_links: uniqueByUrl(externalLinks, 30),
    images,
    extracted_at: new Date().toISOString()
  };
})()
"""


RENDER_STATE_JS = r"""
(() => {
  const bodyText = (document.body?.innerText || "").replace(/\s+/g, " ").trim();
  const root = document.querySelector("main,#app,#__next,#root,[data-page-ready]") || document.body;
  const rootText = (root?.innerText || "").replace(/\s+/g, " ").trim();
  const loadingText = /(^|\b)(loading|please wait|spinner|skeleton|加载中|正在加载)(\b|$)/i.test(bodyText.slice(0, 800));
  return {
    readyState: document.readyState,
    title: document.title || "",
    bodyLength: bodyText.length,
    rootLength: rootText.length,
    linkCount: document.querySelectorAll("a[href]").length,
    loadingText,
    hasMainishRoot: Boolean(root)
  };
})()
"""


SCROLL_JS = r"""
(async () => {
  window.scrollTo(0, document.body ? document.body.scrollHeight : 0);
  await new Promise((resolve) => setTimeout(resolve, 250));
  window.scrollTo(0, 0);
  return true;
})()
"""


class CrawlError(Exception):
    """Crawler-level error."""


def parse_args() -> argparse.Namespace:
    parser = argparse.ArgumentParser(description="Batch crawl a JS-rendered site into 02-site-profile.json")
    parser.add_argument("--brief", required=True, help="Path to 01-site-brief.md")
    parser.add_argument("--profile", required=True, help="Path to 02-site-profile.json")
    parser.add_argument("--site-url", help="Override primary URL from the brief")
    parser.add_argument("--upsert-script", default=str(DEFAULT_UPSERT), help="Path to upsert_site_profile.py")
    parser.add_argument("--min-pages", type=int, default=50, help="Preferred minimum successful pages")
    parser.add_argument("--max-pages", type=int, default=80, help="Maximum pages to crawl in this run")
    parser.add_argument("--max-discovered", type=int, default=200, help="Maximum same-origin URLs to keep in the queue")
    parser.add_argument("--nav-timeout", type=float, default=20.0, help="Navigation timeout per page in seconds")
    parser.add_argument("--render-timeout", type=float, default=12.0, help="JS render-stability timeout per page in seconds")
    parser.add_argument("--fetch-timeout", type=float, default=8.0, help="robots/sitemap fetch timeout in seconds")
    parser.add_argument("--seed", help="Optional crawl sampling seed. Defaults to a time-based seed for varied review coverage.")
    parser.add_argument("--deterministic", action="store_true", help="Use deterministic URL priority without random sampling")
    parser.add_argument("--chrome-path", help="Chrome/Chromium executable path")
    parser.add_argument("--headed", action="store_true", help="Run Chrome with a visible window")
    return parser.parse_args()


def parse_primary_url(brief_path: Path) -> str:
    text = brief_path.read_text(encoding="utf-8")
    match = re.search(r"(?m)^\s*-\s*primary_url:\s*(\S+)\s*$", text)
    if not match:
        match = re.search(r"(?im)\bprimary_url\s*[:=]\s*(https?://\S+)", text)
    if not match:
        raise CrawlError(f"primary_url not found in {brief_path}")
    return match.group(1).strip().rstrip(".,")


def canonical_site_url(url: str) -> str:
    parsed = urllib.parse.urlparse(url)
    if parsed.scheme not in {"http", "https"} or not parsed.netloc:
        raise CrawlError(f"invalid site URL: {url}")
    path = parsed.path or "/"
    if not path.endswith("/") and "." not in Path(path).name:
        path += "/"
    return urllib.parse.urlunparse((parsed.scheme, parsed.netloc, path, "", parsed.query, ""))


def origin_tuple(url: str) -> tuple[str, str, int]:
    parsed = urllib.parse.urlparse(url)
    port = parsed.port or (443 if parsed.scheme == "https" else 80)
    return (parsed.scheme, (parsed.hostname or "").lower(), port)


def normalize_url(raw_url: str, base_url: str, site_origin: tuple[str, str, int]) -> Optional[str]:
    if not raw_url:
        return None
    raw_url = raw_url.strip()
    if raw_url.startswith(("mailto:", "tel:", "javascript:", "data:")):
        return None
    try:
        parsed = urllib.parse.urlparse(urllib.parse.urljoin(base_url, raw_url))
    except ValueError:
        return None
    if parsed.scheme not in {"http", "https"} or not parsed.netloc:
        return None
    if origin_tuple(parsed.geturl()) != site_origin:
        return None
    path = parsed.path or "/"
    suffix = Path(path.lower()).suffix
    if suffix in SKIP_EXTENSIONS:
        return None
    if parsed.query and len(parsed.query) > 160:
        return None
    normalized = parsed._replace(fragment="").geturl()
    return normalized


def crawl_bucket(url: str) -> str:
    path = urllib.parse.urlparse(url).path.lower()
    if path in {"", "/"}:
        return "home"
    if re.search(r"privacy|terms|contact|about|cookie|policy|disclaimer", path):
        return "trust"
    if re.search(r"help|docs|guide|learn|tutorial|faq|support", path):
        return "help_docs"
    if re.search(r"blog|article|compare|versus|vs|why|resource", path):
        return "content"
    if re.search(r"product|service|feature|solution|pricing|app|toolkit|tool|generator|formatter|converter|editor|tester|calculator|compressor|optimizer|chart|cron|uuid|json|markdown|regex|timestamp|base64|password|qrcode|hash|jwt|gradient|color|image|pdf|diff|unit|random|css|svg|favicon|flowchart|plotter", path):
        return "core"
    return "other"


def priority(url: str) -> tuple[int, int, str]:
    bucket = CRAWL_BUCKET_ORDER.index(crawl_bucket(url)) if crawl_bucket(url) in CRAWL_BUCKET_ORDER else len(CRAWL_BUCKET_ORDER)
    path = urllib.parse.urlparse(url).path.lower()
    return (bucket, len(path), path)


def make_seed(site_url: str, explicit_seed: Optional[str]) -> str:
    if explicit_seed:
        return explicit_seed
    today = time.strftime("%Y%m%d", time.gmtime())
    return f"{urllib.parse.urlparse(site_url).netloc}:{today}:{os.getpid()}:{time.time_ns()}"


def weighted_random_url(queue: list[str], rng: random.Random, crawled_bucket_counts: Counter[str]) -> str:
    if not queue:
        raise CrawlError("cannot select from an empty queue")
    if len(queue) == 1:
        return queue.pop(0)

    scored: list[tuple[str, float]] = []
    for url in queue:
        bucket = crawl_bucket(url)
        base_weight = CRAWL_BUCKET_WEIGHTS.get(bucket, CRAWL_BUCKET_WEIGHTS["other"])
        coverage_penalty = 1 + crawled_bucket_counts.get(bucket, 0)
        freshness = 0.65 + rng.random()
        scored.append((url, base_weight * freshness / coverage_penalty))
    total = sum(score for _, score in scored)
    pick = rng.random() * total
    running = 0.0
    chosen_index = 0
    for index, (_, score) in enumerate(scored):
        running += score
        if running >= pick:
            chosen_index = index
            break
    return queue.pop(chosen_index)


def pop_random_bucket_url(queue: list[str], bucket: str, rng: random.Random) -> Optional[str]:
    matching_indices = [index for index, url in enumerate(queue) if crawl_bucket(url) == bucket]
    if not matching_indices:
        return None
    return queue.pop(rng.choice(matching_indices))


def coverage_guard_url(queue: list[str], rng: random.Random, crawled_bucket_counts: Counter[str]) -> Optional[str]:
    for bucket in ("trust", "core", "help_docs", "content"):
        if crawled_bucket_counts.get(bucket, 0) >= CRAWL_BUCKET_MINIMUMS[bucket]:
            continue
        selected = pop_random_bucket_url(queue, bucket, rng)
        if selected:
            return selected
    return None


def order_seed_urls(raw_urls: list[str], base_url: str, site_origin: tuple[str, str, int], rng: random.Random, deterministic: bool) -> list[str]:
    normalized_urls: list[str] = []
    seen: set[str] = set()
    for raw_url in raw_urls:
        normalized = normalize_url(raw_url, base_url, site_origin)
        if not normalized or normalized in seen:
            continue
        seen.add(normalized)
        normalized_urls.append(normalized)
    if deterministic:
        return sorted(normalized_urls, key=priority)

    grouped: dict[str, list[str]] = {bucket: [] for bucket in CRAWL_BUCKET_ORDER}
    for url in normalized_urls:
        grouped.setdefault(crawl_bucket(url), []).append(url)
    for urls in grouped.values():
        rng.shuffle(urls)

    ordered: list[str] = []
    for bucket in ("home", "trust"):
        ordered.extend(grouped.get(bucket, []))
        grouped[bucket] = []

    emitted_counts: Counter[str] = Counter()
    while any(grouped.get(bucket) for bucket in CRAWL_BUCKET_ORDER):
        candidates = [bucket for bucket in CRAWL_BUCKET_ORDER if grouped.get(bucket)]
        weights = [
            CRAWL_BUCKET_WEIGHTS.get(bucket, CRAWL_BUCKET_WEIGHTS["other"]) / (1 + emitted_counts[bucket])
            for bucket in candidates
        ]
        total = sum(weights)
        pick = rng.random() * total
        running = 0.0
        selected = candidates[-1]
        for bucket, weight in zip(candidates, weights):
            running += weight
            if running >= pick:
                selected = bucket
                break
        ordered.append(grouped[selected].pop())
        emitted_counts[selected] += 1
    return ordered


def deterministic_url(queue: list[str]) -> str:
    queue.sort(key=priority)
    return queue.pop(0)


def bucket_summary(urls: set[str]) -> dict[str, int]:
    counts = Counter(crawl_bucket(url) for url in urls)
    return {bucket: counts.get(bucket, 0) for bucket in CRAWL_BUCKET_ORDER if counts.get(bucket, 0)}


def remaining_bucket_summary(urls: list[str]) -> dict[str, int]:
    counts = Counter(crawl_bucket(url) for url in urls)
    return {bucket: counts.get(bucket, 0) for bucket in CRAWL_BUCKET_ORDER if counts.get(bucket, 0)}


def read_existing_profile(profile_path: Path) -> tuple[set[str], list[str]]:
    if not profile_path.exists() or profile_path.stat().st_size == 0:
        return set(), []
    try:
        profile = json.loads(profile_path.read_text(encoding="utf-8"))
    except json.JSONDecodeError as exc:
        raise CrawlError(f"existing profile is invalid JSON: {profile_path}: {exc}") from exc
    completed: set[str] = set()
    discovered: list[str] = []
    for site in profile.get("sites", []):
        for page in site.get("pages", []):
            full_url = str(page.get("full_url") or page.get("url") or "").strip()
            if full_url:
                completed.add(full_url.split("#", 1)[0])
            for link in page.get("internal_links", []):
                if isinstance(link, dict):
                    link_url = str(link.get("url") or "").strip()
                    if link_url:
                        discovered.append(link_url)
    return completed, discovered


def http_get_text(url: str, timeout: float) -> tuple[int, str, str]:
    request = urllib.request.Request(
        url,
        headers={
            "User-Agent": "Mozilla/5.0 (compatible; AnyAI-GoogleReviewCrawler/1.0)",
            "Accept": "text/html,application/xhtml+xml,application/xml,text/plain;q=0.9,*/*;q=0.8",
        },
    )
    with urllib.request.urlopen(request, timeout=timeout) as response:
        raw = response.read(2_000_000)
        content_type = response.headers.get("content-type", "")
        charset = response.headers.get_content_charset() or "utf-8"
        return response.status, raw.decode(charset, errors="replace"), content_type


def discover_robots_and_sitemaps(site_url: str, timeout: float) -> tuple[dict[str, Any], dict[str, Any], list[str], list[str]]:
    parsed = urllib.parse.urlparse(site_url)
    root = f"{parsed.scheme}://{parsed.netloc}"
    robots_info: dict[str, Any] = {"url": root + "/robots.txt", "status_code": None, "sitemaps": [], "disallow": []}
    sitemap_info: dict[str, Any] = {"urls_checked": [], "urls": [], "errors": []}
    disallow: list[str] = []
    sitemap_candidates: list[str] = [root + "/sitemap.xml"]

    try:
        status, text, _ = http_get_text(robots_info["url"], timeout)
        robots_info["status_code"] = status
        robots_info["available"] = 200 <= status < 400
        robots_info["content_sample"] = text[:800]
        for line in text.splitlines():
            stripped = line.strip()
            if not stripped or stripped.startswith("#"):
                continue
            if stripped.lower().startswith("sitemap:"):
                value = stripped.split(":", 1)[1].strip()
                if value:
                    sitemap_candidates.append(value)
                    robots_info["sitemaps"].append(value)
            elif stripped.lower().startswith("disallow:"):
                value = stripped.split(":", 1)[1].strip()
                if value:
                    disallow.append(value)
        robots_info["disallow"] = disallow[:80]
    except Exception as exc:  # noqa: BLE001 - capture crawl evidence
        robots_info["available"] = False
        robots_info["error"] = str(exc)

    discovered: list[str] = []
    seen_sitemaps: set[str] = set()
    for sitemap_url in sitemap_candidates:
        if sitemap_url in seen_sitemaps:
            continue
        seen_sitemaps.add(sitemap_url)
        sitemap_info["urls_checked"].append(sitemap_url)
        try:
            status, text, _ = http_get_text(sitemap_url, timeout)
            if not (200 <= status < 400):
                sitemap_info["errors"].append({"url": sitemap_url, "status_code": status})
                continue
            locs = re.findall(r"<loc>\s*([^<]+?)\s*</loc>", text, flags=re.IGNORECASE)
            if locs and any(loc.lower().endswith(".xml") for loc in locs[:20]):
                for loc in locs[:50]:
                    if loc.lower().endswith(".xml") and loc not in seen_sitemaps:
                        sitemap_candidates.append(loc)
                continue
            discovered.extend(locs)
        except Exception as exc:  # noqa: BLE001 - capture crawl evidence
            sitemap_info["errors"].append({"url": sitemap_url, "error": str(exc)})
    sitemap_info["urls"] = discovered[:500]
    return robots_info, sitemap_info, discovered, disallow


def is_disallowed(url: str, disallow: list[str]) -> bool:
    path = urllib.parse.urlparse(url).path or "/"
    for rule in disallow:
        if rule == "/":
            return True
        if rule and path.startswith(rule):
            return True
    return False


def find_chrome(explicit: Optional[str]) -> str:
    candidates = []
    if explicit:
        candidates.append(explicit)
    env_path = os.environ.get("CHROME_PATH") or os.environ.get("CHROMIUM_PATH")
    if env_path:
        candidates.append(env_path)
    candidates.extend(
        [
            "google-chrome",
            "google-chrome-stable",
            "chromium",
            "chromium-browser",
            "chrome",
            "/Applications/Google Chrome.app/Contents/MacOS/Google Chrome",
            "/Applications/Chromium.app/Contents/MacOS/Chromium",
            "/Applications/Microsoft Edge.app/Contents/MacOS/Microsoft Edge",
            "/usr/bin/google-chrome",
            "/usr/bin/chromium",
            "/usr/bin/chromium-browser",
        ]
    )
    for candidate in candidates:
        if not candidate:
            continue
        path = shutil.which(candidate) if os.path.basename(candidate) == candidate else candidate
        if path and Path(path).exists():
            return path
    raise CrawlError("Chrome/Chromium executable not found; set CHROME_PATH or install Chrome")


def choose_port() -> int:
    with socket.socket(socket.AF_INET, socket.SOCK_STREAM) as sock:
        sock.bind(("127.0.0.1", 0))
        return int(sock.getsockname()[1])


def wait_json_endpoint(url: str, timeout: float) -> dict[str, Any]:
    deadline = time.time() + timeout
    last_error: Optional[Exception] = None
    while time.time() < deadline:
        try:
            with urllib.request.urlopen(url, timeout=1.5) as response:
                return json.loads(response.read().decode("utf-8"))
        except Exception as exc:  # noqa: BLE001
            last_error = exc
            time.sleep(0.2)
    raise CrawlError(f"Chrome DevTools endpoint did not become ready: {last_error}")


def request_json(url: str, method: str = "GET") -> dict[str, Any]:
    request = urllib.request.Request(url, method=method)
    with urllib.request.urlopen(request, timeout=5) as response:
        return json.loads(response.read().decode("utf-8"))


class WebSocket:
    def __init__(self, url: str, timeout: float = 30.0) -> None:
        parsed = urllib.parse.urlparse(url)
        if parsed.scheme != "ws":
            raise CrawlError(f"only ws:// CDP URLs are supported, got {url}")
        host = parsed.hostname or "127.0.0.1"
        port = parsed.port or 80
        path = parsed.path or "/"
        if parsed.query:
            path += "?" + parsed.query
        self.sock = socket.create_connection((host, port), timeout=timeout)
        self.sock.settimeout(timeout)
        key = base64.b64encode(os.urandom(16)).decode("ascii")
        request = (
            f"GET {path} HTTP/1.1\r\n"
            f"Host: {host}:{port}\r\n"
            "Upgrade: websocket\r\n"
            "Connection: Upgrade\r\n"
            f"Sec-WebSocket-Key: {key}\r\n"
            "Sec-WebSocket-Version: 13\r\n\r\n"
        )
        self.sock.sendall(request.encode("ascii"))
        response = self._read_http_response()
        if b" 101 " not in response.split(b"\r\n", 1)[0]:
            raise CrawlError(f"CDP websocket upgrade failed: {response[:200]!r}")
        accept = base64.b64encode(hashlib.sha1((key + "258EAFA5-E914-47DA-95CA-C5AB0DC85B11").encode("ascii")).digest()).decode("ascii")
        if accept.encode("ascii") not in response:
            raise CrawlError("CDP websocket upgrade returned an invalid accept key")

    def _read_http_response(self) -> bytes:
        chunks: list[bytes] = []
        while True:
            chunk = self.sock.recv(4096)
            if not chunk:
                break
            chunks.append(chunk)
            data = b"".join(chunks)
            if b"\r\n\r\n" in data:
                return data
        return b"".join(chunks)

    def close(self) -> None:
        try:
            self.sock.close()
        except OSError:
            pass

    def send_json(self, payload: dict[str, Any]) -> None:
        data = json.dumps(payload, separators=(",", ":")).encode("utf-8")
        self._send_frame(0x1, data)

    def _send_frame(self, opcode: int, data: bytes) -> None:
        first = 0x80 | opcode
        length = len(data)
        mask_key = os.urandom(4)
        if length < 126:
            header = struct.pack("!BB", first, 0x80 | length)
        elif length < 65536:
            header = struct.pack("!BBH", first, 0x80 | 126, length)
        else:
            header = struct.pack("!BBQ", first, 0x80 | 127, length)
        masked = bytes(byte ^ mask_key[index % 4] for index, byte in enumerate(data))
        self.sock.sendall(header + mask_key + masked)

    def recv_json(self, timeout: float) -> dict[str, Any]:
        self.sock.settimeout(timeout)
        fragments: list[bytes] = []
        while True:
            fin, opcode, data = self._recv_frame()
            if opcode == 0x8:
                raise CrawlError("CDP websocket closed")
            if opcode == 0x9:
                self._send_frame(0xA, data)
                continue
            if opcode == 0xA:
                continue
            if opcode in {0x1, 0x2, 0x0}:
                fragments.append(data)
                if fin:
                    raw = b"".join(fragments)
                    return json.loads(raw.decode("utf-8"))

    def _recv_exact(self, count: int) -> bytes:
        chunks: list[bytes] = []
        remaining = count
        while remaining > 0:
            chunk = self.sock.recv(remaining)
            if not chunk:
                raise CrawlError("CDP websocket ended unexpectedly")
            chunks.append(chunk)
            remaining -= len(chunk)
        return b"".join(chunks)

    def _recv_frame(self) -> tuple[bool, int, bytes]:
        header = self._recv_exact(2)
        first, second = header
        fin = bool(first & 0x80)
        opcode = first & 0x0F
        masked = bool(second & 0x80)
        length = second & 0x7F
        if length == 126:
            length = struct.unpack("!H", self._recv_exact(2))[0]
        elif length == 127:
            length = struct.unpack("!Q", self._recv_exact(8))[0]
        mask_key = self._recv_exact(4) if masked else b""
        data = self._recv_exact(length) if length else b""
        if masked:
            data = bytes(byte ^ mask_key[index % 4] for index, byte in enumerate(data))
        return fin, opcode, data


class CDPClient:
    def __init__(self, websocket_url: str) -> None:
        self.ws = WebSocket(websocket_url)
        self.next_id = 1

    def close(self) -> None:
        self.ws.close()

    def call(self, method: str, params: Optional[dict[str, Any]] = None, timeout: float = 30.0) -> dict[str, Any]:
        call_id = self.next_id
        self.next_id += 1
        self.ws.send_json({"id": call_id, "method": method, "params": params or {}})
        deadline = time.time() + timeout
        while True:
            remaining = max(0.1, deadline - time.time())
            if remaining <= 0:
                raise CrawlError(f"CDP call timed out: {method}")
            message = self.ws.recv_json(remaining)
            if message.get("id") != call_id:
                continue
            if "error" in message:
                raise CrawlError(f"CDP {method} failed: {message['error']}")
            return message.get("result", {})

    def evaluate(self, expression: str, timeout: float = 30.0) -> Any:
        result = self.call(
            "Runtime.evaluate",
            {
                "expression": expression,
                "awaitPromise": True,
                "returnByValue": True,
            },
            timeout=timeout,
        )
        remote = result.get("result", {})
        if "exceptionDetails" in result:
            raise CrawlError(f"page evaluation failed: {result['exceptionDetails']}")
        if "value" in remote:
            return remote["value"]
        return remote.get("description")


class ChromeProcess:
    def __init__(self, executable: str, headed: bool) -> None:
        self.port = choose_port()
        self.user_data_dir = tempfile.mkdtemp(prefix="anyai-google-review-chrome-")
        command = [
            executable,
            f"--remote-debugging-port={self.port}",
            f"--user-data-dir={self.user_data_dir}",
            "--disable-background-networking",
            "--disable-default-apps",
            "--disable-dev-shm-usage",
            "--disable-extensions",
            "--disable-gpu",
            "--disable-popup-blocking",
            "--hide-scrollbars",
            "--no-first-run",
            "--no-sandbox",
            "about:blank",
        ]
        if not headed:
            command.insert(1, "--headless=new")
        self.process = subprocess.Popen(command, stdout=subprocess.DEVNULL, stderr=subprocess.PIPE)
        wait_json_endpoint(f"http://127.0.0.1:{self.port}/json/version", timeout=15)

    def new_page_ws(self) -> str:
        endpoint = f"http://127.0.0.1:{self.port}/json/new?about:blank"
        try:
            target = request_json(endpoint, method="PUT")
        except urllib.error.HTTPError:
            target = request_json(endpoint, method="GET")
        websocket_url = target.get("webSocketDebuggerUrl")
        if not websocket_url:
            raise CrawlError(f"Chrome did not return a page websocket URL: {target}")
        return str(websocket_url)

    def close(self) -> None:
        if self.process.poll() is None:
            self.process.terminate()
            try:
                self.process.wait(timeout=5)
            except subprocess.TimeoutExpired:
                self.process.kill()
        shutil.rmtree(self.user_data_dir, ignore_errors=True)


def wait_render_stable(cdp: CDPClient, timeout: float) -> dict[str, Any]:
    deadline = time.time() + timeout
    last_signature: Optional[tuple[Any, ...]] = None
    stable_count = 0
    scrolled = False
    last_state: dict[str, Any] = {}
    while time.time() < deadline:
        state = cdp.evaluate(RENDER_STATE_JS, timeout=5)
        if isinstance(state, dict):
            last_state = state
        elapsed_ratio = 1 - max(0, deadline - time.time()) / max(timeout, 0.1)
        if not scrolled and elapsed_ratio > 0.35:
            try:
                cdp.evaluate(SCROLL_JS, timeout=5)
            except Exception:
                pass
            scrolled = True
        signature = (
            state.get("readyState"),
            state.get("bodyLength"),
            state.get("rootLength"),
            state.get("linkCount"),
            state.get("loadingText"),
        )
        enough_text = int(state.get("bodyLength") or 0) >= 300
        not_loading = not bool(state.get("loadingText"))
        ready = state.get("readyState") in {"interactive", "complete"}
        if signature == last_signature:
            stable_count += 1
        else:
            stable_count = 0
            last_signature = signature
        if ready and not_loading and stable_count >= 2 and (enough_text or elapsed_ratio > 0.75):
            return state
        time.sleep(0.45)
    return last_state


def wait_navigation_started(cdp: CDPClient, target_url: str, timeout: float) -> str:
    deadline = time.time() + timeout
    target_origin = origin_tuple(target_url)
    last_href = ""
    while time.time() < deadline:
        try:
            href = cdp.evaluate("location.href", timeout=3)
            if isinstance(href, str):
                last_href = href
                if href != "about:blank" and origin_tuple(href) == target_origin:
                    return href
        except Exception:
            pass
        time.sleep(0.2)
    raise CrawlError(f"navigation did not reach target origin: target={target_url} last_href={last_href}")


def navigate_and_extract(cdp: CDPClient, url: str, nav_timeout: float, render_timeout: float) -> dict[str, Any]:
    result = cdp.call("Page.navigate", {"url": url}, timeout=nav_timeout)
    if result.get("errorText"):
        raise CrawlError(str(result["errorText"]))
    wait_navigation_started(cdp, url, min(8.0, nav_timeout))
    wait_render_stable(cdp, render_timeout)
    page = cdp.evaluate(EXTRACT_PAGE_JS, timeout=10)
    if not isinstance(page, dict):
        raise CrawlError("extractor returned a non-object result")
    if int(page.get("content_length") or 0) < 100:
        wait_render_stable(cdp, max(2.0, render_timeout / 2))
        page = cdp.evaluate(EXTRACT_PAGE_JS, timeout=10)
        if not isinstance(page, dict):
            raise CrawlError("extractor returned a non-object result after retry")
    return page


def run_upsert(
    upsert_script: Path,
    profile: Path,
    site_url: str,
    status: str,
    page: Optional[dict[str, Any]] = None,
    failed_url: Optional[str] = None,
    error: Optional[str] = None,
    notes: Optional[list[str]] = None,
) -> dict[str, Any]:
    command = [
        sys.executable,
        str(upsert_script),
        "--profile",
        str(profile),
        "--site-url",
        site_url,
        "--status",
        status,
    ]
    if page is not None:
        command.extend(["--page-json", json.dumps(page, ensure_ascii=False, separators=(",", ":"))])
    if failed_url:
        command.extend(["--failed-url", failed_url, "--error", error or "crawl failed"])
    for note in notes or []:
        command.extend(["--note", note])
    result = subprocess.run(command, check=False, text=True, stdout=subprocess.PIPE, stderr=subprocess.PIPE)
    if result.returncode != 0:
        raise CrawlError(f"upsert failed for {page.get('full_url') if page else failed_url}: {result.stderr.strip()}")
    return json.loads(result.stdout.strip())


def atomic_write_json(path: Path, data: dict[str, Any]) -> None:
    tmp = path.with_suffix(path.suffix + ".tmp")
    with tmp.open("w", encoding="utf-8") as handle:
        json.dump(data, handle, ensure_ascii=False, indent=2)
        handle.write("\n")
    os.replace(tmp, path)


def annotate_profile(
    profile_path: Path,
    robots_info: dict[str, Any],
    sitemap_info: dict[str, Any],
    discovered: set[str],
    skipped: list[dict[str, str]],
    sampling: dict[str, Any],
) -> None:
    profile = json.loads(profile_path.read_text(encoding="utf-8"))
    site = profile.setdefault("sites", [{}])[0]
    site["robots_txt"] = robots_info
    site["sitemap_xml"] = {
        **sitemap_info,
        "same_origin_url_count": len([url for url in sitemap_info.get("urls", []) if isinstance(url, str)]),
    }
    meta = profile.setdefault("crawl_metadata", {})
    meta["discovered_url_count"] = len(discovered)
    meta["discovered_url_buckets"] = bucket_summary(discovered)
    meta["skipped_urls"] = skipped[:80]
    meta["crawl_strategy"] = sampling
    atomic_write_json(profile_path, profile)


def crawl(args: argparse.Namespace) -> dict[str, Any]:
    brief_path = Path(args.brief).resolve()
    profile_path = Path(args.profile).resolve()
    upsert_script = Path(args.upsert_script).resolve()
    if not brief_path.exists():
        raise CrawlError(f"brief file not found: {brief_path}")
    if not upsert_script.exists():
        raise CrawlError(f"upsert script not found: {upsert_script}")

    site_url = canonical_site_url(args.site_url or parse_primary_url(brief_path))
    site_origin = origin_tuple(site_url)
    seed = make_seed(site_url, args.seed)
    rng = random.Random(seed)
    completed_urls, recovered_urls = read_existing_profile(profile_path)
    robots_info, sitemap_info, sitemap_urls, disallow = discover_robots_and_sitemaps(site_url, args.fetch_timeout)

    discovered: set[str] = set()
    queue: list[str] = []
    skipped: list[dict[str, str]] = []
    crawled_bucket_counts: Counter[str] = Counter()
    crawled_order_sample: list[str] = []

    def add_url(raw_url: str, base_url: str, source: str, append_to_queue: bool = True) -> None:
        if len(discovered) >= args.max_discovered:
            return
        normalized = normalize_url(raw_url, base_url, site_origin)
        if not normalized:
            return
        if is_disallowed(normalized, disallow):
            skipped.append({"url": normalized, "reason": "robots_disallow", "source": source})
            return
        if normalized not in discovered:
            discovered.add(normalized)
            if append_to_queue and normalized not in completed_urls:
                queue.append(normalized)

    add_url(site_url, site_url, "brief")
    for raw in order_seed_urls(sitemap_urls, site_url, site_origin, rng, args.deterministic):
        add_url(raw, site_url, "sitemap")
    for raw in recovered_urls:
        add_url(raw, site_url, "existing_profile")

    chrome = ChromeProcess(find_chrome(args.chrome_path), headed=args.headed)
    cdp = CDPClient(chrome.new_page_ws())
    successful = len(completed_urls)
    failed = 0

    try:
        cdp.call("Page.enable", timeout=5)
        cdp.call("Runtime.enable", timeout=5)
        while queue and successful < args.max_pages:
            if site_url in queue:
                queue.remove(site_url)
                url = site_url
            elif args.deterministic:
                url = deterministic_url(queue)
            else:
                url = coverage_guard_url(queue, rng, crawled_bucket_counts) or weighted_random_url(queue, rng, crawled_bucket_counts)
            if url in completed_urls:
                continue
            try:
                page = navigate_and_extract(cdp, url, args.nav_timeout, args.render_timeout)
                completed_urls.add(str(page.get("full_url") or url).split("#", 1)[0])
                successful += 1
                crawled_bucket_counts[crawl_bucket(str(page.get("full_url") or url))] += 1
                if len(crawled_order_sample) < 40:
                    crawled_order_sample.append(str(page.get("full_url") or url))
                run_upsert(upsert_script, profile_path, site_url, "partial", page=page)
                for link in page.get("internal_links", []):
                    if isinstance(link, dict):
                        add_url(str(link.get("url") or link.get("path") or ""), page.get("full_url") or url, "page_link")
            except Exception as exc:  # noqa: BLE001 - preserve failed page evidence
                failed += 1
                completed_urls.add(url)
                crawled_bucket_counts[crawl_bucket(url)] += 1
                run_upsert(upsert_script, profile_path, site_url, "partial", failed_url=url, error=str(exc))
    finally:
        cdp.close()
        chrome.close()

    discovered_count = len(discovered)
    success_pages = max(0, successful)
    if success_pages == 0:
        raise CrawlError("no pages were successfully crawled")

    notes = [
        (
            "scripted browser crawl: "
            f"discovered {discovered_count} same-origin URLs; "
            f"successful pages {success_pages}; failed pages {failed}; "
            f"min target {args.min_pages}; max target {args.max_pages}; "
            f"sampling {'deterministic priority' if args.deterministic else 'weighted random'}."
        )
    ]
    if not args.deterministic:
        notes.append(f"randomized review sampling seed: {seed}")
    if success_pages < args.min_pages:
        if not queue and discovered_count < args.min_pages:
            notes.append(
                f"completed below min target because only {discovered_count} crawlable same-origin URLs were discovered after sitemap, robots, existing profile, and rendered link discovery."
            )
            final_status = "completed"
        else:
            notes.append(
                f"partial below min target: {len(queue)} URLs remain or failures prevented reaching {args.min_pages} successful pages."
            )
            final_status = "partial"
    else:
        final_status = "completed"

    final_upsert = run_upsert(upsert_script, profile_path, site_url, final_status, notes=notes)
    sampling = {
        "mode": "deterministic_priority" if args.deterministic else "weighted_random",
        "seed": seed,
        "bucket_weights": CRAWL_BUCKET_WEIGHTS,
        "bucket_minimums": CRAWL_BUCKET_MINIMUMS,
        "crawled_bucket_counts": dict(sorted(crawled_bucket_counts.items())),
        "remaining_queue_buckets": remaining_bucket_summary(queue),
        "crawled_order_sample": crawled_order_sample,
    }
    annotate_profile(profile_path, robots_info, sitemap_info, discovered, skipped, sampling)
    final_profile = json.loads(profile_path.read_text(encoding="utf-8"))
    return {
        "status": final_status,
        "profile": str(profile_path),
        "site_url": site_url,
        "pages": final_profile.get("sites", [{}])[0].get("statistics", {}).get("total_pages", final_upsert.get("pages")),
        "failed_urls": final_profile.get("crawl_metadata", {}).get("failed_crawls", final_upsert.get("failed_urls")),
        "discovered_url_count": discovered_count,
        "notes": notes,
    }


def main() -> int:
    args = parse_args()
    try:
        result = crawl(args)
        print(json.dumps(result, ensure_ascii=False, sort_keys=True))
        return 0 if result["status"] == "completed" else 2
    except Exception as exc:  # noqa: BLE001 - produce machine-readable tool output
        print(json.dumps({"status": "failed", "error": str(exc)}, ensure_ascii=False, sort_keys=True), file=sys.stderr)
        return 1


if __name__ == "__main__":
    raise SystemExit(main())
