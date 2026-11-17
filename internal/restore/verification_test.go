package restore

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"os"
	"testing"
)

func TestInspectionRejectsAmbiguousAndUnsafeArchives(t *testing.T) {
	for name, entries := range map[string][]entry{
		"absolute allowed folder": {file("/nom/a", "x")},
		"normalized traversal":    {file("nom/../nom/a", "x")},
		"root link":               {dir("nom/"), {name: ".", typ: tar.TypeSymlink, link: "/tmp"}},
		"duplicate file":          {file("nom/a", "x"), file("./nom/a", "y")},
		"file then child":         {file("nom/a", "x"), file("nom/a/b", "y")},
		"child then file":         {file("nom/a/b", "y"), file("nom/a", "x")},
	} {
		t.Run(name, func(t *testing.T) {
			archive := tgz(t, entries...)
			if _, err := Inspect(archive); err == nil {
				t.Fatal("accepted invalid archive")
			}
			if err := Extract(archive, t.TempDir()); err == nil {
				t.Fatal("extracted invalid archive")
			}
		})
	}
}

func TestInspectionConsumesGzipTrailerAndTrailingData(t *testing.T) {
	for _, kind := range []string{"crc", "truncated", "trailing tar", "trailing zeros"} {
		t.Run(kind, func(t *testing.T) {
			a := tgz(t, good()...)
			data, err := os.ReadFile(a)
			if err != nil {
				t.Fatal(err)
			}
			switch kind {
			case "crc":
				data[len(data)-8] ^= 0xff
			case "truncated":
				data = data[:len(data)-1]
			case "trailing tar":
				data = append(data, data...)
			case "trailing zeros":
				var padding bytes.Buffer
				gz := gzip.NewWriter(&padding)
				if _, err := gz.Write(make([]byte, 4096)); err != nil {
					t.Fatal(err)
				}
				if err := gz.Close(); err != nil {
					t.Fatal(err)
				}
				data = append(data, padding.Bytes()...)
			}
			if err := os.WriteFile(a, data, 0o600); err != nil {
				t.Fatal(err)
			}
			_, err = Inspect(a)
			if (err == nil) != (kind == "trailing zeros") {
				t.Fatalf("%s: %v", kind, err)
			}
		})
	}
}
