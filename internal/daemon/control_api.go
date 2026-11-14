package daemon

import (
	"encoding/json"
	"flodni/internal/atomicfile"
	"flodni/internal/config"
	"fmt"
	"net/http"
	"os"
	"syscall"
	"time"
)

// handleConfig serves GET and POST /api/config.
func handleConfig(w http.ResponseWriter, r *http.Request) {
	if r.Method == "GET" {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(gc().Redacted())
		return
	}
	if r.Method == "POST" {
		var newCfg config.Config
		if err := json.NewDecoder(r.Body).Decode(&newCfg); err != nil {
			http.Error(w, err.Error(), 400)
			return
		}
		// The panel never sees the token; an empty post keeps the one in the file.
		stored := gc().ResolveAPIToken("")
		newCfg.APIToken = gc().ResolveAPIToken(newCfg.APIToken)
		// No token enforced: any client that reaches the port could choose the token
		// the next start enforces. Setting or changing it needs the environment or
		// the file; leaving it as stored (absent, or the echoed empty value) is fine.
		if controlToken == "" && newCfg.APIToken != stored {
			refuseOpenPort(w, "api_token cannot be set or changed through the API while no token is enforced")
			return
		}
		// Paths that came from the command line are not written to the
		// file: keep what the file had.
		onDisk := config.LoadConfig()
		if newCfg.PhysicalSourcePath == physicalSourcePath {
			newCfg.PhysicalSourcePath = onDisk.PhysicalSourcePath
		}
		if newCfg.FuseMountPath == virtualMountPath {
			newCfg.FuseMountPath = onDisk.FuseMountPath
		}
		// Update file
		data, _ := json.MarshalIndent(newCfg, "", "  ")
		if err := atomicfile.WriteFileKeepMode(gc().ConfigPath, data, 0600); err != nil {
			http.Error(w, err.Error(), 500)
			return
		}
		// Reload in memory (V1.4.0 Live Update)
		cfg := config.LoadConfig()
		// A save must not reintroduce a path other than the mounted one.
		_ = applyEffectivePaths(&cfg, physicalSourcePath, virtualMountPath)
		// RootPath comes from -path at startup, not from the file; without it
		// GetStateDir falls back to /home/pi/STATE after every save.
		cfg.RootPath = gc().RootPath
		globalConfig.Store(&cfg)
		applyLogConfig()
		logger.Info("[Config] Updated via Dashboard API")
		w.WriteHeader(200)
	}
}

// refuseOpenPort answers 403 with a JSON error that says where to set the token.
func refuseOpenPort(w http.ResponseWriter, what string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusForbidden)
	_ = json.NewEncoder(w).Encode(map[string]string{
		"error": what + ": set api_token through the FLODNI_API_TOKEN environment variable " +
			"or by editing config.json, then restart flodni",
	})
}

// restartSelf asks the process to stop gracefully; a test replaces it.
var restartSelf = func() {
	restartRequested.Store(true)
	go func() {
		time.Sleep(150 * time.Millisecond)
		p, _ := os.FindProcess(os.Getpid())
		if err := p.Signal(syscall.SIGTERM); err != nil {
			logger.Warn(fmt.Sprintf("WARNING: restart requested but SIGTERM to self failed: %v", err), "err", err)
		}
	}()
}

// handleRestart serves POST /api/restart.
func handleRestart(w http.ResponseWriter, r *http.Request) {
	if r.Method != "POST" {
		http.Error(w, "method not allowed", 405)
		return
	}
	if controlToken == "" {
		refuseOpenPort(w, "restart is refused while no token is enforced")
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(200)
	_, _ = w.Write([]byte(`{"ok":true}`))
	// Flush response before exiting
	if f, ok := w.(http.Flusher); ok {
		f.Flush()
	}
	// Graceful shutdown, then exit with restartExitCode: systemd restarts on any
	// code, and the Docker entrypoint's supervision loop restarts only on this one.
	restartSelf()
}
