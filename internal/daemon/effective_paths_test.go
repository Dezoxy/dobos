package daemon

import (
	"strings"
	"testing"

	"flodni/internal/config"
)

func TestApplyEffectivePaths(t *testing.T) {
	const src, mnt = "/mnt/real", "/mnt/virtual"
	tests := []struct {
		name         string
		in           config.Config
		wantWarnings int
		wantContains []string
	}{
		{"empty config is filled", config.Config{}, 0, nil},
		{"equal config gives no warning",
			config.Config{PhysicalSourcePath: src, FuseMountPath: mnt}, 0, nil},
		{"different source is overridden and reported",
			config.Config{PhysicalSourcePath: "/other", FuseMountPath: mnt}, 1,
			[]string{"/other", src}},
		{"different mount is overridden and reported",
			config.Config{PhysicalSourcePath: src, FuseMountPath: "/other"}, 1,
			[]string{"/other", mnt}},
		{"both different give two warnings",
			config.Config{PhysicalSourcePath: "/a", FuseMountPath: "/b"}, 2, nil},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			cfg := tc.in
			warnings := applyEffectivePaths(&cfg, src, mnt)
			if cfg.PhysicalSourcePath != src || cfg.FuseMountPath != mnt {
				t.Fatalf("paths = %q, %q; want %q, %q",
					cfg.PhysicalSourcePath, cfg.FuseMountPath, src, mnt)
			}
			if len(warnings) != tc.wantWarnings {
				t.Fatalf("warnings = %v; want %d", warnings, tc.wantWarnings)
			}
			for _, want := range tc.wantContains {
				if !strings.Contains(warnings[0], want) {
					t.Errorf("warning %q lacks %q", warnings[0], want)
				}
			}
		})
	}
}
