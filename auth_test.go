package golens

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
)

func TestExpandEnv(t *testing.T) {
	os.Setenv("GOLENS_TEST_VAR", "supersecret")
	defer os.Unsetenv("GOLENS_TEST_VAR")

	if got := expandEnv("env:GOLENS_TEST_VAR"); got != "supersecret" {
		t.Errorf("env: form = %q, want supersecret", got)
	}
	if got := expandEnv("plaintext"); got != "plaintext" {
		t.Errorf("literal = %q, want plaintext", got)
	}
	if got := expandEnv(""); got != "" {
		t.Errorf("empty = %q, want empty", got)
	}
	// missing variable resolves to empty
	if got := expandEnv("env:GOLENS_NOPE_MISSING"); got != "" {
		t.Errorf("missing env = %q, want empty", got)
	}
}

func TestAuthInactiveByDefault(t *testing.T) {
	r, err := New(DefaultConfig())
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	if r.auth.active() {
		t.Error("auth should be inactive by default")
	}
}

func TestAuthActiveWithPassword(t *testing.T) {
	cfg := DefaultConfig()
	cfg.UI.Auth.Username = "admin"
	cfg.UI.Auth.Password = "s3cret"
	r, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()

	if !r.auth.active() {
		t.Fatal("auth should be active with username+password")
	}
	// plaintext must be wiped from the Registry's retained config after hashing
	if r.cfg.UI.Auth.Password != "" {
		t.Error("plaintext password was not wiped after load")
	}
	if len(r.auth.hash) == 0 {
		t.Error("no hash stored")
	}
}

func TestAuthActiveWithPasswordHash(t *testing.T) {
	cfg := DefaultConfig()
	cfg.UI.Auth.Username = "admin"
	cfg.UI.Auth.PasswordHash = "$2a$10$abcdefghijklmnopqrstuvOO7nNnNnNnNnNnNnNnNnNnNnNnNnNnN"
	r, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	if !r.auth.active() {
		t.Error("auth should be active with a password_hash")
	}
}

func TestAuthEnvPassword(t *testing.T) {
	os.Setenv("GOLENS_ADMIN_PASSWORD", "envpass")
	defer os.Unsetenv("GOLENS_ADMIN_PASSWORD")

	cfg := DefaultConfig()
	cfg.UI.Auth.Username = "admin"
	cfg.UI.Auth.Password = "env:GOLENS_ADMIN_PASSWORD"
	r, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	if !r.auth.active() {
		t.Fatal("auth should be active with env-resolved password")
	}
}

// A supplied credential that cannot be honored is a startup error, never a
// silent fallback to an unauthenticated dashboard.
func TestAuthRequiresUsername(t *testing.T) {
	tests := []struct {
		name     string
		password string
		hash     string
	}{
		{name: "password without username", password: "s3cret"},
		{name: "password_hash without username", hash: "$2a$10$abcdefghijklmnopqrstuvOO7nNnNnNnNnNnNnNnNnNnNnNnNnNnN"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			cfg := DefaultConfig()
			cfg.UI.Auth.Password = tc.password
			cfg.UI.Auth.PasswordHash = tc.hash
			r, err := New(cfg)
			if err == nil {
				t.Fatal("credential without username must fail startup, not deactivate auth silently")
			}
			if !strings.Contains(err.Error(), "username is required") {
				t.Errorf("error = %q, want username-required message", err)
			}
			if r != nil {
				t.Error("registry must be nil when auth resolution fails")
			}
		})
	}
}

// A plaintext password bcrypt cannot hash (over its 72-byte limit) is a
// startup error, not a silent deactivation of the configured auth.
func TestAuthRejectsUnhashablePassword(t *testing.T) {
	cfg := DefaultConfig()
	cfg.UI.Auth.Username = "admin"
	cfg.UI.Auth.Password = strings.Repeat("x", 100)
	r, err := New(cfg)
	if err == nil {
		t.Fatal("over-limit password must fail startup")
	}
	if r != nil {
		t.Error("registry must be nil when auth resolution fails")
	}
}

func TestBasicAuthRejectsMissing(t *testing.T) {
	withAuthServer(t, "admin", "s3cret", func(srv *httptest.Server) {
		resp, err := http.Get(srv.URL + "/metrics")
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusUnauthorized {
			t.Errorf("no creds: status = %d, want 401", resp.StatusCode)
		}
		if resp.Header.Get("WWW-Authenticate") == "" {
			t.Error("missing WWW-Authenticate challenge")
		}
	})
}

func TestBasicAuthRejectsWrong(t *testing.T) {
	withAuthServer(t, "admin", "s3cret", func(srv *httptest.Server) {
		req, _ := http.NewRequest("GET", srv.URL+"/metrics", nil)
		req.SetBasicAuth("admin", "wrong")
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusUnauthorized {
			t.Errorf("wrong pass: status = %d, want 401", resp.StatusCode)
		}
	})
}

func TestBasicAuthAcceptsCorrect(t *testing.T) {
	withAuthServer(t, "admin", "s3cret", func(srv *httptest.Server) {
		req, _ := http.NewRequest("GET", srv.URL+"/metrics", nil)
		req.SetBasicAuth("admin", "s3cret")
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			t.Errorf("correct creds: status = %d, want 200", resp.StatusCode)
		}
		if !strings.Contains(resp.Header.Get("Content-Type"), "text/html") {
			t.Errorf("expected dashboard HTML, got content-type %q", resp.Header.Get("Content-Type"))
		}
	})
}

func TestBasicAuthConstantTimeUsername(t *testing.T) {
	// sanity: a wrong username (not just wrong password) is rejected
	withAuthServer(t, "admin", "s3cret", func(srv *httptest.Server) {
		req, _ := http.NewRequest("GET", srv.URL+"/metrics", nil)
		req.SetBasicAuth("root", "s3cret")
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusUnauthorized {
			t.Errorf("wrong user: status = %d, want 401", resp.StatusCode)
		}
	})
}

// withAuthServer builds a Registry with declarative basic auth, mounts the UI,
// and runs fn against a live test server. The password is hashed on load.
func withAuthServer(t *testing.T, user, pass string, fn func(*httptest.Server)) {
	t.Helper()
	cfg := DefaultConfig()
	cfg.UI.Auth.Username = user
	cfg.UI.Auth.Password = pass
	r, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	r.Start(ctx)
	defer func() { cancel(); r.Close() }()

	mux := http.NewServeMux()
	r.MountUI(mux)
	srv := httptest.NewServer(mux)
	defer srv.Close()
	fn(srv)
}
