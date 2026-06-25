# Harness Google Review

Universal Google AdSense review workflow based on scripted browser crawling.

This harness crawls websites using a scripted real browser, then runs expert audits step by step. Each artifact gates the next step so later analysis cannot skip missing or failed earlier work.

## Features

- **Scripted browser crawling**: Uses real Chrome/Chromium through CDP so JS-rendered pages are extracted without page-by-page model control
- **Weighted random crawl sampling**: Varies long-tail page coverage across review runs so repeated audits can cover more pages and reduce missed Google review issues
- **Universal input**: Works with any website URL
- **Strict sequential gates**: Every numbered artifact must be produced before the next agent runs
- **File-based communication**: Agents communicate via artifacts, avoiding context explosion
- **Actionable output**: Prioritized remediation recommendations

## Agent Architecture

Total agents: 12.

- **Lead**: `review-lead` — Orchestrates the workflow
- **Intake**: `intake-triager` — Validates URL and generates brief
- **Crawler**: `site-crawler` — Crawls site with a scripted real browser and checkpointed profile writer
- **Analyzers**:
  - `content-analyzer` — Content value and originality
  - `duplication-auditor` — Internal and cross-site duplication
  - `seo-analyzer` — Technical SEO and crawlability, with Chrome DevTools MCP browser verification
  - `ux-analyzer` — Mobile experience and usability, using Chrome DevTools MCP
  - `policy-analyzer` — Privacy policy and trust pages, with Chrome DevTools MCP browser verification
  - `ad-inventory-analyzer` — Ad placement and inventory value, with Chrome DevTools MCP browser verification
- **Reporting**:
  - `rejection-mapper` — Maps findings to rejection types
  - `report-generator` — Creates final report
- **Optional (for fix phase)**:
  - `requirement-generator` — Generates detailed remediation requirements
  - `qa-verifier` — Verifies readiness after fixes

## Workflow

```text
review-lead
→ 01 intake-triager (URL validation, brief generation)
→ 02 site-crawler (scripted browser crawl)
→ 03 content-analyzer
→ 04 duplication-auditor
→ 05 seo-analyzer
→ 06 ux-analyzer
→ 07 policy-analyzer
→ 08 ad-inventory-analyzer
→ 09 rejection-mapper (maps to rejection types)
→ 10 report-generator (final report)
→ optional: requirement-generator / qa-verifier (fix phase)
```

If any step fails, `review-lead` rolls back to the previous step, retries that upstream producer, then retries the failed step. Later steps do not run until all earlier steps have completed successfully.

## Requirements

- Chrome/Chromium available on the machine, or `CHROME_PATH` set for the crawler script
- Chrome DevTools MCP installed for optional diagnostics: `common/mcps/chrome-devtools.yaml`
- Model provider configured in `anyai.yaml`

## Browser Evidence Scope

- `site-crawler` uses the batch crawler for speed and records `crawl_metadata.crawl_strategy` with sampling mode, seed, bucket minimums, and page bucket counts.
- The crawler's randomization exists to maximize page coverage across multiple review runs, not to make a single run arbitrary.
- Analysis agents should use Chrome DevTools MCP whenever browser-level evidence can improve fidelity.
- If an agent cannot review every crawled page in Chrome, it should randomly sample from the crawled page pool, include high-risk pages and random long-tail pages, and record the sample seed and uncovered scope.

## Run

CLI:

```bash
anyai chat --project ./examples/harness-google-review
```

Suggested prompts:

```text
# Single site review
请审核 https://example.com

# With known rejection reason
请审核 https://example.com，拒审原因是 low_value_content

# Two-site comparison
请审核 https://site1.com 和 https://site2.com

# With local source code
请审核 https://example.com，源码路径是 /path/to/src
```

Gateway / HTTP + SSE:

```bash
anyai start --project ./examples/harness-google-review
```

```bash
curl -N -X POST 'http://127.0.0.1:2333/api/chat?stream=1' \
  -H 'Content-Type: application/json' \
  -H 'Accept: text/event-stream' \
  -d '{
    "agent_id": "review-lead",
    "session_id": "my-review-session",
    "text": "请审核 https://example.com"
  }'
```

## Output Artifacts

Artifacts are stored in per-review directories such as `artifacts-1/`, `artifacts-2/`, etc. `artifacts-N/` in agent docs is a placeholder for the concrete directory chosen by `review-lead` for that website/review run:

- `01-site-brief.md` — Site brief from intake
- `02-site-profile.json` — Crawled site data
- `03-content-analysis.md` — Content value analysis
- `04-duplication-analysis.md` — Duplication analysis
- `05-seo-analysis.md` — Technical SEO analysis
- `06-ux-analysis.md` — UX/mobile analysis
- `07-policy-analysis.md` — Policy/trust analysis
- `08-ad-inventory-analysis.md` — Ad inventory analysis
- `09-rejection-mapping.md` — Rejection type mapping
- `10-final-report.md` — Final report with remediation

## Final Report Structure

```markdown
# Google AdSense Review Report

## Executive Summary
- submit_ready: true/false
- confidence: 1-10
- primary_rejection_type

## Issues by Priority
- P0 Blockers
- P1 High Priority
- P2 Medium Priority

## Detailed Findings
- Content Value
- Technical SEO
- UX & Mobile
- Trust & Policy
- Ad Inventory

## Remediation Plan
- Implementation order
- Detailed actions

## Re-submission Checklist
```

## Directory

```
harness-google-review/
├── anyai.yaml
├── agent.md (review-lead)
├── agents/
│   ├── intake-triager/
│   ├── site-crawler/
│   ├── content-analyzer/
│   ├── duplication-auditor/
│   ├── seo-analyzer/
│   ├── ux-analyzer/
│   ├── policy-analyzer/
│   ├── ad-inventory-analyzer/
│   ├── rejection-mapper/
│   ├── report-generator/
│   ├── requirement-generator/ (optional)
│   └── qa-verifier/ (optional)
└── common/
    └── mcps/
        └── chrome-devtools.yaml
```

## Runtime Observability

```bash
curl -s http://127.0.0.1:2333/api/tasks | jq
curl -s http://127.0.0.1:2333/api/runs | jq
curl -s http://127.0.0.1:2333/api/sessions/review-lead/{session-id} | jq
```
