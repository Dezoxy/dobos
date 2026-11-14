package warmup

import (
	"fmt"
	"strings"
	"testing"
)

// The start-up lines an operator greps for, and the facts the daemon's startup report
// reads, must not drift apart.
func TestStartLinesAndStartInfo(t *testing.T) {
	dir := t.TempDir()
	denseFile(t, dir+"/a-1.warmup", 2<<20)
	old := startInfo.Load()
	t.Cleanup(func() { startInfo.Store(old) })

	buf := markerEnv(t, dir)

	out := buf.String()
	for _, want := range []string{
		"[DiskWarmup] Initial size: ",
		"GB (dropped 0 stale cache files, 0 residues)",
		"[DiskWarmup] Active — dir=" + dir,
	} {
		if !strings.Contains(out, want) {
			t.Errorf("start-up log lacks %q:\n%s", want, out)
		}
	}
	info := Started()
	if !info.Active || info.Dir != dir || info.InitialSizeBytes != 2<<20 || info.Dropped != 0 || info.Residues != 0 {
		t.Errorf("StartInfo = %+v, want active in %s with 2 MiB", info, dir)
	}
	if info.HeadsChecked != 1 || info.HeadsHoley != 0 {
		t.Errorf("heads checked/holey = %d/%d, want 1/0", info.HeadsChecked, info.HeadsHoley)
	}
}

func TestPinnedFormats(t *testing.T) {
	for _, tc := range []struct{ got, want string }{
		{fmt.Sprintf(initialSizeFmt, 1.5, 2, 3), "[DiskWarmup] Initial size: 1.5GB (dropped 2 stale cache files, 3 residues)"},
		{fmt.Sprintf(activeFmt, "/c", 4, 64, 16, true), "[DiskWarmup] Active — dir=/c quota=4GB head=64MB tail=16MB persistent=true"},
		{fmt.Sprintf(corruptCacheFmt, 130.0, "abcdef12"), "[DiskWarmup] CORRUPT CACHE detected (Size: 130.0MB > 128MB) for abcdef12. Removing."},
		{fmt.Sprintf(denseCheckFmt, ".dense-migrated", 3, 1), "[DiskWarmup] WARNING: .dense-migrated was missing: checked 3 heads for holes, dropped 1 (the one-shot density check; never delete that file by hand)"},
	} {
		if tc.got != tc.want {
			t.Errorf("got  %q\nwant %q", tc.got, tc.want)
		}
	}
}
