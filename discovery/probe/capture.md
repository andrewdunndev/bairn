# Mode 2: traffic capture playbook

This is the manual-driven discovery mode. Use it to find vendor
endpoints the official client hits that our probe manifest doesn't
yet know about.

Outputs (HAR files, endpoint diffs) are gitignored.

## Setup

1. Open a Chromium under Playwright control. The Playwright MCP
   server at `npx -y @playwright/mcp@latest` works.
2. Navigate to the vendor app and log in. Drive the login yourself
   so credentials never enter the agent's transcript.
3. Open the network panel; clear it.

## Walk

Visit every screen the operator uses in normal flow. Move through
each tab, trigger each action that fetches data, scroll lists to
their end. After each screen, note any new request hosts or paths.

The vendor-specific list of screens (feed, profile tabs, daily
reports, messages, etc.) lives in the operator's local notes, not
here.

## Capture

Export the network log as HAR:

  - DevTools network panel → right-click → "Save all as HAR"
  - Save under `discovery/captures/<date>-<screen>.har`
  - HAR files are gitignored

## Diff against known surface

Pipe HAR through a small jq filter to extract unique
(method, host, pathname). Compare against the manifest endpoint
list. New entries are candidates for inclusion in the typed client.

  jq -r '.log.entries[] |
    [.request.method, (.request.url | sub("\\?.*"; ""))] | @tsv' \\
    discovery/captures/*.har | sort -u

## Privacy

HAR files contain full request and response bodies including auth
headers and any PII. Treat them as you would credentials. Never
commit. Delete or move off-machine when the walk is finished.
