package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	_ "github.com/lib/pq"
	"github.com/redis/go-redis/v9"
)

type dbStore interface {
	ping(context.Context) error
	roundTrip(context.Context) (string, error)
}

type objectStore interface {
	ping(context.Context) error
	roundTrip(context.Context, string, []byte) error
}

type cacheStore interface {
	ping(context.Context) error
	roundTrip(context.Context, string, string) error
	keyPrefix() string
}

type app struct {
	db      dbStore
	objects objectStore
	cache   cacheStore
}

func main() {
	ctx := context.Background()
	handler := newApp(dbFromEnv(ctx), objectStoreFromEnv(ctx), cacheFromEnv())

	port := os.Getenv("PORT")
	if port == "" {
		port = "8080"
	}
	log.Printf("listening on :%s", port)
	log.Fatal(http.ListenAndServe(":"+port, handler))
}

func newApp(db dbStore, objects objectStore, cache cacheStore) http.Handler {
	a := &app{db: db, objects: objects, cache: cache}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /health", a.handleHealth)
	mux.HandleFunc("GET /ready", a.handleReady)
	mux.HandleFunc("GET /db", a.handleDB)
	mux.HandleFunc("GET /storage", a.handleStorage)
	mux.HandleFunc("GET /redis", a.handleRedis)
	mux.HandleFunc("GET /env", a.handleEnv)
	mux.HandleFunc("GET /", a.handleRoot)
	return mux
}

func (a *app) handleRoot(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	_, _ = w.Write([]byte("anyhost fullstack smoke is running\n"))
}

func (a *app) handleHealth(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]string{
		"service": "anyhost-fullstack-smoke",
		"status":  "ok",
	})
}

func (a *app) handleReady(w http.ResponseWriter, r *http.Request) {
	checks := map[string]string{}
	status := http.StatusOK
	for name, ping := range map[string]func(context.Context) error{
		"postgres": a.db.ping,
		"storage":  a.objects.ping,
		"redis":    a.cache.ping,
	} {
		if err := ping(r.Context()); err != nil {
			checks[name] = err.Error()
			status = http.StatusServiceUnavailable
		} else {
			checks[name] = "ok"
		}
	}
	state := "ready"
	if status != http.StatusOK {
		state = "not_ready"
	}
	writeJSON(w, status, map[string]any{
		"service": "anyhost-fullstack-smoke",
		"status":  state,
		"checks":  checks,
	})
}

func (a *app) handleDB(w http.ResponseWriter, r *http.Request) {
	value, err := a.db.roundTrip(r.Context())
	if err != nil {
		writeDetailedError(w, http.StatusServiceUnavailable, "postgres round trip failed", err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{
		"service": "anyhost-fullstack-smoke",
		"status":  "ok",
		"value":   value,
	})
}

func (a *app) handleStorage(w http.ResponseWriter, r *http.Request) {
	payload := []byte(fmt.Sprintf("fullstack-smoke-%d", time.Now().UnixNano()))
	key := "fullstack-smoke/" + hex.EncodeToString(payload[:8])
	if err := a.objects.roundTrip(r.Context(), key, payload); err != nil {
		writeDetailedError(w, http.StatusServiceUnavailable, "storage round trip failed", err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{
		"service": "anyhost-fullstack-smoke",
		"status":  "ok",
		"key":     key,
	})
}

func (a *app) handleRedis(w http.ResponseWriter, r *http.Request) {
	value := fmt.Sprintf("fullstack-smoke-%d", time.Now().UnixNano())
	if err := a.cache.roundTrip(r.Context(), "probe", value); err != nil {
		writeDetailedError(w, http.StatusServiceUnavailable, "redis round trip failed", err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{
		"service":    "anyhost-fullstack-smoke",
		"status":     "ok",
		"key_prefix": a.cache.keyPrefix(),
	})
}

func (a *app) handleEnv(w http.ResponseWriter, _ *http.Request) {
	// Expose only non-sensitive presence/checksum evidence for gate verification.
	plain := strings.TrimSpace(os.Getenv("SMOKE_PLAIN_MARKER"))
	secret := strings.TrimSpace(os.Getenv("SMOKE_SECRET_MARKER"))
	writeJSON(w, http.StatusOK, map[string]any{
		"service": "anyhost-fullstack-smoke",
		"status":  "ok",
		"present": map[string]bool{
			"DATABASE_URL":     strings.TrimSpace(os.Getenv("DATABASE_URL")) != "",
			"S3_BUCKET":        strings.TrimSpace(os.Getenv("S3_BUCKET")) != "",
			"S3_PREFIX":        strings.TrimSpace(os.Getenv("S3_PREFIX")) != "",
			"S3_REGION":        strings.TrimSpace(os.Getenv("S3_REGION")) != "",
			"REDIS_URL":        strings.TrimSpace(os.Getenv("REDIS_URL")) != "",
			"REDIS_KEY_PREFIX": strings.TrimSpace(os.Getenv("REDIS_KEY_PREFIX")) != "",
		},
		"plain_marker":          plain,
		"secret_marker_sha256":  checksumOrEmpty(secret),
		"secret_marker_present": secret != "",
	})
}

type postgresStore struct {
	db *sql.DB
}

func dbFromEnv(ctx context.Context) dbStore {
	dsn := strings.TrimSpace(os.Getenv("DATABASE_URL"))
	if dsn == "" {
		return missingDBStore("DATABASE_URL is not set")
	}
	db, err := sql.Open("postgres", dsn)
	if err != nil {
		return missingDBStore("open postgres: " + err.Error())
	}
	_ = ctx
	return &postgresStore{db: db}
}

func (s *postgresStore) ensureSchema(ctx context.Context) error {
	_, err := s.db.ExecContext(ctx, `
		CREATE TABLE IF NOT EXISTS fullstack_smoke_probe (
			id TEXT PRIMARY KEY,
			value TEXT NOT NULL,
			created_at TIMESTAMPTZ NOT NULL
		)
	`)
	return err
}

func (s *postgresStore) ping(ctx context.Context) error {
	if err := s.db.PingContext(ctx); err != nil {
		return err
	}
	return s.ensureSchema(ctx)
}

func (s *postgresStore) roundTrip(ctx context.Context) (string, error) {
	if err := s.ensureSchema(ctx); err != nil {
		return "", err
	}
	id := fmt.Sprintf("probe-%d", time.Now().UnixNano())
	value := "ok-" + id
	if _, err := s.db.ExecContext(ctx, `
		INSERT INTO fullstack_smoke_probe (id, value, created_at)
		VALUES ($1, $2, $3)
	`, id, value, time.Now().UTC()); err != nil {
		return "", err
	}
	var got string
	if err := s.db.QueryRowContext(ctx, `
		SELECT value FROM fullstack_smoke_probe WHERE id = $1
	`, id).Scan(&got); err != nil {
		return "", err
	}
	if got != value {
		return "", fmt.Errorf("read back %q, want %q", got, value)
	}
	return got, nil
}

type s3ObjectStore struct {
	client *s3.Client
	bucket string
	prefix string
}

func objectStoreFromEnv(ctx context.Context) objectStore {
	bucket := strings.TrimSpace(os.Getenv("S3_BUCKET"))
	region := strings.TrimSpace(os.Getenv("S3_REGION"))
	if bucket == "" || region == "" {
		return missingObjectStore("S3_BUCKET and S3_REGION are required")
	}
	opts := []func(*awsconfig.LoadOptions) error{awsconfig.WithRegion(region)}
	accessKey := strings.TrimSpace(os.Getenv("S3_ACCESS_KEY_ID"))
	secretKey := strings.TrimSpace(os.Getenv("S3_SECRET_ACCESS_KEY"))
	if accessKey != "" || secretKey != "" {
		if accessKey == "" || secretKey == "" {
			return missingObjectStore("S3_ACCESS_KEY_ID and S3_SECRET_ACCESS_KEY must be provided together")
		}
		opts = append(opts, awsconfig.WithCredentialsProvider(credentials.NewStaticCredentialsProvider(accessKey, secretKey, "")))
	}
	cfg, err := awsconfig.LoadDefaultConfig(ctx, opts...)
	if err != nil {
		return missingObjectStore("load aws config: " + err.Error())
	}
	cfg.RequestChecksumCalculation = aws.RequestChecksumCalculationWhenRequired
	return &s3ObjectStore{
		client: s3.NewFromConfig(cfg, func(o *s3.Options) {
			o.ContinueHeaderThresholdBytes = -1
		}),
		bucket: bucket,
		prefix: strings.Trim(os.Getenv("S3_PREFIX"), "/"),
	}
}

func (s *s3ObjectStore) key(key string) string {
	key = strings.TrimLeft(key, "/")
	if s.prefix == "" {
		return key
	}
	return s.prefix + "/" + key
}

func (s *s3ObjectStore) listPrefix() string {
	if s.prefix == "" {
		return ""
	}
	return s.prefix + "/"
}

func (s *s3ObjectStore) ping(ctx context.Context) error {
	_, err := s.client.ListObjectsV2(ctx, &s3.ListObjectsV2Input{
		Bucket:  aws.String(s.bucket),
		Prefix:  aws.String(s.listPrefix()),
		MaxKeys: aws.Int32(1),
	})
	return err
}

func (s *s3ObjectStore) roundTrip(ctx context.Context, key string, body []byte) error {
	fullKey := s.key(key)
	size := int64(len(body))
	if _, err := s.client.PutObject(ctx, &s3.PutObjectInput{
		Bucket:        aws.String(s.bucket),
		Key:           aws.String(fullKey),
		Body:          bytes.NewReader(body),
		ContentType:   aws.String("text/plain"),
		ContentLength: aws.Int64(size),
	}); err != nil {
		return err
	}
	out, err := s.client.GetObject(ctx, &s3.GetObjectInput{
		Bucket: aws.String(s.bucket),
		Key:    aws.String(fullKey),
	})
	if err != nil {
		return err
	}
	defer out.Body.Close()
	got, err := io.ReadAll(out.Body)
	if err != nil {
		return err
	}
	if !bytes.Equal(got, body) {
		return fmt.Errorf("storage body mismatch")
	}
	return nil
}

type redisStore struct {
	client *redis.Client
	prefix string
}

func cacheFromEnv() cacheStore {
	url := strings.TrimSpace(os.Getenv("REDIS_URL"))
	if url == "" {
		return missingCacheStore("REDIS_URL is not set")
	}
	opts, err := redis.ParseURL(url)
	if err != nil {
		return missingCacheStore("parse REDIS_URL: " + err.Error())
	}
	prefix := strings.TrimSpace(os.Getenv("REDIS_KEY_PREFIX"))
	return &redisStore{client: redis.NewClient(opts), prefix: prefix}
}

func (s *redisStore) keyPrefix() string { return s.prefix }

func (s *redisStore) namespaced(key string) string {
	key = strings.TrimLeft(key, "/")
	if s.prefix == "" {
		return key
	}
	return strings.TrimRight(s.prefix, ":") + ":" + key
}

func (s *redisStore) ping(ctx context.Context) error {
	return s.client.Ping(ctx).Err()
}

func (s *redisStore) roundTrip(ctx context.Context, key, value string) error {
	fullKey := s.namespaced(key)
	if err := s.client.Set(ctx, fullKey, value, time.Minute).Err(); err != nil {
		return err
	}
	got, err := s.client.Get(ctx, fullKey).Result()
	if err != nil {
		return err
	}
	if got != value {
		return fmt.Errorf("redis value mismatch")
	}
	if s.prefix != "" && !strings.HasPrefix(fullKey, strings.TrimRight(s.prefix, ":")) {
		return fmt.Errorf("redis key %q missing prefix %q", fullKey, s.prefix)
	}
	return nil
}

type missingDBStore string

func (s missingDBStore) ping(context.Context) error { return errors.New(string(s)) }
func (s missingDBStore) roundTrip(context.Context) (string, error) {
	return "", errors.New(string(s))
}

type missingObjectStore string

func (s missingObjectStore) ping(context.Context) error { return errors.New(string(s)) }
func (s missingObjectStore) roundTrip(context.Context, string, []byte) error {
	return errors.New(string(s))
}

type missingCacheStore string

func (s missingCacheStore) ping(context.Context) error { return errors.New(string(s)) }
func (s missingCacheStore) roundTrip(context.Context, string, string) error {
	return errors.New(string(s))
}
func (s missingCacheStore) keyPrefix() string { return "" }

func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}

func writeDetailedError(w http.ResponseWriter, status int, message string, err error) {
	writeJSON(w, status, map[string]string{
		"error":  message,
		"detail": err.Error(),
	})
}

func checksumOrEmpty(value string) string {
	if value == "" {
		return ""
	}
	sum := sha256.Sum256([]byte(value))
	return hex.EncodeToString(sum[:])
}
