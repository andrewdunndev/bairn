# api/famly/testdata/

Synthetic fixtures used by the unit tests in `api/famly`.

All names, IDs, URLs, and bodies in these files are fabricated. No
real Famly account data is checked in. Children are `Child A` /
`Child B`, logins are zero-padded UUIDs ending in a small integer
(`...000000000001`), images and videos point at `https://fixture/...`,
and post bodies are short generic strings (`"Great day at the park"`,
`"Music time"`).

## Files

| File | Purpose |
|------|---------|
| `me.json` | `/api/me/me/me` response: login id plus `roles2` for two children |
| `relations.json` | `/api/v2/relations` response: two parent-child relations on one child |
| `feed-page1.json` | `/api/feed/feed/feed` first page: one image post (two images, one tagged), one video post |
| `feed-page2.json` | second page: empty `feedItems` (terminator) |
| `auth-success.json` | GraphQL `authenticateWithPassword` success payload |
| `auth-failed.json` | GraphQL `authenticateWithPassword` `FailedInvalidPassword` payload |

## Regenerating or extending fixtures

If you need to extend coverage (new field, new shape, new failure
mode), do not paste in a captured response from your real account.
The procedure is:

1. Capture a real response from your own account using
   `discovery/probe/shape.py` (or any equivalent HAR/cURL flow).
   Captured output lands under `discovery/baselines/`, which is
   gitignored.
2. Run a sanitization pass over the captured JSON before copying any
   of it into `testdata/`:
   - Names (children, educators, parents): replace with `Child A`,
     `Child B`, `Educator A`, etc.
   - Login IDs, child IDs, relation IDs, image IDs: replace with
     zero-padded UUIDs or short synthetic ids
     (`00000000-0000-0000-0000-00000000c001`, `img-001`).
   - CDN URLs (anything under `img.famly.co`, signed S3 URLs, etc.):
     rewrite to `https://fixture/...`.
   - Post bodies, comments, descriptions: replace with a short
     generic string. Do not preserve real wording even if it looks
     innocuous.
   - Email addresses: use `alice@example.com` or similar.
3. Diff the sanitized file against the original and confirm no real
   identifiers, URLs, or prose survived.
4. Only then commit the sanitized file to `api/famly/testdata/`.

Captured-but-not-yet-sanitized responses must stay under
`discovery/baselines/` (gitignored). They must never be moved or
copied into `testdata/` without the sanitization pass above.

## Example: a sanitized fixture entry

```json
{
  "feedItemId": "post-001",
  "originatorId": "Post:e001",
  "createdDate": "2026-05-06T14:30:00Z",
  "body": "Great day at the park",
  "images": [
    {
      "imageId": "img-001",
      "url": "https://fixture/image/img-001",
      "url_big": "https://fixture/image/img-001/big",
      "width": 1024,
      "height": 768,
      "createdAt": {"date": "2026-05-06T14:25:00Z"},
      "expiration": "2026-05-07T14:30:00Z",
      "liked": true,
      "likes": [{"loginId": "00000000-0000-0000-0000-000000000001"}],
      "tags": [
        {"tag": "child-a", "type": "Child", "name": "Child A",
         "childId": "00000000-0000-0000-0000-00000000c001"}
      ]
    }
  ],
  "videos": []
}
```

Every identifier here is fabricated: the `imageId` is a counter, the
URLs point at a non-resolvable `fixture` host, the `loginId` and
`childId` are zero-padded UUIDs ending in a short integer, and the
body is a generic string.
