package support

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
)

func TestRedact(t *testing.T) {
	cases := map[string]string{
		`--password=hunter2 --rpc.token:abc x`:              `--password=<redacted> --rpc.token:<redacted> x`,
		`Environment="GRAFANA_PASSWORD=secret" User=root`:   `Environment="GRAFANA_PASSWORD=<redacted>" User=root`,
		`Environment=API_KEY=k1 Environment=MNEMONIC=words`: `Environment=API_KEY=<redacted> Environment=MNEMONIC=<redacted>`,
		`plain line without secrets`:                        `plain line without secrets`,
		`--token="abc def" tail`:                            `--token=<redacted> tail`,
		`password: 's3 cr3t' next`:                          `password: <redacted> next`,
		"password:\nnextline":                               "password:\nnextline",
	}
	for in, want := range cases {
		if got := Redact(in); got != want {
			t.Errorf("Redact(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestCrashMarkers(t *testing.T) {
	in := "ok line\nfatal error: runtime: out of memory\nanother ok\nznnd.service: Main process exited, code=killed\nToo Many Open Files\n"
	var out bytes.Buffer
	if err := FilterCrashMarkers(strings.NewReader(in), &out); err != nil {
		t.Fatal(err)
	}
	got := out.String()
	if strings.Contains(got, "ok line") || !strings.Contains(got, "out of memory") || !strings.Contains(got, "Main process exited") || !strings.Contains(got, "Too Many Open Files") {
		t.Errorf("markers:\n%s", got)
	}
}

func TestRedactPeers(t *testing.T) {
	in := []byte(`{"numPeers":2,"peers":[{"publicKey":"a","ip":"1.2.3.4","name":"x"},{"publicKey":"b","ip":"5.6.7.8","name":"y"}],"self":{"publicKey":"s","ip":"9.9.9.9","name":"*self*"}}`)
	out, err := RedactPeers(in)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(out, []byte("1.2.3.4")) || bytes.Contains(out, []byte("9.9.9.9")) {
		t.Errorf("ip leaked: %s", out)
	}
	var v struct {
		NumPeers int
		Peers    []map[string]string
		Self     map[string]string
	}
	if err := json.Unmarshal(out, &v); err != nil || v.NumPeers != 2 || len(v.Peers) != 2 || v.Peers[0]["ip"] != "<redacted>" || v.Peers[0]["publicKey"] != "a" || v.Self["ip"] != "<redacted>" {
		t.Errorf("redacted doc wrong: %s (%v)", out, err)
	}
	if _, err := RedactPeers([]byte("{bad")); err == nil {
		t.Error("invalid JSON must error")
	}
}

func TestRedactStructuredCredentials(t *testing.T) {
	input := "startup config: {\n  \"Producer\": {\n    \"Password\": \"example-credential\\\"-suffix\",\n    \"Name\": \"example-node\"\n  },\n  \"Token\":\n    \"example-token\"\n}\nAuthorization: Bearer example-bearer\nProxy-Authorization: Basic example-basic\nauthorization = Digest username=example, response=example-response\n"
	out := Redact(input)
	for _, secret := range []string{"example-credential", "-suffix", "example-token", "example-bearer", "example-basic", "example-response"} {
		if strings.Contains(out, secret) {
			t.Errorf("credential fixture was retained: %s", secret)
		}
	}
	if !strings.Contains(out, "example-node") || !strings.Contains(out, `"Password": "<redacted>"`) {
		t.Errorf("expected redacted structured record: %s", out)
	}
	var doc any
	if err := json.Unmarshal([]byte(Redact(`{"Password":"example","Authorization":"Bearer example","port":1234}`)), &doc); err != nil {
		t.Fatalf("redacted JSON should remain valid: %v", err)
	}
}
