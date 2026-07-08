package fal

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

// multipartServer is a fake v3 CDN that records multipart parts and completion.
type multipartServer struct {
	srv         *httptest.Server
	mu          sync.Mutex
	parts       map[int][]byte
	partHeaders map[int]http.Header
	completeRaw []byte
	createHdrs  http.Header
	uploadID    string
	finalURL    string
}

func newMultipartServer(t *testing.T) *multipartServer {
	t.Helper()
	m := &multipartServer{
		parts:       make(map[int][]byte),
		partHeaders: make(map[int]http.Header),
		uploadID:    "upload-xyz",
	}
	m.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/files/upload/multipart":
			m.mu.Lock()
			m.createHdrs = r.Header.Clone()
			id := m.uploadID
			base := m.srv.URL
			m.mu.Unlock()
			writeJSON(t, w, map[string]string{"access_url": base, "uploadId": id})

		case r.Method == http.MethodPut && strings.HasPrefix(r.URL.Path, "/multipart/"):
			// /multipart/{uploadId}/{n}
			segs := strings.Split(strings.Trim(r.URL.Path, "/"), "/")
			if len(segs) != 3 {
				t.Errorf("unexpected part path %q", r.URL.Path)
				return
			}
			n, err := strconv.Atoi(segs[2])
			if err != nil {
				t.Errorf("part number %q: %v", segs[2], err)
				return
			}
			body, _ := io.ReadAll(r.Body)
			m.mu.Lock()
			m.parts[n] = body
			m.partHeaders[n] = r.Header.Clone()
			m.mu.Unlock()
			w.Header().Set("ETag", "etag-"+segs[2])
			w.WriteHeader(http.StatusOK)

		case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/complete"):
			body, _ := io.ReadAll(r.Body)
			m.mu.Lock()
			m.completeRaw = body
			final := m.srv.URL + "/final-object"
			m.finalURL = final
			m.mu.Unlock()
			writeJSON(t, w, map[string]string{"access_url": final})

		default:
			t.Errorf("unexpected request %s %s", r.Method, r.URL.Path)
			w.WriteHeader(http.StatusInternalServerError)
		}
	}))
	t.Cleanup(m.srv.Close)
	return m
}

// assembled returns the recorded parts concatenated in part-number order.
func (m *multipartServer) assembled() []byte {
	m.mu.Lock()
	defer m.mu.Unlock()
	nums := make([]int, 0, len(m.parts))
	for n := range m.parts {
		nums = append(nums, n)
	}
	sort.Ints(nums)
	var out []byte
	for _, n := range nums {
		out = append(out, m.parts[n]...)
	}
	return out
}

func (m *multipartServer) token() v3Token {
	return v3Token{
		token:     "cdn-token",
		tokenType: "Bearer",
		baseURL:   m.srv.URL,
		expiresAt: time.Now().Add(time.Hour),
	}
}

func TestMultipartUploadFromMemory(t *testing.T) {
	m := newMultipartServer(t)
	c := NewClient(WithKey("acct-key"))

	data := []byte("abcdefghij") // 10 bytes
	const partSize = 4           // parts: abcd | efgh | ij

	src := &bytesSource{data: data}
	url, err := c.uploadV3Multipart(context.Background(), m.token(), src, "text/plain", &uploadConfig{fileName: "blob.txt"}, partSize, 2)
	if err != nil {
		t.Fatalf("uploadV3Multipart() error = %v", err)
	}

	m.mu.Lock()
	finalURL := m.finalURL
	numParts := len(m.parts)
	createFileName := m.createHdrs.Get("X-Fal-File-Name")
	part1AcceptEnc := ""
	if h, ok := m.partHeaders[1]; ok {
		part1AcceptEnc = h.Get("Accept-Encoding")
	}
	part1Auth := ""
	if h, ok := m.partHeaders[1]; ok {
		part1Auth = h.Get("Authorization")
	}
	completeRaw := append([]byte(nil), m.completeRaw...)
	m.mu.Unlock()

	if url != finalURL {
		t.Errorf("returned url = %q, want %q", url, finalURL)
	}
	if numParts != 3 {
		t.Errorf("recorded parts = %d, want 3", numParts)
	}
	if got := string(m.assembled()); got != string(data) {
		t.Errorf("reassembled = %q, want %q", got, data)
	}
	if createFileName != "blob.txt" {
		t.Errorf("create X-Fal-File-Name = %q", createFileName)
	}
	if part1AcceptEnc != "identity" {
		t.Errorf("part Accept-Encoding = %q, want identity", part1AcceptEnc)
	}
	if part1Auth != "Bearer cdn-token" {
		t.Errorf("part Authorization = %q, want CDN token auth", part1Auth)
	}

	// Verify part boundaries precisely.
	wantParts := map[int]string{1: "abcd", 2: "efgh", 3: "ij"}
	m.mu.Lock()
	for n, want := range wantParts {
		if got := string(m.parts[n]); got != want {
			t.Errorf("part %d = %q, want %q", n, got, want)
		}
	}
	m.mu.Unlock()

	// Verify completion body JSON exactly.
	var complete struct {
		Parts []struct {
			PartNumber int    `json:"partNumber"`
			ETag       string `json:"etag"`
		} `json:"parts"`
	}
	if err := json.Unmarshal(completeRaw, &complete); err != nil {
		t.Fatalf("completion body: %v", err)
	}
	if len(complete.Parts) != 3 {
		t.Fatalf("completion parts = %d, want 3", len(complete.Parts))
	}
	for i, p := range complete.Parts {
		wantNum := i + 1
		if p.PartNumber != wantNum {
			t.Errorf("completion part %d number = %d, want %d", i, p.PartNumber, wantNum)
		}
		if p.ETag != "etag-"+strconv.Itoa(wantNum) {
			t.Errorf("completion part %d etag = %q", i, p.ETag)
		}
	}
}

func TestMultipartUploadFileStreaming(t *testing.T) {
	m := newMultipartServer(t)
	c := NewClient(WithKey("acct-key"))

	data := []byte("streamed-from-disk-content-0123456789")
	path := filepath.Join(t.TempDir(), "big.bin")
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatalf("writing fixture: %v", err)
	}

	src, err := openFileSource(path)
	if err != nil {
		t.Fatalf("openFileSource() error = %v", err)
	}
	defer func() { _ = src.Close() }()

	const partSize = 8
	url, err := c.uploadV3Multipart(context.Background(), m.token(), src, "application/octet-stream", &uploadConfig{}, partSize, 3)
	if err != nil {
		t.Fatalf("uploadV3Multipart() error = %v", err)
	}

	if got := string(m.assembled()); got != string(data) {
		t.Errorf("reassembled file = %q, want %q", got, data)
	}
	m.mu.Lock()
	final := m.finalURL
	m.mu.Unlock()
	if url != final {
		t.Errorf("returned url = %q, want %q", url, final)
	}
}
