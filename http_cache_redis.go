package httpcache

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json/v2"
	"errors"
	"io"
	"log/slog"
	"math/rand"
	"net/http"
	"strconv"
	"time"

	"github.com/redis/go-redis/v9"
)

type HTTPClientCacheRedisConfig struct {
	RedisPrefix string        `json:"redis_prefix"`
	MinTTL      time.Duration `json:"min_ttl,format:units"`
}

func (s HTTPClientCacheRedisConfig) WithDefaults() HTTPClientCacheRedisConfig {
	if s.MinTTL == 0 {
		s.MinTTL = time.Minute
	}
	if s.RedisPrefix == "" {
		s.RedisPrefix = "httpcache:" + strconv.Itoa(rand.Int()) + ":"
	}
	return s
}

// HTTPClientCacheRedis caches HTTP responses in Redis, including body, headers, and status code.
type HTTPClientCacheRedis struct {
	Config HTTPClientCacheRedisConfig
	Client interface {
		Do(req *http.Request) (*http.Response, error)
	}
	Redis *redis.Client
}

func (s HTTPClientCacheRedis) copyBody(req *http.Request) ([]byte, error) {
	body, err := io.ReadAll(req.Body)
	if err != nil {
		return nil, err
	}

	req.Body = io.NopCloser(bytes.NewReader(body))
	req.GetBody = func() (io.ReadCloser, error) { return io.NopCloser(bytes.NewReader(body)), nil }

	return body, nil
}

func (s HTTPClientCacheRedis) key(req *http.Request) (string, error) {
	body, err := s.copyBody(req)
	if err != nil {
		return "", err
	}

	var hashInput bytes.Buffer
	hashInput.WriteString(req.Method)
	hashInput.WriteByte(0) // Delimiter
	hashInput.WriteString(req.URL.String())
	hashInput.WriteByte(0) // Delimiter
	hashInput.Write(body)

	hash := sha256.Sum256(hashInput.Bytes())
	return s.Config.RedisPrefix + hex.EncodeToString(hash[:]), nil
}

type cachedResponse struct {
	StatusCode int         `json:"status_code"`
	Headers    http.Header `json:"headers"`
	HasBody    bool        `json:"has_body"`
}

func (s HTTPClientCacheRedis) set(ctx context.Context, key string, ttl time.Duration, resp *http.Response) error {
	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return err
	}
	resp.Body.Close()
	resp.Body = io.NopCloser(bytes.NewReader(respBody))

	cachedResp := cachedResponse{
		StatusCode: resp.StatusCode,
		Headers:    resp.Header,
		HasBody:    len(respBody) > 0,
	}

	cacheEntryJSON, err := json.Marshal(cachedResp)
	if err != nil {
		return err
	}
	if err := s.Redis.Set(ctx, key, cacheEntryJSON, ttl).Err(); err != nil {
		return err
	}

	if len(respBody) > 0 {
		if err := s.Redis.Set(ctx, key+":b", respBody, ttl).Err(); err != nil {
			return err
		}
	}

	return nil
}

func (s HTTPClientCacheRedis) get(ctx context.Context, key string) (*http.Response, error) {
	b, err := s.Redis.Get(ctx, key).Bytes()
	if err != nil {
		if errors.Is(err, redis.Nil) {
			return nil, nil
		}
		return nil, err
	}

	var cachedResp cachedResponse
	if err := json.Unmarshal(b, &cachedResp); err != nil {
		return nil, err
	}

	var body []byte
	if cachedResp.HasBody {
		body, err = s.Redis.Get(ctx, key+":b").Bytes()
		if err != nil {
			return nil, err
		}
	}

	resp := &http.Response{
		StatusCode:    cachedResp.StatusCode,
		Header:        cachedResp.Headers,
		Body:          io.NopCloser(bytes.NewReader(body)),
		ContentLength: int64(len(body)),
		Proto:         "HTTP/1.1",
		ProtoMajor:    1,
		ProtoMinor:    1,
	}

	return resp, nil
}

func (s HTTPClientCacheRedis) Do(req *http.Request) (*http.Response, error) {
	ctx := req.Context()
	key, err := s.key(req)
	if err != nil {
		slog.ErrorContext(ctx, "cache: cannot get key", "error", err)
	}

	if resp, err := s.get(ctx, key); err != nil {
		slog.ErrorContext(ctx, "skip cache: cannot get cached response", "error", err)
	} else {
		if resp != nil {
			return resp, nil
		}
	}

	resp, err := s.Client.Do(req)
	if err != nil {
		return nil, err
	}

	var ttl time.Duration
	if s := resp.Header.Get("Expires"); s != "" {
		expiresAt, err := time.Parse(time.RFC1123, s)
		if err != nil {
			slog.ErrorContext(ctx, "skip cache: bad header: Expires", "error", err)
		} else {
			ttl = time.Until(expiresAt)
		}
	}

	if ttl > s.Config.MinTTL {
		if err := s.set(ctx, key, ttl, resp); err != nil {
			slog.ErrorContext(ctx, "skip cache: cannot cache response", "error", err)
		}
	}

	return resp, nil
}
