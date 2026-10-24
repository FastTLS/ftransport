package ftransport

import (
	"bufio"
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestXNetSnapshotManifest(t *testing.T) {
	t.Parallel()
	_, sourceFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("cannot locate test source")
	}
	root := filepath.Join(filepath.Dir(sourceFile), "third_party", "xnet")
	manifest, err := os.Open(filepath.Join(root, "MANIFEST.sha256"))
	if err != nil {
		t.Fatal(err)
	}
	defer manifest.Close()

	scanner := bufio.NewScanner(manifest)
	checked := 0
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) != 3 {
			t.Fatalf("invalid manifest line: %q", line)
		}
		want, err := hex.DecodeString(fields[1])
		if err != nil {
			t.Fatalf("invalid local hash for %s: %v", fields[2], err)
		}
		content, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(fields[2])))
		if err != nil {
			t.Fatal(err)
		}
		got := sha256.Sum256(content)
		if !equalBytes(got[:], want) {
			t.Fatalf("snapshot hash changed for %s: got %x, want %x", fields[2], got, want)
		}
		checked++
	}
	if err := scanner.Err(); err != nil {
		t.Fatal(err)
	}
	if checked != 65 {
		t.Fatalf("checked %d snapshot files, want 65", checked)
	}
}

func TestXNetProductionCopyMatchesSnapshot(t *testing.T) {
	t.Parallel()
	_, sourceFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("cannot locate test source")
	}
	repository := filepath.Dir(sourceFile)
	snapshot := filepath.Join(repository, "third_party", "xnet")
	production := filepath.Join(repository, "internal", "xnet")
	prefixes := []string{"http/", "http2/", "idna/", "internal/httpcommon/", "internal/httpsfv/"}
	err := filepath.WalkDir(snapshot, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() {
			return nil
		}
		relative, err := filepath.Rel(snapshot, path)
		if err != nil {
			return err
		}
		relative = filepath.ToSlash(relative)
		selected := false
		for _, prefix := range prefixes {
			if strings.HasPrefix(relative, prefix) {
				selected = true
				break
			}
		}
		if !selected {
			return nil
		}
		upstream, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		want := strings.ReplaceAll(string(upstream), "golang.org/x/net/", "github.com/FastTLS/ftransport/internal/xnet/")
		got, err := os.ReadFile(filepath.Join(production, filepath.FromSlash(relative)))
		if err != nil {
			return err
		}
		if string(got) != want {
			t.Errorf("production x/net copy diverged from audited snapshot: %s", relative)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

func equalBytes(left, right []byte) bool {
	if len(left) != len(right) {
		return false
	}
	for index := range left {
		if left[index] != right[index] {
			return false
		}
	}
	return true
}
