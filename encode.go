package fal

import (
	"encoding/base64"
	"fmt"
	"mime"
	"os"
	"path/filepath"
)

// defaultContentType is used when a content type is unknown or unspecified.
const defaultContentType = "application/octet-stream"

// Encode returns an RFC 2397 data URL for data, using base64 encoding and the
// given content type. When contentType is empty it defaults to
// application/octet-stream. The result can be passed directly to model inputs
// that accept file URLs, avoiding a separate upload.
func Encode(data []byte, contentType string) string {
	if contentType == "" {
		contentType = defaultContentType
	}
	return "data:" + contentType + ";base64," + base64.StdEncoding.EncodeToString(data)
}

// EncodeFile reads the file at path and returns an RFC 2397 base64 data URL for
// its contents. The content type is guessed from the file extension, falling
// back to application/octet-stream when the extension is unknown.
func EncodeFile(path string) (string, error) {
	data, err := os.ReadFile(path) // #nosec G304 -- path is supplied by the caller by design
	if err != nil {
		return "", fmt.Errorf("fal: reading file for encoding: %w", err)
	}
	contentType := mime.TypeByExtension(filepath.Ext(path))
	if contentType == "" {
		contentType = defaultContentType
	}
	return Encode(data, contentType), nil
}
