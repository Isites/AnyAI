---
name: site-page-extractor
description: Private skill for site-crawler. Use it to run a scripted browser crawl that supports JS-rendered pages and writes compact checkpointed JSON without page-by-page model control.
tags:
  - crawler
  - extraction
  - chrome-devtools
  - checkpoint
---

# Site Page Extractor

Use this skill whenever `site-crawler` needs to crawl a site. The default path is a single scripted crawl, not page-by-page model control. The script launches real Chrome/Chromium through the Chrome DevTools Protocol, waits for JS-rendered DOM content, extracts compact page JSON in the browser context, and checkpoints every page.

## Workflow

1. Read the intake brief and resolve the concrete `artifact_root`.
2. Call `scripts/crawl_site_profile.py` once with `python`.
3. The script discovers sitemap/robots/rendered internal links, applies weighted random URL sampling across page buckets, drives Chrome in a batch, waits for rendered DOM stability, scrolls once to trigger lazy content, extracts compact page data, and writes checkpoint updates through `scripts/upsert_site_profile.py`.
4. Inspect the script's JSON stdout. If `"status":"completed"`, return the completed artifact summary. If it returns `"partial"` or exits non-zero, report the exact status/error and do not invent a completed profile.
5. Do not drive `mcp__chrome_devtools__navigate_page` and `mcp__chrome_devtools__evaluate_script` page by page unless the batch script itself is unavailable or reports a Chrome startup problem that can only be diagnosed interactively.

## Batch Crawler

Call the bundled crawler:

```json
{
  "file": "/opt/repos/projects/anyai/examples/harness-google-review/agents/site-crawler/skills/site-page-extractor/scripts/crawl_site_profile.py",
  "args": [
    "--brief", "/opt/repos/projects/anyai/examples/harness-google-review/artifacts-4/01-site-brief.md",
    "--profile", "/opt/repos/projects/anyai/examples/harness-google-review/artifacts-4/02-site-profile.json",
    "--min-pages", "50",
    "--max-pages", "80"
  ],
  "timeout": 600
}
```

Rules:

- Replace `--brief` and `--profile` with this run's actual absolute paths.
- The script reads `primary_url` from the brief unless `--site-url` is explicitly supplied.
- The script resumes from an existing valid `02-site-profile.json` and skips already completed page URLs.
- For pure JS pages, the script waits for `document.readyState`, body/root text stability, loading/skeleton text disappearance, and then scrolls once before extracting. Thin pages are retried after an additional render wait.
- By default, URL selection is weighted-random to vary long-tail coverage across review runs while still prioritizing home/trust/core/help/content buckets.
- For reproducible diagnosis, pass `--seed` with a previous `crawl_metadata.crawl_strategy.seed`; for deterministic tests only, pass `--deterministic`.
- The script writes the sampling mode, seed, bucket weights/minimums, crawled bucket counts, remaining queue buckets, and a crawled URL order sample to `crawl_metadata.crawl_strategy`.
- The script returns exit code `0` for completed, `2` for partial below the target, and `1` for fatal failure.
- If Chrome is not discoverable, set `CHROME_PATH` or pass `--chrome-path`; do not fall back to static HTTP-only crawling.

## Checkpoint Writer

The batch crawler uses the bundled upsert script internally. If you must record one page manually during diagnosis, use this same writer instead of writing custom Python in the model response. The script lives next to this skill:

`examples/harness-google-review/agents/site-crawler/skills/site-page-extractor/scripts/upsert_site_profile.py`

Manual single-page upsert shape:

```json
{
  "file": "/opt/repos/projects/anyai/examples/harness-google-review/agents/site-crawler/skills/site-page-extractor/scripts/upsert_site_profile.py",
  "args": [
    "--profile", "/opt/repos/projects/anyai/examples/harness-google-review/artifacts-4/02-site-profile.json",
    "--site-url", "https://www.ai-tol.top/",
    "--page-json", "{\"url\":\"/\",\"full_url\":\"https://www.example.com/\",\"type\":\"core\",\"status_code\":200}",
    "--status", "partial"
  ],
  "timeout": 30
}
```

Rules:

- Replace `--profile` and `--site-url` with this run's actual values.
- Pass only the compact JSON object returned by `evaluate_script` as `--page-json`.
- For failed pages, use `--failed-url <url> --error <message> --status partial`.
- At the end of the crawl, the batch crawler runs the writer with `--status completed` only after the site-crawler target is satisfied. The default target is 50+ successful same-site pages. If fewer than 50 pages are truly discoverable, it writes notes explaining discovered URL count, crawled count, skipped URLs, and why 50 pages could not be reached.
- After each script call, verify the output JSON includes `"pages"` and `"bytes"` greater than zero.
- Do not hand-write a long inline Python script for profile upserts.

## Evaluate Script

```javascript
() => {
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

  const images = Array.from(document.querySelectorAll("img")).slice(0, 40).map((img) => ({
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
  else if (/privacy|terms|contact|about|cookie|policy/.test(path)) type = "trust";
  else if (/blog|guide|docs|article|learn|tutorial/.test(path)) type = "content";
  else if (/product|service|feature|solution|app|tool|generator|animation|business|creative|data|education|technology|health|social|misc/.test(path) || document.querySelector("textarea,input,button,select")) type = "core";
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
    internal_links: uniqueByUrl(internalLinks, 120),
    external_links: uniqueByUrl(externalLinks, 40),
    images,
    extracted_at: new Date().toISOString()
  };
}
```

## Accumulator Rules

- Normalize page identity by `full_url` without hash fragments.
- Keep at most 200 pages.
- Keep `content_sample` short; never store full HTML or full page text.
- Preserve failed pages as objects with `full_url`, `status_code`, `type: "error"`, and `error`.
- For each site, compute `statistics` from accumulated page objects, not from memory.
