# bucket — the Go client for cloud's versioned key-value store

`github.com/hanzoai/bucket` is the one client Hanzo products use to reach the
versioned bucket store at `/v1/kv/{bucket}/{key}` on api.hanzo.ai, instead of
running their own Redis for a plain cache.

**Not to be confused with `github.com/hanzoai/kv`** — that is a separate
product, a from-scratch Redis-protocol server (RESP2/RESP3, Lua scripting,
pub/sub, streams) reached with its own client, `github.com/hanzokv/go`, over
the wire protocol. That is still Redis, wherever it runs. This package talks
HTTP/JSON to cloud's `/v1/kv` REST surface, a narrower, versioned bucket
store — not a Redis replacement server.

## What it does

A bucket holds keyed values. Each key keeps up to `History` revisions, and
every value in a bucket shares the bucket's own TTL, set once at creation —
there is no per-key TTL.

| Op | Endpoint |
|----|----------|
| `Create` | `POST /v1/kv/{bucket}` |
| `Drop` | `DELETE /v1/kv/{bucket}` |
| `Get` / `GetJSON` | `GET /v1/kv/{bucket}/{key}` |
| `Put` / `PutJSON` | `PUT /v1/kv/{bucket}/{key}` |
| `Delete` | `DELETE /v1/kv/{bucket}/{key}` |
| `History` | `GET /v1/kv/{bucket}/{key}/history` |

Auth is the caller's own Hanzo IAM bearer token:

```
Authorization: Bearer ${HANZO_API_TOKEN}
```

## What it is not

- **Not a lock.** `Put` takes a key and a value — no expected revision, no
  compare-and-swap. Two concurrent `Put`s to the same key race; last write
  wins. Something that needs mutual exclusion needs a different primitive.
- **Not pub/sub.** A write here notifies no one.
- **No listing or scan.** A key is read by name; there is no operation that
  enumerates a bucket's keys.

## Use it

```go
client, err := bucket.FromEnv() // HANZO_API_BASE (default https://api.hanzo.ai) + HANZO_API_TOKEN

if _, err := client.Create(ctx, "sessions", 1, time.Hour); err != nil &&
	!errors.Is(err, bucket.ErrConflict) {
	return err
}

if _, err := client.Put(ctx, "sessions", userID, token); err != nil {
	return err
}

entry, err := client.Get(ctx, "sessions", userID)
if errors.Is(err, bucket.ErrNotFound) {
	// cache miss
}
```

This is a leaf package: it imports only the standard library, so any
product — Go service, CLI, or job — can import it without pulling cloud's
server code into its binary.
