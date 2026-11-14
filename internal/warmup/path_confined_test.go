package warmup

import (
	"path/filepath"
	"testing"
)

// A cache path never leaves the cache directory, whatever the hash carries.
func TestCachePathsStayInTheCacheDir(t *testing.T) {
	d := &DiskWarmupCache{dir: "/cache"}
	for _, h := range []string{"0123abcd", "../../etc/passwd", "a/b", "/abs", ".."} {
		for _, p := range []string{d.filePath(h, 1), d.tailPath(h, 1)} {
			if filepath.Dir(p) != "/cache" {
				t.Errorf("hash %q: path %q leaves the cache dir", h, p)
			}
		}
	}
	if got := d.filePath("0123abcd", 2); got != filepath.Join("/cache", "0123abcd-2"+warmupSuffix) {
		t.Errorf("a plain hash changed: %q", got)
	}
}
