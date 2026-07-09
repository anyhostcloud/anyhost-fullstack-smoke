package main

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

type fakeDB struct {
	pingErr error
	value   string
	rtErr   error
}

func (f *fakeDB) ping(context.Context) error { return f.pingErr }
func (f *fakeDB) roundTrip(context.Context) (string, error) {
	if f.rtErr != nil {
		return "", f.rtErr
	}
	if f.value == "" {
		return "ok", nil
	}
	return f.value, nil
}

type fakeObjects struct {
	pingErr error
	rtErr   error
}

func (f *fakeObjects) ping(context.Context) error { return f.pingErr }
func (f *fakeObjects) roundTrip(context.Context, string, []byte) error {
	return f.rtErr
}

type fakeCache struct {
	pingErr error
	rtErr   error
	prefix  string
}

func (f *fakeCache) ping(context.Context) error { return f.pingErr }
func (f *fakeCache) roundTrip(context.Context, string, string) error {
	return f.rtErr
}
func (f *fakeCache) keyPrefix() string { return f.prefix }

func TestHealth(t *testing.T) {
	handler := newApp(&fakeDB{}, &fakeObjects{}, &fakeCache{})
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/health", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d body = %s", rec.Code, rec.Body.String())
	}
}

func TestReadyRequiresAllResources(t *testing.T) {
	handler := newApp(&fakeDB{}, &fakeObjects{}, &fakeCache{prefix: "ah:w:p:e:r:"})
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/ready", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("healthy ready status = %d body = %s", rec.Code, rec.Body.String())
	}

	handler = newApp(&fakeDB{pingErr: errors.New("down")}, &fakeObjects{}, &fakeCache{})
	rec = httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/ready", nil))
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("unhealthy ready status = %d body = %s", rec.Code, rec.Body.String())
	}
}

func TestComponentEndpoints(t *testing.T) {
	handler := newApp(
		&fakeDB{value: "db-ok"},
		&fakeObjects{},
		&fakeCache{prefix: "ah:demo:"},
	)

	for _, path := range []string{"/db", "/storage", "/redis"} {
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
		if rec.Code != http.StatusOK {
			t.Fatalf("%s status = %d body = %s", path, rec.Code, rec.Body.String())
		}
	}

	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/redis", nil))
	var redisBody map[string]string
	if err := json.Unmarshal(rec.Body.Bytes(), &redisBody); err != nil {
		t.Fatal(err)
	}
	if redisBody["key_prefix"] != "ah:demo:" {
		t.Fatalf("key_prefix = %q", redisBody["key_prefix"])
	}
}

func TestEnvChecksumDoesNotLeakSecret(t *testing.T) {
	t.Setenv("SMOKE_PLAIN_MARKER", "plain-value")
	t.Setenv("SMOKE_SECRET_MARKER", "super-secret")
	t.Setenv("DATABASE_URL", "postgres://example")
	handler := newApp(&fakeDB{}, &fakeObjects{}, &fakeCache{})
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/env", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d body = %s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	if strings.Contains(body, "super-secret") {
		t.Fatalf("secret leaked in /env body: %s", body)
	}
	var payload map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
		t.Fatal(err)
	}
	if payload["plain_marker"] != "plain-value" {
		t.Fatalf("plain_marker = %#v", payload["plain_marker"])
	}
	if payload["secret_marker_present"] != true {
		t.Fatalf("secret_marker_present = %#v", payload["secret_marker_present"])
	}
	if payload["secret_marker_sha256"] == "" || payload["secret_marker_sha256"] == "super-secret" {
		t.Fatalf("unexpected checksum %#v", payload["secret_marker_sha256"])
	}
}

func TestRedisNamespacedKey(t *testing.T) {
	store := &redisStore{prefix: "ah:w:p:e:r"}
	if got := store.namespaced("probe"); got != "ah:w:p:e:r:probe" {
		t.Fatalf("namespaced = %q", got)
	}
}
