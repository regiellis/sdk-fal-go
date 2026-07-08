package fal

import "testing"

func TestParseAppID(t *testing.T) {
	tests := []struct {
		name    string
		input   string
		want    appID
		wantErr bool
	}{
		{
			name:  "owner and alias",
			input: "fal-ai/flux",
			want:  appID{Owner: "fal-ai", Alias: "flux"},
		},
		{
			name:  "owner alias and path",
			input: "fal-ai/flux/dev",
			want:  appID{Owner: "fal-ai", Alias: "flux", Path: "dev"},
		},
		{
			name:  "owner alias and multi-segment path",
			input: "owner/app/a/b/c",
			want:  appID{Owner: "owner", Alias: "app", Path: "a/b/c"},
		},
		{
			name:  "workflows namespace",
			input: "workflows/owner/my-workflow",
			want:  appID{Namespace: "workflows", Owner: "owner", Alias: "my-workflow"},
		},
		{
			name:  "comfy namespace with path",
			input: "comfy/owner/graph/run",
			want:  appID{Namespace: "comfy", Owner: "owner", Alias: "graph", Path: "run"},
		},
		{
			name:  "legacy numeric prefix",
			input: "12345-my-app",
			want:  appID{Owner: "12345", Alias: "my-app"},
		},
		{
			name:    "bare word without slash",
			input:   "justanapp",
			wantErr: true,
		},
		{
			name:    "empty",
			input:   "",
			wantErr: true,
		},
		{
			name:    "namespace missing alias",
			input:   "workflows/owner",
			wantErr: true,
		},
		{
			name:    "empty alias",
			input:   "owner/",
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := parseAppID(tt.input)
			if tt.wantErr {
				if err == nil {
					t.Fatalf("parseAppID(%q) = %+v, want error", tt.input, got)
				}
				return
			}
			if err != nil {
				t.Fatalf("parseAppID(%q) unexpected error: %v", tt.input, err)
			}
			if got != tt.want {
				t.Fatalf("parseAppID(%q) = %+v, want %+v", tt.input, got, tt.want)
			}
		})
	}
}

func TestAppIDPath(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		{"fal-ai/flux", "fal-ai/flux"},
		{"fal-ai/flux/dev", "fal-ai/flux/dev"},
		{"workflows/owner/wf", "workflows/owner/wf"},
		{"comfy/owner/graph/run", "comfy/owner/graph/run"},
		{"12345-app", "12345/app"},
	}
	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			id, err := parseAppID(tt.input)
			if err != nil {
				t.Fatalf("parseAppID(%q): %v", tt.input, err)
			}
			if got := id.path(); got != tt.want {
				t.Fatalf("path() = %q, want %q", got, tt.want)
			}
		})
	}
}
