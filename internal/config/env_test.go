package config

import "testing"

// clearPlexEnv blanks every name applyEnvOverrides reads for the Plex
// settings, so a variable set in the developer's shell cannot leak in.
func clearPlexEnv(t *testing.T) {
	t.Helper()
	for _, k := range []string{
		"FLODNI_PLEX_URL", "TIRAMISU_PLEX_URL", "GOSTREAM_PLEX_URL", "PLEX_URL",
		"FLODNI_PLEX_TOKEN", "TIRAMISU_PLEX_TOKEN", "GOSTREAM_PLEX_TOKEN", "PLEX_TOKEN",
	} {
		t.Setenv(k, "")
	}
}

func TestEnvPrecedencePlex(t *testing.T) {
	tests := []struct {
		name      string
		env       map[string]string
		wantURL   string
		wantToken string
	}{
		{"flodni wins over tiramisu", map[string]string{
			"FLODNI_PLEX_URL": "http://flodni", "TIRAMISU_PLEX_URL": "http://tiramisu",
			"FLODNI_PLEX_TOKEN": "tok-flodni", "TIRAMISU_PLEX_TOKEN": "tok-tiramisu",
		}, "http://flodni", "tok-flodni"},
		{"tiramisu still works alone", map[string]string{
			"TIRAMISU_PLEX_URL": "http://tiramisu", "TIRAMISU_PLEX_TOKEN": "tok-tiramisu",
		}, "http://tiramisu", "tok-tiramisu"},
		{"tiramisu wins over gostream and bare", map[string]string{
			"TIRAMISU_PLEX_URL": "http://tiramisu", "GOSTREAM_PLEX_URL": "http://gostream", "PLEX_URL": "http://bare",
			"TIRAMISU_PLEX_TOKEN": "tok-tiramisu", "GOSTREAM_PLEX_TOKEN": "tok-gostream", "PLEX_TOKEN": "tok-bare",
		}, "http://tiramisu", "tok-tiramisu"},
		{"gostream beats bare", map[string]string{
			"GOSTREAM_PLEX_URL": "http://gostream", "PLEX_URL": "http://bare",
			"GOSTREAM_PLEX_TOKEN": "tok-gostream", "PLEX_TOKEN": "tok-bare",
		}, "http://gostream", "tok-gostream"},
		{"bare name still works", map[string]string{
			"PLEX_URL": "http://bare", "PLEX_TOKEN": "tok-bare",
		}, "http://bare", "tok-bare"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			clearPlexEnv(t)
			for k, v := range tt.env {
				t.Setenv(k, v)
			}
			var c Config
			c.applyEnvOverrides()
			if c.Plex.URL != tt.wantURL {
				t.Errorf("Plex.URL = %q, want %q", c.Plex.URL, tt.wantURL)
			}
			if c.Plex.Token != tt.wantToken {
				t.Errorf("Plex.Token = %q, want %q", c.Plex.Token, tt.wantToken)
			}
		})
	}
}
