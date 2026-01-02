package httpcache_test

import (
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	httpcache "github.com/ndx-technologies/mm-go-redis-http-cache"
	"github.com/redis/go-redis/v9"
)

func TestHTTPClientCacheRedis(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping test in short mode; requires network and Redis")
	}

	addr := os.Getenv("REDIS_ADDR")
	if addr == "" {
		addr = "localhost:6379"
	}
	rdb := redis.NewClient(&redis.Options{
		Addr:     addr,
		Password: os.Getenv("REDIS_PASSWORD"),
	})

	ctx := t.Context()

	if err := rdb.Ping(ctx).Err(); err != nil {
		t.Fatal(err)
	}

	callCounts := make(map[string]int)
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		callCounts[r.URL.Path]++
		switch r.URL.Path {
		case "/cache-hit":
			w.Header().Set("Expires", time.Now().Add(1*time.Hour).Format(time.RFC1123))
			w.Write([]byte("cached"))
		case "/no-expires":
			w.Write([]byte("no cache"))
		default:
			http.NotFound(w, r)
		}
	}))
	defer ts.Close()

	cache := httpcache.HTTPClientCacheRedis{
		Client: http.DefaultClient,
		Redis:  rdb,
	}

	t.Run("cache miss", func(t *testing.T) {
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, ts.URL+"/cache-hit", strings.NewReader("some body"))
		if err != nil {
			t.Error(err)
		}

		resp, err := cache.Do(req)
		if err != nil {
			t.Error(err)
		}

		if resp.StatusCode != http.StatusOK {
			t.Errorf("unexpected status code: %d", resp.StatusCode)
		}

		body, err := io.ReadAll(resp.Body)
		if err != nil {
			t.Error(err)
		}

		resp.Body.Close()

		if string(body) != "cached" {
			t.Error(string(body))
		}

		if callCounts["/cache-hit"] != 1 {
			t.Error(callCounts["/cache-hit"])
		}
	})

	t.Run("cache hit", func(t *testing.T) {
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, ts.URL+"/cache-hit", strings.NewReader("some body"))
		if err != nil {
			t.Error(err)
		}

		resp, err := cache.Do(req)
		if err != nil {
			t.Error(err)
		}

		if resp.StatusCode != http.StatusOK {
			t.Errorf("unexpected status code: %d", resp.StatusCode)
		}

		body, err := io.ReadAll(resp.Body)
		if err != nil {
			t.Error(err)
		}

		resp.Body.Close()

		if string(body) != "cached" {
			t.Error(string(body))
		}

		if callCounts["/cache-hit"] != 1 {
			t.Error(callCounts["/cache-hit"])
		}
	})

	t.Run("when no Expires header, not cached", func(t *testing.T) {
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, ts.URL+"/no-expires", strings.NewReader("some body"))
		if err != nil {
			t.Error(err)
		}

		resp, err := cache.Do(req)
		if err != nil {
			t.Error(err)
		}

		if resp.StatusCode != http.StatusOK {
			t.Errorf("unexpected status code: %d", resp.StatusCode)
		}

		body, err := io.ReadAll(resp.Body)
		if err != nil {
			t.Error(err)
		}

		resp.Body.Close()

		if string(body) != "no cache" {
			t.Error(string(body))
		}

		if callCounts["/no-expires"] != 1 {
			t.Error(callCounts["/no-expires"])
		}
	})
}
