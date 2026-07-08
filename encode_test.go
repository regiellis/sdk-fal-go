package fal

import (
	"encoding/base64"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestEncode(t *testing.T) {
	data := []byte("hello world")
	b64 := base64.StdEncoding.EncodeToString(data)

	tests := []struct {
		name        string
		data        []byte
		contentType string
		want        string
	}{
		{
			name:        "explicit content type",
			data:        data,
			contentType: "text/plain",
			want:        "data:text/plain;base64," + b64,
		},
		{
			name:        "empty content type defaults to octet-stream",
			data:        data,
			contentType: "",
			want:        "data:application/octet-stream;base64," + b64,
		},
		{
			name:        "empty data",
			data:        nil,
			contentType: "image/png",
			want:        "data:image/png;base64,",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := Encode(tt.data, tt.contentType); got != tt.want {
				t.Errorf("Encode() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestEncodeFile(t *testing.T) {
	dir := t.TempDir()
	data := []byte{0x89, 0x50, 0x4e, 0x47, 0x0d, 0x0a}
	b64 := base64.StdEncoding.EncodeToString(data)

	pngPath := filepath.Join(dir, "image.png")
	if err := os.WriteFile(pngPath, data, 0o600); err != nil {
		t.Fatalf("writing fixture: %v", err)
	}
	unknownPath := filepath.Join(dir, "blob.unknownext")
	if err := os.WriteFile(unknownPath, data, 0o600); err != nil {
		t.Fatalf("writing fixture: %v", err)
	}

	t.Run("known extension", func(t *testing.T) {
		got, err := EncodeFile(pngPath)
		if err != nil {
			t.Fatalf("EncodeFile() error = %v", err)
		}
		want := "data:image/png;base64," + b64
		if got != want {
			t.Errorf("EncodeFile() = %q, want %q", got, want)
		}
	})

	t.Run("unknown extension falls back", func(t *testing.T) {
		got, err := EncodeFile(unknownPath)
		if err != nil {
			t.Fatalf("EncodeFile() error = %v", err)
		}
		want := "data:application/octet-stream;base64," + b64
		if got != want {
			t.Errorf("EncodeFile() = %q, want %q", got, want)
		}
	})

	t.Run("missing file errors", func(t *testing.T) {
		_, err := EncodeFile(filepath.Join(dir, "nope.png"))
		if err == nil {
			t.Fatal("EncodeFile() expected error for missing file")
		}
		if !strings.Contains(err.Error(), "fal:") {
			t.Errorf("error %q should be wrapped with fal: prefix", err)
		}
	})
}
