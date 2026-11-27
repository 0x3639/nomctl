package orchestrator

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/0x3639/nomctl/internal/nodeconfig"
)

func TestSyncTemplatesApplyPerNetworkAndPreserveConfig(t *testing.T) {
	dir := t.TempDir()
	path := ConfigPath(dir)
	initial := `{
    "DataPath": "/root/.orchestrator",
    "GlobalState": 0,
    "ProducerKeyFileName": "producer",
    "ProducerKeyFilePassphrase": "top-secret-password",
    "Networks": {
        "BNB Chain": {
            "Urls": [
                "wss://bnb-mainnet.g.alchemy.com/v2/my-bnb-key"
            ],
            "FilterQuerySize": 100,
            "RpcRequestsPerSecond": 2,
            "RpcBurst": 3
        },
        "BSC": {
            "Urls": [
                "ws://127.0.0.1:8545"
            ],
            "FilterQuerySize": 999,
            "RpcRequestsPerSecond": 9,
            "RpcBurst": 9
        },
        "Ethereum": {
            "Urls": [
                "wss://eth-mainnet.g.alchemy.com/v2/my-eth-key"
            ],
            "FilterQuerySize": 100,
            "RpcRequestsPerSecond": 2,
            "RpcBurst": 3
        },
        "Supernova": {
            "Urls": [
                "wss://rpc.novascan.io"
            ],
            "FilterQuerySize": 2000,
            "RpcRequestsPerSecond": 0,
            "RpcBurst": 0
        },
        "Zenon": {
            "Urls": [
                "ws://127.0.0.1:35998"
            ]
        }
    },
    "TssConfig": {
        "Port": 55055,
        "PublicKey": "keep-this-key"
    }
}
`
	if err := os.WriteFile(path, []byte(initial), 0o600); err != nil {
		t.Fatal(err)
	}

	now := time.Unix(1700000000, 0)
	res, err := ApplySyncTemplates(path, map[string]string{
		"bnb": "drpc.org",
		"eth": "drpc",
	}, now)
	if err != nil {
		t.Fatal(err)
	}
	if res.BackupPath == "" {
		t.Fatal("expected config backup path")
	}
	if _, err := os.Stat(res.BackupPath); err != nil {
		t.Fatalf("backup file missing: %v", err)
	}

	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var doc struct {
		ProducerKeyFilePassphrase string `json:"ProducerKeyFilePassphrase"`
		TssConfig                 struct {
			PublicKey string `json:"PublicKey"`
		} `json:"TssConfig"`
		Networks map[string]struct {
			Urls                 []string `json:"Urls"`
			FilterQuerySize      uint64   `json:"FilterQuerySize"`
			RpcRequestsPerSecond float64  `json:"RpcRequestsPerSecond"`
			RpcBurst             int      `json:"RpcBurst"`
		} `json:"Networks"`
	}
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatal(err)
	}

	// dRPC BNB Chain settings.
	bnb := doc.Networks["BNB Chain"]
	if bnb.FilterQuerySize != 2000 || bnb.RpcRequestsPerSecond != 0 || bnb.RpcBurst != 0 {
		t.Fatalf("BNB Chain dRPC settings = %+v", bnb)
	}
	if len(bnb.Urls) != 1 || bnb.Urls[0] != "wss://bnb-mainnet.g.alchemy.com/v2/my-bnb-key" {
		t.Fatalf("BNB Chain Urls were altered: %+v", bnb.Urls)
	}
	// dRPC Ethereum settings.
	eth := doc.Networks["Ethereum"]
	if eth.FilterQuerySize != 500 || eth.RpcRequestsPerSecond != 0 || eth.RpcBurst != 0 {
		t.Fatalf("Ethereum dRPC settings = %+v", eth)
	}
	if len(eth.Urls) != 1 || eth.Urls[0] != "wss://eth-mainnet.g.alchemy.com/v2/my-eth-key" {
		t.Fatalf("Ethereum Urls were altered: %+v", eth.Urls)
	}
	// Unrelated BSC / Supernova / TssConfig / Producer must remain untouched.
	if doc.Networks["BSC"].FilterQuerySize != 999 {
		t.Fatalf("unused BSC stub should remain untouched: %+v", doc.Networks["BSC"])
	}
	if doc.Networks["Supernova"].FilterQuerySize != 2000 || doc.TssConfig.PublicKey != "keep-this-key" || doc.ProducerKeyFilePassphrase != "top-secret-password" {
		t.Fatal("unrelated config fields were modified")
	}

	// Now apply Alchemy to Ethereum only, keeping BNB Chain on dRPC!
	if _, err := ApplySyncTemplates(path, map[string]string{
		NetworkEthereum: ProviderAlchemy,
	}, now.Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	raw, _ = os.ReadFile(path)
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatal(err)
	}
	if doc.Networks["Ethereum"].FilterQuerySize != 10 || doc.Networks["Ethereum"].RpcRequestsPerSecond != 0.5 || doc.Networks["Ethereum"].RpcBurst != 1 {
		t.Fatalf("Ethereum Alchemy settings = %+v", doc.Networks["Ethereum"])
	}
	if doc.Networks["BNB Chain"].FilterQuerySize != 2000 || doc.Networks["BNB Chain"].RpcRequestsPerSecond != 0 || doc.Networks["BNB Chain"].RpcBurst != 0 {
		t.Fatalf("BNB Chain settings should stay dRPC: %+v", doc.Networks["BNB Chain"])
	}

	// Apply Alchemy to BNB Chain too.
	if _, err := ApplySyncTemplates(path, map[string]string{
		NetworkBNBChain: ProviderAlchemy,
	}, now.Add(2*time.Second)); err != nil {
		t.Fatal(err)
	}
	raw, _ = os.ReadFile(path)
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatal(err)
	}
	if doc.Networks["BNB Chain"].FilterQuerySize != 10 || doc.Networks["BNB Chain"].RpcRequestsPerSecond != 0.5 || doc.Networks["BNB Chain"].RpcBurst != 1 {
		t.Fatalf("BNB Chain Alchemy settings = %+v", doc.Networks["BNB Chain"])
	}
}

func TestConfigInvalidEditPreservesOldFile(t *testing.T) {
	dir := t.TempDir()
	path := ConfigPath(dir)
	original := []byte("{\n    \"DataPath\": \"/root/.orchestrator\"\n}\n")
	if err := os.WriteFile(path, original, 0o600); err != nil {
		t.Fatal(err)
	}

	// Direct SaveConfig refuses invalid JSON and negative rates.
	for _, bad := range []string{
		"{not json",
		`{"Networks":{"Ethereum":{"RpcRequestsPerSecond":-1}}}`,
		`{"Networks":{"Ethereum":{"FilterQuerySize":-5}}}`,
		`{"Networks":{"Ethereum":{"RpcBurst":-2}}}`,
		`{"TssConfig":{"Port":70000}}`,
	} {
		if _, err := SaveConfig(path, []byte(bad), time.Unix(100, 0)); err == nil {
			t.Errorf("SaveConfig should reject %s", bad)
		}
	}
	if got, _ := os.ReadFile(path); string(got) != string(original) {
		t.Fatal("invalid save modified original config.json")
	}

	// EditConfig retries on invalid edit and aborts cleanly when user says No.
	retries := 0
	outcome, _, err := EditConfig(path,
		func(p string) error { return os.WriteFile(p, []byte("{bad"), 0o600) },
		func(problem error) (bool, error) { retries++; return false, nil },
		time.Unix(100, 0),
	)
	if err != nil || outcome != nodeconfig.EditAborted || retries != 1 {
		t.Fatalf("outcome=%v retries=%d err=%v", outcome, retries, err)
	}
	if got, _ := os.ReadFile(path); string(got) != string(original) {
		t.Fatal("aborted edit modified original config.json")
	}

	// Valid edit saves atomically with 0600 permissions and creates backup.
	updated := []byte("{\n    \"DataPath\": \"/root/.orchestrator\",\n    \"ProducerIndex\": 1\n}\n")
	outcome, backupPath, err := EditConfig(path,
		func(p string) error { return os.WriteFile(p, updated, 0o600) },
		func(error) (bool, error) { t.Fatal("unexpected retry"); return false, nil },
		time.Unix(200, 0),
	)
	if err != nil || outcome != nodeconfig.EditSaved || backupPath == "" {
		t.Fatalf("outcome=%v backup=%q err=%v", outcome, backupPath, err)
	}
	if got, _ := os.ReadFile(path); string(got) != string(updated) {
		t.Fatalf("updated config not saved: %s", got)
	}
	if st, err := os.Stat(path); err != nil || st.Mode().Perm() != 0o600 {
		t.Fatalf("config mode = %v, err = %v", st, err)
	}
	if bak, _ := os.ReadFile(backupPath); string(bak) != string(original) {
		t.Fatal("backup file does not match original")
	}
}

func TestRedactConfigJSONIsIdempotentAndMasksSecrets(t *testing.T) {
	raw := []byte(`{
    "DataPath": "/root/.orchestrator",
    "ProducerKeyFilePassphrase": "super-secret-passphrase",
    "Networks": {
        "Ethereum": {
            "Urls": [
                "wss://user:pass@eth-mainnet.g.alchemy.com/v2/secret-api-key?token=xyz"
            ],
            "FilterQuerySize": 500,
            "RpcRequestsPerSecond": 0,
            "RpcBurst": 0
        },
        "Zenon": {
            "Urls": ["ws://127.0.0.1:35998"]
        }
    }
}`)
	redacted, err := RedactConfigJSON(raw)
	if err != nil {
		t.Fatal(err)
	}
	text := string(redacted)
	for _, leak := range []string{"super-secret-passphrase", "secret-api-key", "token=xyz", "user:pass"} {
		if strings.Contains(text, leak) {
			t.Errorf("redacted output leaked %q:\n%s", leak, text)
		}
	}
	if !strings.Contains(text, "ws://127.0.0.1:35998") || !strings.Contains(text, "eth-mainnet.g.alchemy.com") {
		t.Errorf("public host details missing:\n%s", text)
	}
	again, err := RedactConfigJSON(redacted)
	if err != nil {
		t.Fatal(err)
	}
	if string(again) != text {
		t.Fatalf("redaction is not idempotent:\nfirst:\n%s\nsecond:\n%s", text, string(again))
	}
}

func TestConfigRefusesSymlink(t *testing.T) {
	dir := t.TempDir()
	secret := filepath.Join(dir, "other")
	if err := os.WriteFile(secret, []byte("{}"), 0o600); err != nil {
		t.Fatal(err)
	}
	link := ConfigPath(dir)
	if err := os.Symlink(secret, link); err != nil {
		t.Fatal(err)
	}
	if _, err := ReadConfig(link); err == nil {
		t.Fatal("ReadConfig should reject symlink")
	}
	if _, err := SaveConfig(link, []byte("{}\n"), time.Now()); err == nil {
		t.Fatal("SaveConfig should reject symlink")
	}
}
