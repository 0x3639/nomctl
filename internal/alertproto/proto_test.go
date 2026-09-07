package alertproto

import (
	"errors"
	"testing"
	"time"
)

func TestSignVerify(t *testing.T) {
	secret := []byte("s3cret")
	body := []byte(`{"alert":"service_down"}`)
	now := time.Unix(1700000000, 0)
	sig := Sign(secret, now.Unix(), body)
	if err := Verify(secret, now.Unix(), body, sig, now); err != nil {
		t.Fatalf("valid signature rejected: %v", err)
	}
	if err := Verify(secret, now.Unix(), []byte(`{"alert":"ok"}`), sig, now); !errors.Is(err, ErrBadSignature) {
		t.Errorf("tampered body: %v", err)
	}
	if err := Verify([]byte("other"), now.Unix(), body, sig, now); !errors.Is(err, ErrBadSignature) {
		t.Errorf("wrong secret: %v", err)
	}
	if err := Verify(secret, now.Unix(), body, "zz", now); !errors.Is(err, ErrBadSignature) {
		t.Errorf("garbage signature: %v", err)
	}
	old := now.Add(-6 * time.Minute)
	if err := Verify(secret, old.Unix(), body, Sign(secret, old.Unix(), body), now); !errors.Is(err, ErrStale) {
		t.Errorf("stale timestamp: %v", err)
	}
	future := now.Add(6 * time.Minute)
	if err := Verify(secret, future.Unix(), body, Sign(secret, future.Unix(), body), now); !errors.Is(err, ErrStale) {
		t.Errorf("future timestamp: %v", err)
	}
	if Sign(secret, 1, body) == Sign(secret, 2, body) {
		t.Error("timestamp must be part of the signature")
	}
}

func TestLookup(t *testing.T) {
	a, ok := Lookup("service_down")
	if !ok || a.Severity != Critical || a.Title == "" || a.OKTitle == "" {
		t.Errorf("service_down = %+v, %v", a, ok)
	}
	if _, ok := Lookup("nope"); ok {
		t.Error("unknown alert must not be found")
	}
	seen := map[string]bool{}
	for _, a := range Alerts {
		if a.Name == "" || a.Title == "" || a.OKTitle == "" || a.Severity == "" {
			t.Errorf("incomplete alert %+v", a)
		}
		if seen[a.Name] {
			t.Errorf("duplicate alert %s", a.Name)
		}
		seen[a.Name] = true
	}
	if !seen["node_silent"] {
		t.Error("node_silent missing")
	}
	if len(Alerts) != 15 {
		t.Errorf("expected 15 alerts, got %d", len(Alerts))
	}
}
