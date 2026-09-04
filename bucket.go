// Package bucket is the Go client for cloud's versioned key-value store —
// the plain-cache replacement for a product's own Redis. The server lives at
// github.com/hanzoai/cloud/apps/kv, mounted at /v1/kv on api.hanzo.ai; this
// package is the thin HTTP client every other product imports instead of
// writing its own.
//
// # NOT TO BE CONFUSED WITH "Hanzo KV"
//
// github.com/hanzoai/kv is a separate product: a from-scratch Redis-protocol
// server (RESP2/RESP3, Lua scripting, pub/sub, streams), reached with its own
// client (github.com/hanzokv/go) over the wire protocol on port 6379. That is
// still Redis, wire-compatible, wherever it runs. This package speaks to
// neither of those — it is a plain HTTP/JSON client for cloud's /v1/kv REST
// surface, which is a narrower, versioned bucket store, not a Redis
// replacement server.
//
// A bucket holds keyed values. Each key keeps up to History revisions, and
// every value in a bucket shares the bucket's own TTL, set once at creation
// — there is no per-key TTL. A miss reads back as ErrNotFound.
//
// # WHAT IT IS NOT
//
// It is not a lock. Put takes a key and a value, nothing else — no expected
// revision, no compare-and-swap — so two concurrent Puts to the same key
// race exactly like two concurrent writes to a plain variable: last write
// wins. A caller that needs mutual exclusion needs a different primitive,
// not this one.
//
// It is not pub/sub. A write here notifies no one; nothing about a bucket
// publishes, subscribes, or streams.
//
// It has no listing or scan. A key is read by name; there is no operation
// that enumerates the keys a bucket holds. A caller that needs to iterate a
// keyspace needs to track that keyspace itself (e.g. an index value) or a
// different store.
//
// The HTTP contract is cloud's canonical KV API, mounted under /v1
// (cloud/apps/kv/kv.go):
//
//	POST   {BaseURL}/v1/kv/{bucket}                create a bucket
//	DELETE {BaseURL}/v1/kv/{bucket}                drop a bucket and everything in it
//	GET    {BaseURL}/v1/kv/{bucket}/{key}          read the current value
//	PUT    {BaseURL}/v1/kv/{bucket}/{key}          write a value, get back its revision
//	DELETE {BaseURL}/v1/kv/{bucket}/{key}          delete one key
//	GET    {BaseURL}/v1/kv/{bucket}/{key}/history  a key's retained revisions
//
// Auth is the caller's own Hanzo IAM bearer token, sent as
//
//	Authorization: Bearer {Token}
//
// The org a bucket belongs to comes from that token's own validated claim,
// never from a field or header this package sends.
//
// This is a leaf package: it imports only the standard library, so any
// product — Go service, CLI, or job — can import github.com/hanzoai/bucket
// without pulling cloud's server code into its binary.
package bucket

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// ErrNotFound is returned by Get, Delete and History when the bucket does
// not exist, the key was never written, or (Get only) its latest revision is
// a delete marker. Test with errors.Is.
var ErrNotFound = errors.New("bucket: not found")

// ErrConflict is returned by Create when the org already has a bucket of
// that name. Test with errors.Is.
var ErrConflict = errors.New("bucket: already exists")

// HTTPDoer is the minimal HTTP surface the client needs. *http.Client
// satisfies it; tests substitute their own.
type HTTPDoer interface {
	Do(req *http.Request) (*http.Response, error)
}

// Config configures a Client. New only rejects an unparseable BaseURL, so a
// caller can build a Client before Token is available and set it later.
type Config struct {
	// BaseURL is the Hanzo API base, e.g. "https://api.hanzo.ai". No
	// trailing /v1 — the client appends the canonical KV paths itself.
	BaseURL string

	// Token is the caller's Hanzo IAM bearer token. MUST come from KMS or an
	// operator-injected secret; never hard-code it. Sent as
	// "Authorization: Bearer".
	Token string

	// Timeout bounds each HTTP call. Default 5s.
	Timeout time.Duration

	// HTTPClient overrides the underlying HTTP client. When nil a client
	// with Timeout is created.
	HTTPClient HTTPDoer
}

// Client is the bucket-store HTTP client. It is safe for concurrent use.
type Client struct {
	baseURL string
	token   string
	http    HTTPDoer
}

// New builds a Client from cfg. It returns an error only for an unparseable
// BaseURL.
func New(cfg Config) (*Client, error) {
	base := strings.TrimRight(strings.TrimSpace(cfg.BaseURL), "/")
	if base != "" {
		if _, err := url.Parse(base); err != nil {
			return nil, fmt.Errorf("bucket: invalid BaseURL %q: %w", cfg.BaseURL, err)
		}
	}

	doer := cfg.HTTPClient
	if doer == nil {
		timeout := cfg.Timeout
		if timeout <= 0 {
			timeout = 5 * time.Second
		}
		doer = &http.Client{Timeout: timeout}
	}

	return &Client{baseURL: base, token: strings.TrimSpace(cfg.Token), http: doer}, nil
}

// Bucket describes one bucket as the server reports it.
type Bucket struct {
	Bucket  string `json:"bucket"`
	History int    `json:"history"`
	TTL     int64  `json:"ttl"` // seconds; 0 means no expiry
	Values  uint64 `json:"values"`
}

// Entry is one key's value at one revision.
type Entry struct {
	Key       string `json:"key"`
	Value     string `json:"value"`
	Revision  uint64 `json:"revision"`
	Created   string `json:"created"`   // RFC3339
	Operation string `json:"operation"` // put, del or purge
}

// Create creates a bucket in the caller's org. history is how many
// revisions each key keeps (1-64; 0 means 1). ttl expires every entry in the
// bucket after that duration, rounded down to the second (0 means never) —
// TTL is set once per bucket, not per key. Returns ErrConflict if the org
// already has a bucket of that name.
func (c *Client) Create(ctx context.Context, name string, history int, ttl time.Duration) (*Bucket, error) {
	body := struct {
		History int   `json:"history"`
		TTL     int64 `json:"ttl"`
	}{History: history, TTL: int64(ttl / time.Second)}

	var out Bucket
	if err := c.do(ctx, http.MethodPost, "/v1/kv/"+url.PathEscape(name), body, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// Drop removes a bucket and every key and revision in it. ErrNotFound if the
// org has no bucket of that name.
func (c *Client) Drop(ctx context.Context, name string) error {
	return c.do(ctx, http.MethodDelete, "/v1/kv/"+url.PathEscape(name), nil, nil)
}

// Get returns key's current value and revision in bucket. ErrNotFound if the
// bucket does not exist, the key was never written, or its latest revision
// is a delete.
func (c *Client) Get(ctx context.Context, bucket, key string) (*Entry, error) {
	var out Entry
	if err := c.do(ctx, http.MethodGet, path(bucket, key), nil, &out); err != nil {
		return nil, err
	}
	out.Key = key
	return &out, nil
}

// GetJSON reads key in bucket and unmarshals its value into dst. A miss
// leaves dst untouched and reports ErrNotFound, matching Get.
func (c *Client) GetJSON(ctx context.Context, bucket, key string, dst any) error {
	e, err := c.Get(ctx, bucket, key)
	if err != nil {
		return err
	}
	if err := json.Unmarshal([]byte(e.Value), dst); err != nil {
		return fmt.Errorf("bucket: unmarshal value: %w", err)
	}
	return nil
}

// Put sets key to value in bucket and returns the revision the write
// created. Each Put is a new revision; it does not check or depend on any
// prior value — see the package doc for why this is not a lock.
func (c *Client) Put(ctx context.Context, bucket, key, value string) (uint64, error) {
	body := struct {
		Value string `json:"value"`
	}{Value: value}

	var out struct {
		Revision uint64 `json:"revision"`
	}
	if err := c.do(ctx, http.MethodPut, path(bucket, key), body, &out); err != nil {
		return 0, err
	}
	return out.Revision, nil
}

// PutJSON marshals value and Puts it.
func (c *Client) PutJSON(ctx context.Context, bucket, key string, value any) (uint64, error) {
	data, err := json.Marshal(value)
	if err != nil {
		return 0, fmt.Errorf("bucket: marshal value: %w", err)
	}
	return c.Put(ctx, bucket, key, string(data))
}

// Delete removes key from bucket. ErrNotFound if the bucket does not exist.
// Deleting an already-absent key in an existing bucket is not an error.
func (c *Client) Delete(ctx context.Context, bucket, key string) error {
	return c.do(ctx, http.MethodDelete, path(bucket, key), nil, nil)
}

// History returns key's retained revisions in bucket, oldest first.
// ErrNotFound if the bucket does not exist or the key was never written.
func (c *Client) History(ctx context.Context, bucket, key string) ([]Entry, error) {
	var out struct {
		Data []Entry `json:"data"`
	}
	if err := c.do(ctx, http.MethodGet, path(bucket, key)+"/history", nil, &out); err != nil {
		return nil, err
	}
	for i := range out.Data {
		out.Data[i].Key = key
	}
	return out.Data, nil
}

// path builds the request path for one key. The key is base64url-encoded
// rather than merely percent-escaped: a key containing "/" (URLs are common
// keys) survives as literal bytes when percent-escaped, but many HTTP routers
// decode the path before matching route segments, so an encoded slash can be
// mistaken for a path separator by the time it is matched. base64url never
// produces "/", so this ambiguity does not exist. The bucket name has no such
// problem — the server itself constrains it to a short identifier — so it is
// only percent-escaped, keeping bucket names human-readable on the wire.
func path(bucket, key string) string {
	return "/v1/kv/" + url.PathEscape(bucket) + "/" + base64.RawURLEncoding.EncodeToString([]byte(key))
}

// do sends one request and decodes a JSON response into out (skipped when
// out is nil). A 404 becomes ErrNotFound and a 409 becomes ErrConflict so
// callers can branch with errors.Is instead of inspecting status codes.
func (c *Client) do(ctx context.Context, method, reqPath string, body, out any) error {
	var reqBody io.Reader
	if body != nil {
		data, err := json.Marshal(body)
		if err != nil {
			return fmt.Errorf("bucket: encode request: %w", err)
		}
		reqBody = bytes.NewReader(data)
	}

	req, err := http.NewRequestWithContext(ctx, method, c.baseURL+reqPath, reqBody)
	if err != nil {
		return fmt.Errorf("bucket: build request: %w", err)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if c.token != "" {
		req.Header.Set("Authorization", "Bearer "+c.token)
	}

	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("bucket: %s %s: %w", method, reqPath, err)
	}
	defer resp.Body.Close()

	switch resp.StatusCode {
	case http.StatusNotFound:
		_, _ = io.Copy(io.Discard, resp.Body)
		return ErrNotFound
	case http.StatusConflict:
		_, _ = io.Copy(io.Discard, resp.Body)
		return ErrConflict
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		data, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return fmt.Errorf("bucket: %s %s: status %d: %s", method, reqPath, resp.StatusCode, strings.TrimSpace(string(data)))
	}
	if out == nil {
		_, _ = io.Copy(io.Discard, resp.Body)
		return nil
	}
	if err := json.NewDecoder(resp.Body).Decode(out); err != nil {
		return fmt.Errorf("bucket: decode response: %w", err)
	}
	return nil
}
