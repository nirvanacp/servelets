# Hacker News bookmarks store

The `hn-bookmarks` servelet: the bookmarks, categories and their order for the `hn-bookmarks` applet. It runs on the phone inside Genesis, with its own SQLite database, and answers the applet's `bookmarks` service. It keeps the rules of the Node backend it replaced, so the applet's code did not change.

## Routes

| Method and path | Body | Answer |
| --- | --- | --- |
| `GET /health` | | 200 `{ "ready": true }` |
| `POST /v1/documents` | `{ "operation": "get" \| "list" \| "commit", "params": { … } }` | 200 `{ "result", "changes" }` |

Every other method or path is 404 `NOT_FOUND`.

- `get`, params `{ collection, key }`: `result` is `{ document, namespaceRevision }`, with `document` null when it does not exist.
- `list`, params `{ collection, afterKey?, limit }` with `limit` 1 to 100: `result` is `{ entries, nextAfterKey, namespaceRevision }`. Entries come in key order and a page holds at most about 240 000 bytes; `nextAfterKey` is null on the last page.
- `commit`, params `{ checks, puts, deletes }`: every put and delete needs a check `{ collection, key, expectedRevision }` (null for a document that must not exist), at most 128 checks and 64 changes, and a document is at most 32 KiB of JSON. A commit raises the namespace revision by one; `result` is `{ namespaceRevision }` and `changes` lists the changed keys.

A document is `{ key, value, revision, updatedAtMs }`. The collections are `bookmarks`, `bookmark-order`, `categories`, `category-meta` and `category-names`; a key is 1 to 512 bytes.

Errors are `{ "error": { "code", "message" } }`: 400 `INVALID_ARGUMENT` for invalid input, 409 `CONFLICT` when a revision check fails, 500 `STORAGE_FAILURE` otherwise.

## Schema

| Version | Change |
| --- | --- |
| 1 | `state` (the namespace revision) and `documents`, as the Node backend's version 1 |

## Build and check

```sh
npx let-servelet check .
npx let-servelet run . --port 8790
```
