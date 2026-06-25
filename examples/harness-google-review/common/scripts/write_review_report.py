#!/usr/bin/env python3
import argparse
import datetime as dt
import json
from pathlib import Path


def load_text(path):
    if not path:
        return ""
    p = Path(path)
    if not p.exists() or not p.is_file():
        return ""
    return p.read_text(encoding="utf-8", errors="replace")


def load_profile(path):
    with Path(path).open("r", encoding="utf-8") as f:
        return json.load(f)


def first_site(profile):
    sites = profile.get("sites") or []
    if not sites:
        return {}
    return sites[0] or {}


def pages(profile):
    return first_site(profile).get("pages") or []


def lang_for_url(url):
    path = "/" + (url.split("://", 1)[-1].split("/", 1)[-1] if "://" in url else url).lstrip("/")
    for lang in ("zh", "ja", "fr", "de"):
        if path.startswith(f"/{lang}/"):
            return lang
    return "en"


def page_type_counts(page_items):
    counts = {}
    for page in page_items:
        typ = page.get("type") or "unknown"
        counts[typ] = counts.get(typ, 0) + 1
    return counts


def lang_counts(page_items):
    counts = {}
    for page in page_items:
        lang = lang_for_url(page.get("full_url") or page.get("url") or "")
        counts[lang] = counts.get(lang, 0) + 1
    return counts


def thin_pages(page_items, threshold=200):
    out = []
    for page in page_items:
        length = int(page.get("content_length") or 0)
        if length < threshold:
            out.append((page.get("full_url") or page.get("url") or "unknown", length, page.get("type") or "unknown"))
    return out


def duplicate_values(page_items, field):
    seen = {}
    for page in page_items:
        value = (page.get(field) or "").strip()
        if not value:
            continue
        seen.setdefault(value, []).append(page.get("full_url") or page.get("url") or "unknown")
    return {k: v for k, v in seen.items() if len(v) > 1}


def sitemap_findings(profile):
    site = first_site(profile)
    robots = site.get("robots_txt") or {}
    sitemap = site.get("sitemap_xml") or {}
    findings = []
    for item in robots.get("sitemaps") or []:
        findings.append(f"- robots.txt sitemap: `{item}`")
    for err in sitemap.get("errors") or []:
        findings.append(f"- sitemap error: `{err.get('url', 'unknown')}` -> {err.get('error', 'unknown error')}")
    if not findings:
        findings.append("- No sitemap or robots blocking issue found in profile.")
    return findings


def sitemap_error_count(profile):
    site = first_site(profile)
    sitemap = site.get("sitemap_xml") or {}
    return len(sitemap.get("errors") or [])


def render_content_report(profile, input_paths):
    page_items = pages(profile)
    thin = thin_pages(page_items)
    dup_titles = duplicate_values(page_items, "title")
    counts = page_type_counts(page_items)
    now = dt.datetime.now(dt.timezone.utc).isoformat()
    lines = [
        "---",
        "agent: content-analyzer",
        f"timestamp: {now}",
        "input_files:",
        f"  - {input_paths['profile_label']}",
        "generation_mode: script_file",
        "---",
        "",
        "# Content Value Analysis",
        "",
        "## Summary",
        f"- status: {'fail' if thin else 'risky' if dup_titles else 'pass'}",
        f"- total_pages: {len(page_items)}",
        f"- page_types: {counts}",
        f"- thin_pages_under_200_chars: {len(thin)}",
        f"- duplicate_title_groups: {len(dup_titles)}",
        "",
        "## Thin Content Signals",
    ]
    if thin:
        lines += ["| page | content_length | type |", "|------|----------------|------|"]
        lines += [f"| {url} | {length} | {typ} |" for url, length, typ in thin[:80]]
    else:
        lines.append("- No pages below 200 visible characters in the crawled profile.")
    lines += ["", "## Template And Duplication Signals"]
    if dup_titles:
        for title, urls in list(dup_titles.items())[:40]:
            lines.append(f"- `{title}` appears on {len(urls)} pages: {', '.join(urls[:5])}")
    else:
        lines.append("- No duplicate title groups found in crawled pages.")
    lines += [
        "",
        "## Evidence Notes",
        "- This report is generated from the crawled profile to avoid streaming large Markdown through model tool-call JSON.",
        "- Browser sampling observations, if any, should be appended by the agent in small chunks.",
    ]
    return "\n".join(lines) + "\n"


def render_duplication_report(profile, input_paths):
    page_items = pages(profile)
    dup_titles = duplicate_values(page_items, "title")
    dup_desc = duplicate_values(page_items, "meta_description")
    counts = lang_counts(page_items)
    now = dt.datetime.now(dt.timezone.utc).isoformat()
    lines = [
        "---",
        "agent: duplication-auditor",
        f"timestamp: {now}",
        "input_files:",
        f"  - {input_paths['profile_label']}",
        f"  - {input_paths.get('content_label', 'artifacts-N/03-content-analysis.md')}",
        "generation_mode: script_file",
        "---",
        "",
        "# Duplication And Scaled Content Analysis",
        "",
        "## Summary",
        f"- status: {'risky' if dup_titles or dup_desc else 'pass'}",
        f"- total_pages: {len(page_items)}",
        f"- language_distribution: {counts}",
        f"- duplicate_title_groups: {len(dup_titles)}",
        f"- duplicate_meta_description_groups: {len(dup_desc)}",
        "",
        "## Duplicate Titles",
    ]
    if dup_titles:
        for title, urls in list(dup_titles.items())[:40]:
            lines.append(f"- `{title}` -> {len(urls)} pages: {', '.join(urls[:6])}")
    else:
        lines.append("- No duplicate title groups found.")
    lines += ["", "## Duplicate Meta Descriptions"]
    if dup_desc:
        for desc, urls in list(dup_desc.items())[:40]:
            lines.append(f"- `{desc[:120]}` -> {len(urls)} pages: {', '.join(urls[:6])}")
    else:
        lines.append("- No duplicate meta-description groups found.")
    lines += [
        "",
        "## Evidence Notes",
        "- This script reports deterministic profile-level duplication signals.",
        "- Browser pair checks, if performed, should be appended by the agent in small chunks.",
    ]
    return "\n".join(lines) + "\n"


def render_seo_report(profile, input_paths):
    site = first_site(profile)
    page_items = pages(profile)
    short_titles = []
    long_titles = []
    short_desc = []
    long_desc = []
    missing_h1 = []
    for page in page_items:
        url = page.get("full_url") or page.get("url") or "unknown"
        title = page.get("title") or ""
        desc = page.get("meta_description") or ""
        if title and len(title) < 30:
            short_titles.append((url, len(title), title))
        if len(title) > 70:
            long_titles.append((url, len(title), title))
        if desc and len(desc) < 50:
            short_desc.append((url, len(desc), desc))
        if len(desc) > 160:
            long_desc.append((url, len(desc), desc[:120]))
        if not (page.get("h1") or "").strip():
            missing_h1.append(url)
    now = dt.datetime.now(dt.timezone.utc).isoformat()
    lines = [
        "---",
        "agent: seo-analyzer",
        f"timestamp: {now}",
        "input_files:",
        f"  - {input_paths['profile_label']}",
        f"  - {input_paths.get('duplication_label', 'artifacts-N/04-duplication-analysis.md')}",
        "generation_mode: script_file",
        "---",
        "",
        "# Technical SEO Analysis",
        "",
        "## Summary",
        f"- status: {'risky' if sitemap_error_count(profile) or short_titles or long_desc else 'pass'}",
        f"- total_pages: {len(page_items)}",
        f"- short_titles: {len(short_titles)}",
        f"- long_titles: {len(long_titles)}",
        f"- short_descriptions: {len(short_desc)}",
        f"- long_descriptions: {len(long_desc)}",
        f"- missing_h1: {len(missing_h1)}",
        f"- robots_available: {bool((site.get('robots_txt') or {}).get('available'))}",
        "",
        "## robots.txt And Sitemap",
        *sitemap_findings(profile),
        "",
        "## Meta Issues",
    ]
    for label, rows in (("Short titles", short_titles), ("Long titles", long_titles), ("Short descriptions", short_desc), ("Long descriptions", long_desc)):
        lines.append(f"### {label}")
        if rows:
            lines += ["| page | length | sample |", "|------|--------|--------|"]
            lines += [f"| {url} | {length} | {sample.replace('|', '/')} |" for url, length, sample in rows[:80]]
        else:
            lines.append("- None found.")
        lines.append("")
    if missing_h1:
        lines += ["## Missing H1", *[f"- {url}" for url in missing_h1[:80]], ""]
    lines.append("## Evidence Notes")
    lines.append("- Browser verification observations, if any, should be appended by the agent in small chunks.")
    return "\n".join(lines) + "\n"


def render_final_report(profile, input_paths):
    page_items = pages(profile)
    thin = thin_pages(page_items)
    dup_titles = duplicate_values(page_items, "title")
    now = dt.datetime.now(dt.timezone.utc).isoformat()
    analyzer_notes = []
    for key in ("content", "duplication", "seo", "ux", "policy", "ad_inventory", "rejection"):
        text = load_text(input_paths.get(key, ""))
        if text:
            first_heading = next((line.strip("# ").strip() for line in text.splitlines() if line.startswith("#")), key)
            analyzer_notes.append(f"- {key}: {first_heading}")
    has_blockers = bool(thin)
    lines = [
        "---",
        "agent: report-generator",
        f"timestamp: {now}",
        "generation_mode: script_file",
        "---",
        "",
        "# Google AdSense Review Report",
        "",
        "## Executive Summary",
        f"- submit_ready: {str(not has_blockers).lower()}",
        f"- confidence: {'6' if has_blockers else '7'}",
        f"- total_pages_reviewed: {len(page_items)}",
        f"- thin_pages_under_200_chars: {len(thin)}",
        f"- duplicate_title_groups: {len(dup_titles)}",
        "",
        "## Issues by Priority",
    ]
    if thin:
        lines += ["### P0 Blockers", "| issue | pages | fix |", "|-------|-------|-----|"]
        lines.append(f"| Thin content pages under 200 visible characters | {len(thin)} pages | Expand useful, page-specific content or noindex/remove weak pages |")
    else:
        lines += ["### P0 Blockers", "- None detected from profile-level checks."]
    lines += ["", "### P1 High Priority"]
    if dup_titles:
        lines.append(f"- Duplicate title groups detected: {len(dup_titles)}. Differentiate page titles by topic and language.")
    else:
        lines.append("- No duplicate title group found in profile-level checks.")
    lines += ["", "## Sitemap And Robots Evidence", *sitemap_findings(profile), "", "## Analyzer Inputs"]
    lines += analyzer_notes or ["- No analyzer report text was available to summarize."]
    lines += [
        "",
        "## Data Sources",
        f"- profile: {input_paths['profile_label']}",
        "",
        "## Notes",
        "- This report was generated by a checked-in script so large Markdown was written directly to disk, not streamed through model tool-call JSON.",
        "- Treat script-level findings as deterministic baseline; expert analyzer reports provide the detailed evidence when present.",
    ]
    return "\n".join(lines) + "\n"


def main():
    parser = argparse.ArgumentParser(description="Write Google review analysis artifacts without large inline tool-call JSON.")
    parser.add_argument("--kind", required=True, choices=["content", "duplication", "seo", "final"])
    parser.add_argument("--profile", required=True)
    parser.add_argument("--output", required=True)
    parser.add_argument("--content-analysis")
    parser.add_argument("--duplication-analysis")
    parser.add_argument("--seo-analysis")
    parser.add_argument("--ux-analysis")
    parser.add_argument("--policy-analysis")
    parser.add_argument("--ad-inventory-analysis")
    parser.add_argument("--rejection-mapping")
    parser.add_argument("--profile-label", default="artifacts-N/02-site-profile.json")
    args = parser.parse_args()

    profile = load_profile(args.profile)
    labels = {
        "profile_label": args.profile_label,
        "content_label": "artifacts-N/03-content-analysis.md",
        "duplication_label": "artifacts-N/04-duplication-analysis.md",
        "content": args.content_analysis or "",
        "duplication": args.duplication_analysis or "",
        "seo": args.seo_analysis or "",
        "ux": args.ux_analysis or "",
        "policy": args.policy_analysis or "",
        "ad_inventory": args.ad_inventory_analysis or "",
        "rejection": args.rejection_mapping or "",
    }

    if args.kind == "content":
        body = render_content_report(profile, labels)
    elif args.kind == "duplication":
        body = render_duplication_report(profile, labels)
    elif args.kind == "seo":
        body = render_seo_report(profile, labels)
    else:
        body = render_final_report(profile, labels)

    output = Path(args.output)
    output.parent.mkdir(parents=True, exist_ok=True)
    output.write_text(body, encoding="utf-8")
    print(json.dumps({
        "status": "completed",
        "artifact_path_abs": str(output.resolve()),
        "artifact_bytes": output.stat().st_size,
        "verified": output.exists() and output.stat().st_size > 0,
    }, ensure_ascii=False))


if __name__ == "__main__":
    main()
