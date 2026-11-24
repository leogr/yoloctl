package terminal

import (
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

// cacheEntries lists the compiled-core files wazero wrote under dir's build key.
func cacheEntries(t *testing.T, dir string) []string {
	t.Helper()
	files, err := filepath.Glob(filepath.Join(dir, buildKey(), "wazero-*", "*"))
	if err != nil {
		t.Fatal(err)
	}
	return files
}

// frameOf renders the same bytes on a terminal of core c.
func frameOf(t *testing.T, c *compiledCore) Frame {
	t.Helper()
	term, err := newOn(c, 12, 40, 100, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = term.Close() }()
	if _, err := term.Write([]byte("\x1b[1;31mred\x1b[0m plain\r\n\x1b[2;5Hmoved é \U0001f600\r\nline three")); err != nil {
		t.Fatal(err)
	}
	f, err := term.Frame(false)
	if err != nil {
		t.Fatal(err)
	}
	return f
}

func TestCompilationCacheColdWarmAndInMemoryRenderAlike(t *testing.T) {
	dir := cacheTestDir(t)
	cold, ok := compileCached(dir)
	if !ok {
		t.Fatal("cold compile through the cache failed")
	}
	if n := len(cacheEntries(t, dir)); n != 1 {
		t.Fatalf("cold compile wrote %d cache entries, want 1", n)
	}
	warm, ok := compileCached(dir)
	if !ok {
		t.Fatal("warm load from the cache failed")
	}
	memory, err := buildCore(runtimeConfig())
	if err != nil {
		t.Fatal(err)
	}
	want := frameOf(t, memory)
	for name, c := range map[string]*compiledCore{"cold": cold, "warm": warm} {
		if got := frameOf(t, c); !reflect.DeepEqual(got, want) {
			t.Fatalf("%s core frame differs from the in-memory core:\n%#v\n%#v", name, got, want)
		}
	}
	if mode := mustStat(t, filepath.Join(dir, buildKey())).Mode().Perm(); mode != 0o700 {
		t.Fatalf("build folder mode %o, want 700", mode)
	}
}

func TestCompilationCacheCorruptEntryFallsBackAndStays(t *testing.T) {
	dir := cacheTestDir(t)
	if _, ok := compileCached(dir); !ok {
		t.Fatal("cold compile failed")
	}
	entries := cacheEntries(t, dir)
	if len(entries) != 1 {
		t.Fatalf("entries %v", entries)
	}
	if err := os.WriteFile(entries[0], []byte("not a compiled module"), 0o600); err != nil {
		t.Fatal(err)
	}
	for range 2 { // every later process falls back the same way; the entry is not removed
		if _, ok := compileCached(dir); ok {
			t.Fatal("a corrupt entry was used")
		}
	}
	if got := cacheEntries(t, dir); len(got) != 1 || got[0] != entries[0] {
		t.Fatalf("the corrupt entry was removed or replaced: %v", got)
	}
}

func TestCompilationCacheUnwritableEntryFolderStillCompilesOnce(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root ignores folder permissions")
	}
	dir := cacheTestDir(t)
	if _, ok := compileCached(dir); !ok {
		t.Fatal("first compile failed")
	}
	for _, f := range cacheEntries(t, dir) {
		if err := os.Remove(f); err != nil {
			t.Fatal(err)
		}
	}
	// wazero's own version folder read-only: the entry write fails, as on a full disk.
	versions, _ := filepath.Glob(filepath.Join(dir, buildKey(), "wazero-*"))
	if len(versions) != 1 {
		t.Fatalf("version folders %v", versions)
	}
	if err := os.Chmod(versions[0], 0o500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(versions[0], 0o700) })
	c, ok := compileCached(dir)
	if !ok {
		t.Fatal("a failed entry write did not reuse the compiled module")
	}
	if len(cacheEntries(t, dir)) != 0 {
		t.Fatal("an entry was written into a read-only folder")
	}
	if got, want := frameOf(t, c), frameOf(t, mustCore(t)); !reflect.DeepEqual(got, want) {
		t.Fatal("the reused module renders differently")
	}
}

func TestCompilationCacheUncreatableFolderFallsBack(t *testing.T) {
	file := filepath.Join(cacheTestDir(t), "not-a-folder")
	if err := os.WriteFile(file, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, ok := compileCached(filepath.Join(file, "terminal-core")); ok {
		t.Fatal("a cache under a regular file was used")
	}
}

func TestCompilationCacheKeyFollowsCoreAndOptions(t *testing.T) {
	if first, again := buildKey(), buildKey(); len(first) != 16 || first != again {
		t.Fatalf("build key %q, then %q", first, again)
	}
	saved := artifact
	t.Cleanup(func() { artifact = saved })
	first := buildKey()
	artifact = append(append([]byte(nil), saved...), 0)
	if buildKey() == first {
		t.Fatal("a different core kept the same build key")
	}
}

// cacheTestDir is a fresh folder for a cache test.
func cacheTestDir(t testing.TB) string {
	t.Helper()
	return t.TempDir()
}

func mustStat(t *testing.T, path string) os.FileInfo {
	t.Helper()
	fi, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	return fi
}

func mustCore(t *testing.T) *compiledCore {
	t.Helper()
	c, err := buildCore(runtimeConfig())
	if err != nil {
		t.Fatal(err)
	}
	return c
}

// BenchmarkCoreStart compares the in-memory compile with the first start on an
// empty cache (compile and write) and with a warm cache, as a new process pays
// it for its first terminal.
func BenchmarkCoreStart(b *testing.B) {
	b.Run("InMemory", func(b *testing.B) {
		for b.Loop() {
			if _, err := buildCore(runtimeConfig()); err != nil {
				b.Fatal(err)
			}
		}
	})
	b.Run("EmptyCache", func(b *testing.B) {
		parent := cacheTestDir(b)
		i := 0
		for b.Loop() {
			i++
			if _, ok := compileCached(filepath.Join(parent, fmt.Sprint(i))); !ok {
				b.Fatal("first compile failed")
			}
		}
	})
	b.Run("WarmCache", func(b *testing.B) {
		dir := filepath.Join(cacheTestDir(b), "terminal-core")
		if _, ok := compileCached(dir); !ok {
			b.Fatal("cold compile failed")
		}
		for b.Loop() {
			if _, ok := compileCached(dir); !ok {
				b.Fatal("warm load failed")
			}
		}
	})
}
