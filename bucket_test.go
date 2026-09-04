package bucket_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/hanzoai/bucket"
)

func TestNewInvalidBaseURL(t *testing.T) {
	if _, err := bucket.New(bucket.Config{BaseURL: "://bad"}); err == nil {
		t.Fatal("expected error for unparseable BaseURL")
	}
}

func TestCreate(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got, want := r.Method, http.MethodPost; got != want {
			t.Errorf("method = %s, want %s", got, want)
		}
		if got, want := r.URL.Path, "/v1/kv/sessions"; got != want {
			t.Errorf("path = %s, want %s", got, want)
		}
		if got, want := r.Header.Get("Authorization"), "Bearer svc-token"; got != want {
			t.Errorf("Authorization = %q, want %q", got, want)
		}
		var in struct {
			History int   `json:"history"`
			TTL     int64 `json:"ttl"`
		}
		if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
			t.Fatal(err)
		}
		if in.TTL != 3600 {
			t.Errorf("ttl = %d, want 3600", in.TTL)
		}
		w.WriteHeader(http.StatusCreated)
		_ = json.NewEncoder(w).Encode(bucket.Bucket{Bucket: "sessions", History: 1, TTL: in.TTL})
	}))
	defer srv.Close()

	c, err := bucket.New(bucket.Config{BaseURL: srv.URL, Token: "svc-token"})
	if err != nil {
		t.Fatal(err)
	}
	b, err := c.Create(context.Background(), "sessions", 1, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if b.Bucket != "sessions" || b.TTL != 3600 {
		t.Errorf("got %+v", b)
	}
}

func TestCreateConflict(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusConflict)
	}))
	defer srv.Close()

	c, err := bucket.New(bucket.Config{BaseURL: srv.URL})
	if err != nil {
		t.Fatal(err)
	}
	_, err = c.Create(context.Background(), "sessions", 1, time.Hour)
	if err == nil || err != bucket.ErrConflict {
		t.Fatalf("err = %v, want ErrConflict", err)
	}
}

func TestPutGetRoundTrip(t *testing.T) {
	store := map[string]string{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodPut:
			var in struct {
				Value string `json:"value"`
			}
			_ = json.NewDecoder(r.Body).Decode(&in)
			store[r.URL.Path] = in.Value
			_ = json.NewEncoder(w).Encode(map[string]uint64{"revision": 1})
		case http.MethodGet:
			v, ok := store[r.URL.Path]
			if !ok {
				w.WriteHeader(http.StatusNotFound)
				return
			}
			_ = json.NewEncoder(w).Encode(bucket.Entry{Key: "theme", Value: v, Revision: 1, Operation: "put"})
		}
	}))
	defer srv.Close()

	c, err := bucket.New(bucket.Config{BaseURL: srv.URL})
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()

	rev, err := c.Put(ctx, "prefs", "theme", `{"mode":"dark"}`)
	if err != nil {
		t.Fatal(err)
	}
	if rev != 1 {
		t.Errorf("revision = %d, want 1", rev)
	}

	e, err := c.Get(ctx, "prefs", "theme")
	if err != nil {
		t.Fatal(err)
	}
	if e.Value != `{"mode":"dark"}` {
		t.Errorf("value = %q", e.Value)
	}

	var dst struct {
		Mode string `json:"mode"`
	}
	if err := c.GetJSON(ctx, "prefs", "theme", &dst); err != nil {
		t.Fatal(err)
	}
	if dst.Mode != "dark" {
		t.Errorf("mode = %q, want dark", dst.Mode)
	}
}

func TestGetNotFound(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))
	defer srv.Close()

	c, err := bucket.New(bucket.Config{BaseURL: srv.URL})
	if err != nil {
		t.Fatal(err)
	}
	_, err = c.Get(context.Background(), "prefs", "missing")
	if err == nil || err != bucket.ErrNotFound {
		t.Fatalf("err = %v, want ErrNotFound", err)
	}
}

func TestPutJSON(t *testing.T) {
	var gotBody string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var in struct {
			Value string `json:"value"`
		}
		_ = json.NewDecoder(r.Body).Decode(&in)
		gotBody = in.Value
		_ = json.NewEncoder(w).Encode(map[string]uint64{"revision": 2})
	}))
	defer srv.Close()

	c, err := bucket.New(bucket.Config{BaseURL: srv.URL})
	if err != nil {
		t.Fatal(err)
	}
	type payload struct {
		Mode string `json:"mode"`
	}
	rev, err := c.PutJSON(context.Background(), "prefs", "theme", payload{Mode: "dark"})
	if err != nil {
		t.Fatal(err)
	}
	if rev != 2 {
		t.Errorf("revision = %d, want 2", rev)
	}
	if gotBody != `{"mode":"dark"}` {
		t.Errorf("body = %q", gotBody)
	}
}

func TestDelete(t *testing.T) {
	var gotMethod, gotPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod, gotPath = r.Method, r.URL.Path
		w.WriteHeader(http.StatusNoContent)
	}))
	defer srv.Close()

	c, err := bucket.New(bucket.Config{BaseURL: srv.URL})
	if err != nil {
		t.Fatal(err)
	}
	if err := c.Delete(context.Background(), "prefs", "theme"); err != nil {
		t.Fatal(err)
	}
	if gotMethod != http.MethodDelete || gotPath != "/v1/kv/prefs/theme" {
		t.Errorf("got %s %s", gotMethod, gotPath)
	}
}

func TestHistory(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/kv/prefs/theme/history" {
			t.Errorf("path = %s", r.URL.Path)
		}
		_ = json.NewEncoder(w).Encode(map[string][]bucket.Entry{
			"data": {
				{Key: "theme", Value: "light", Revision: 1, Operation: "put"},
				{Key: "theme", Value: "dark", Revision: 2, Operation: "put"},
			},
		})
	}))
	defer srv.Close()

	c, err := bucket.New(bucket.Config{BaseURL: srv.URL})
	if err != nil {
		t.Fatal(err)
	}
	entries, err := c.History(context.Background(), "prefs", "theme")
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 2 || entries[1].Value != "dark" {
		t.Errorf("got %+v", entries)
	}
}

func TestServerError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte("boom"))
	}))
	defer srv.Close()

	c, err := bucket.New(bucket.Config{BaseURL: srv.URL})
	if err != nil {
		t.Fatal(err)
	}
	_, err = c.Get(context.Background(), "prefs", "theme")
	if err == nil {
		t.Fatal("expected error")
	}
}
