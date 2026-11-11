package main

import (
	"fmt"

	"flodni/internal/config"
)

// applyEffectivePaths makes the paths the daemon mounts the only paths the
// config carries, so the library writes stubs where FUSE serves them. A
// different non-empty value from config.json is overridden and reported.
func applyEffectivePaths(cfg *config.Config, source, mount string) (warnings []string) {
	if cfg.PhysicalSourcePath != "" && cfg.PhysicalSourcePath != source {
		warnings = append(warnings, fmt.Sprintf(
			"config.json physical_source_path %q differs from the mounted source path %q; the command-line path wins",
			cfg.PhysicalSourcePath, source))
	}
	if cfg.FuseMountPath != "" && cfg.FuseMountPath != mount {
		warnings = append(warnings, fmt.Sprintf(
			"config.json fuse_mount_path %q differs from the mount path %q; the command-line path wins",
			cfg.FuseMountPath, mount))
	}
	cfg.PhysicalSourcePath = source
	cfg.FuseMountPath = mount
	return warnings
}
