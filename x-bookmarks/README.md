# X categories store

The `x-bookmarks` servelet: the categories and saved posts of the `x-bookmarks` applet. It runs on the phone inside Genesis, with its own SQLite database, and answers the applet's `bookmarks` service. It keeps the rules and answers of the Node backend it replaced. The one difference is how the library gets its account: the Node server read it from `X_EXPECTED_HANDLE`, while this servelet binds the library to the first signed-in X handle the applet sends to `POST /v1/library`.

## Routes

| Method and path | Body | Answer |
| --- | --- | --- |
| `GET /health` | | 200 `{ "ready": true }` |
| `POST /v1/library` | `{ "expectedHandle" }` | 200 the library; creates it for that handle when there is none |
| `GET /v1/library` | | 200 the library |
| `GET /v1/categories` | | 200 `{ libraryRevision, categories }`, each with its post count, in name order |
| `POST /v1/bookmarks/lookup` | `{ postIds }`, at most 50 unique | 200 `{ records, missingIds, libraryRevision }` |
| `POST /v1/bookmarks/query` | `{ categoryId?, query?, limit: 20, cursor? }` | 200 `{ entries, nextCursor, libraryRevision, filter }`, newest first, at most 20 posts and about 192 KiB a page |
| `POST /v1/operations` | `{ operationId, kind, payload }` | 200 the receipt `{ operationId, kind, status, result, libraryRevision, createdAtMs }` |
| `GET /v1/operations/{operationId}` | | 200 the receipt of a committed operation, or 404 `OPERATION_NOT_FOUND` |

Every other method or path is 404 `NOT_FOUND`. Request bodies are at most 256 KiB (413 `TOO_LARGE`).

A library is `{ version, libraryId, expectedHandle, revision }`. Every endpoint but the claim and `/health` answers 409 `LIBRARY_MISSING` until the library exists. A claim with another handle returns the existing library unchanged, so the applet can tell the user that X is signed in to a different account.

Operation kinds are `category.create`, `category.rename`, `category.delete`, `bookmark.setCategories` and `bookmark.remove`. Each one raises the library revision by one and stores its receipt; sending the same operation again returns that receipt, and reusing its ID for a different change is 409 `OPERATION_REUSED`. A library has at most 50 categories, and a saved post belongs to 1 to 10 of them. The first `bookmark.setCategories` of a post also takes its `url` and `snapshot`.

Errors are `{ "error": { "code", "message", "details"? } }`: 400 `INVALID_ARGUMENT` for invalid input, 409 for `CONFLICT`, `CATEGORY_MISSING`, `CATEGORY_NAME_EXISTS` (with `details.existingCategoryId`), `CATEGORY_NOT_EMPTY`, `CURSOR_STALE` and `LIMIT_REACHED`, and 503 `STORAGE_FAILURE` when the outcome is unknown.

## Schema

| Version | Change |
| --- | --- |
| 1 | `libraries`, `categories`, `saved_posts`, `memberships` and `operations`, as the Node backend's version 1 |

## Build and check

```sh
npx let-servelet check .
npx let-servelet run . --port 8790
```
