package support

import (
	"bytes"
	"compress/gzip"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestListLogsNewestFirstAndDepth(t *testing.T) {
	dir := t.TempDir()
	deep := filepath.Join(dir, "a", "b", "c", "d")
	if err := os.MkdirAll(deep, 0o755); err != nil {
		t.Fatal(err)
	}
	write := func(p string, age time.Duration) {
		if err := os.WriteFile(p, []byte("log"), 0o644); err != nil {
			t.Fatal(err)
		}
		mt := time.Now().Add(-age)
		_ = os.Chtimes(p, mt, mt)
	}
	write(filepath.Join(dir, "old.log"), 2*time.Hour)
	write(filepath.Join(dir, "new.log"), time.Minute)
	write(filepath.Join(dir, "a", "b", "mid.log"), time.Hour)
	write(filepath.Join(deep, "toodeep.log"), 0)
	logs, err := ListLogs(dir)
	if err != nil || len(logs) != 3 || filepath.Base(logs[0].Path) != "new.log" || filepath.Base(logs[2].Path) != "old.log" {
		t.Errorf("logs = %+v, %v", logs, err)
	}
	if _, err := ListLogs(filepath.Join(dir, "missing")); err == nil {
		t.Error("missing dir must error")
	}
}

func TestTailBytesBounded(t *testing.T) {
	// 1 MiB stream, 100-byte tail: the result must be the last 100 bytes.
	src := bytes.Repeat([]byte("0123456789"), 100*1024)
	src = append(src, []byte("THE-END")...)
	got, err := tailBytes(bytes.NewReader(src), 100)
	if err != nil || len(got) != 100 || !bytes.HasSuffix(got, []byte("THE-END")) {
		t.Errorf("tailBytes = %d bytes, %v", len(got), err)
	}
	// The working buffer may hold one extra read chunk, never the whole stream.
	if cap(got) > 100+2*(64<<10) {
		t.Errorf("buffer grew beyond the bound: cap %d", cap(got))
	}
}

func TestTailLog(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "znnd.log")
	if err := os.WriteFile(src, bytes.Repeat([]byte("x"), 100), 0o644); err != nil {
		t.Fatal(err)
	}
	dst := filepath.Join(dir, "out.tail")
	if err := TailLog(src, dst, 10); err != nil {
		t.Fatal(err)
	}
	if data, _ := os.ReadFile(dst); !strings.Contains(string(data), "truncated") || bytes.Contains(data, []byte("xxxxx")) {
		t.Errorf("partial log record retained: %q", data)
	}
	gz := filepath.Join(dir, "old.log.gz")
	f, _ := os.Create(gz)
	w := gzip.NewWriter(f)
	_, _ = w.Write([]byte("hello gzip\nworld\n"))
	_ = w.Close()
	_ = f.Close()
	dst = filepath.Join(dir, "gzip.tail")
	if err := TailLog(gz, dst, 7); err != nil {
		t.Fatal(err)
	}
	if data, _ := os.ReadFile(dst); !strings.HasSuffix(string(data), "world\n") {
		t.Errorf("gz tail = %q", data)
	}
}

func TestLogTailRedactionAndCancellation(t *testing.T) {
	path := filepath.Join(t.TempDir(), "node.log")
	input := strings.Repeat("old record\n", 20000) + "Password=example-log-value\nlatest record\n"
	if err := os.WriteFile(path, []byte(input), 0o600); err != nil {
		t.Fatal(err)
	}
	out := filepath.Join(t.TempDir(), "tail.log")
	if err := TailLog(path, out, 100); err != nil {
		t.Fatal(err)
	}
	data, _ := os.ReadFile(out)
	if strings.Contains(string(data), "example-log-value") || !strings.Contains(string(data), "latest record") || len(data) > 200 {
		t.Fatalf("unexpected bounded log tail: %q", data)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := readLogTail(ctx, path, 100); err == nil {
		t.Fatal("cancelled log read succeeded")
	}
}

func TestLogTailOmitsLeadingStructuredFragment(t *testing.T) {
	path := filepath.Join(t.TempDir(), "node.log")
	fragment := "\"Password\":\n  \"example-fragment-value\"\n}\nINFO latest record\n"
	if err := os.WriteFile(path, []byte(strings.Repeat("previous record\n", 100)+fragment), 0o600); err != nil {
		t.Fatal(err)
	}
	data, err := readLogTail(context.Background(), path, int64(len(fragment)))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), "example-fragment-value") || !strings.Contains(string(data), "INFO latest record") {
		t.Fatalf("unexpected structured tail: %q", data)
	}
}
