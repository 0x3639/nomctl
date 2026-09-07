package producer

import (
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/0x3639/nomctl/internal/config"
)

func testCfg(t *testing.T) config.Config {
	t.Helper()
	return config.Config{ServiceName: "go-zenon", ZnnDir: t.TempDir()}
}

func stubHost(t *testing.T, active bool) *[]string {
	t.Helper()
	var calls []string
	oldActive, oldRestart := isActive, restart
	isActive = func(string) bool { return active }
	restart = func(n string) error { calls = append(calls, "restart "+n); return nil }
	t.Cleanup(func() { isActive, restart = oldActive, oldRestart })
	return &calls
}

func TestSetupCreatesKeyAndConfig(t *testing.T) {
	cfg := testCfg(t)
	calls := stubHost(t, true)
	res, err := Setup(cfg, Options{Now: func() time.Time { return time.Unix(1_800_000_000, 0) }})
	if err != nil {
		t.Fatal(err)
	}
	if !res.Created || res.Password == "" || !strings.HasPrefix(res.Address, "z1") || !res.Restarted {
		t.Fatalf("result = %+v", res)
	}
	pc, err := ReadConfig(ConfigPath(cfg.ZnnDir))
	if err != nil || pc == nil || pc.Address != res.Address || pc.Password != res.Password || pc.KeyFilePath != "producer" || pc.Index != 0 {
		t.Fatalf("config = %+v, %v", pc, err)
	}
	if got, err := Verify(res.KeyFile, res.Password); err != nil || got != res.Address {
		t.Errorf("key does not open with the stored password: %v", err)
	}
	if strings.Join(*calls, ",") != "restart go-zenon" {
		t.Errorf("calls = %v", *calls)
	}
	// Second run keeps the configuration without prompts, touches nothing.
	res2, err := Setup(cfg, Options{})
	if err != nil || res2.Created || res2.Configured || res2.Address != res.Address || res2.Password != "" {
		t.Fatalf("second run: %+v, %v", res2, err)
	}
	if len(*calls) != 1 {
		t.Errorf("second run restarted: %v", *calls)
	}
}

func TestSetupConfiguresExistingKey(t *testing.T) {
	cfg := testCfg(t)
	stubHost(t, false)
	addr, err := Create(KeyFilePath(cfg.ZnnDir), "known")
	if err != nil {
		t.Fatal(err)
	}
	// Non-interactive without a password: refuse.
	if _, err := Setup(cfg, Options{}); err == nil || !strings.Contains(err.Error(), "--password") {
		t.Fatalf("expected a --password hint, got %v", err)
	}
	// Prompted: two wrong answers, then right.
	attempts := 0
	res, err := Setup(cfg, Options{Prompts: Prompts{PasswordFor: func(_ string, attempt int) (string, error) {
		attempts = attempt
		if attempt < 3 {
			return "wrong", nil
		}
		return "known\n", nil
	}}})
	if err != nil || !res.Configured || res.Created || res.Address != addr || res.Password != "" || res.Restarted {
		t.Fatalf("result = %+v, %v (attempts %d)", res, err, attempts)
	}
	if pc, _ := ReadConfig(ConfigPath(cfg.ZnnDir)); pc == nil || pc.Password != "known" {
		t.Errorf("config = %+v", pc)
	}
	// Three wrong answers abort without writing.
	cfg2 := testCfg(t)
	if _, err := Create(KeyFilePath(cfg2.ZnnDir), "x"); err != nil {
		t.Fatal(err)
	}
	_, err = Setup(cfg2, Options{Prompts: Prompts{PasswordFor: func(string, int) (string, error) { return "nope", nil }}})
	if err == nil || !strings.Contains(err.Error(), "three times") {
		t.Fatalf("err = %v", err)
	}
	if _, statErr := os.Stat(ConfigPath(cfg2.ZnnDir)); !errors.Is(statErr, os.ErrNotExist) {
		t.Error("config written despite failure")
	}
	// --password verifies directly.
	cfg3 := testCfg(t)
	if _, err := Create(KeyFilePath(cfg3.ZnnDir), "pw3"); err != nil {
		t.Fatal(err)
	}
	if _, err := Setup(cfg3, Options{Password: "bad"}); !errors.Is(err, ErrWrongPassword) {
		t.Errorf("bad --password: %v", err)
	}
	if res, err := Setup(cfg3, Options{Password: "pw3"}); err != nil || !res.Configured {
		t.Errorf("good --password: %+v, %v", res, err)
	}
}

func TestSetupReplaceExistingWhenAsked(t *testing.T) {
	cfg := testCfg(t)
	stubHost(t, false)
	first, err := Setup(cfg, Options{Password: "one"})
	if err != nil {
		t.Fatal(err)
	}
	// Decline to keep: the key file exists, so it is re-verified, not replaced.
	asked := ""
	res, err := Setup(cfg, Options{Password: "one", Prompts: Prompts{KeepExisting: func(addr string) (bool, error) { asked = addr; return false, nil }}})
	if err != nil || asked != first.Address || !res.Configured || res.Address != first.Address {
		t.Fatalf("result = %+v, %v, asked %q", res, err, asked)
	}
	backups, _ := os.ReadDir(cfg.ZnnDir)
	n := 0
	for _, e := range backups {
		if strings.HasPrefix(e.Name(), "config.json.bak.") {
			n++
		}
	}
	if n != 1 {
		t.Errorf("expected one config backup, found %d", n)
	}
}
