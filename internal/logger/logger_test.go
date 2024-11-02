//nolint:testpackage // Testing internal implementation details that aren't exported
package logger

import (
	"os"
	"strings"
	"testing"

	"codeberg.org/mutker/bumpa/internal/errors"
)

func TestInit(t *testing.T) {
	tests := []struct {
		name    string
		cfg     Config
		wantErr string // Changed to expect specific error contexts
		cleanup bool
	}{
		{
			name: "valid console config",
			cfg: Config{
				Level:  "info",
				Output: "console",
			},
			wantErr: "",
			cleanup: false,
		},
		{
			name: "invalid level",
			cfg: Config{
				Level:  "invalid",
				Output: "console",
			},
			wantErr: errors.ContextInvalidLogLevel,
			cleanup: false,
		},
		{
			name: "valid file output",
			cfg: Config{
				Level:     "info",
				Output:    "file",
				Path:      "test.log",
				FilePerms: 0o644,
			},
			wantErr: "",
			cleanup: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := Init(tt.cfg)

			if tt.wantErr == "" {
				if err != nil {
					t.Errorf("Init() unexpected error = %v", err)
				}
			} else {
				if err == nil {
					t.Error("Init() expected error, got nil")
				} else if !strings.Contains(err.Error(), tt.wantErr) {
					t.Errorf("Init() error = %v, want %v", err, tt.wantErr)
				}
			}

			if tt.cleanup && tt.cfg.Output == "file" {
				os.Remove(tt.cfg.Path)
			}
		})
	}
}
