package fal

import (
	"context"
	"net/http"
	"testing"
	"time"
)

func TestNewClientDefaults(t *testing.T) {
	t.Setenv("FAL_RUN_HOST", "")
	t.Setenv("FAL_QUEUE_RUN_HOST", "")

	c := NewClient()
	if c.runURL != "https://fal.run" {
		t.Errorf("runURL = %q, want https://fal.run", c.runURL)
	}
	if c.queueURL != "https://queue.fal.run" {
		t.Errorf("queueURL = %q, want https://queue.fal.run", c.queueURL)
	}
	if c.restURL != "https://rest.fal.ai" {
		t.Errorf("restURL = %q, want https://rest.fal.ai", c.restURL)
	}
	if c.cdnURL != "https://v3.fal.media" {
		t.Errorf("cdnURL = %q, want https://v3.fal.media", c.cdnURL)
	}
	if c.userAgent != "sdk-fal-go/"+Version+" (go)" {
		t.Errorf("userAgent = %q", c.userAgent)
	}
	if c.httpClient.Timeout != defaultTimeout {
		t.Errorf("timeout = %v, want %v", c.httpClient.Timeout, defaultTimeout)
	}
}

func TestNewClientRunHostOverride(t *testing.T) {
	t.Setenv("FAL_RUN_HOST", "gateway.example.com")
	t.Setenv("FAL_QUEUE_RUN_HOST", "")

	c := NewClient()
	if c.runURL != "https://gateway.example.com" {
		t.Errorf("runURL = %q, want https://gateway.example.com", c.runURL)
	}
	if c.queueURL != "https://queue.gateway.example.com" {
		t.Errorf("queueURL = %q, want https://queue.gateway.example.com", c.queueURL)
	}
}

func TestNewClientQueueHostOverride(t *testing.T) {
	t.Setenv("FAL_RUN_HOST", "")
	t.Setenv("FAL_QUEUE_RUN_HOST", "myqueue.example.com")

	c := NewClient()
	if c.queueURL != "https://myqueue.example.com" {
		t.Errorf("queueURL = %q, want https://myqueue.example.com", c.queueURL)
	}
}

func TestNewClientExplicitOptionsWin(t *testing.T) {
	c := NewClient(
		WithRunURL("http://localhost:8080/"),
		WithQueueURL("http://localhost:8081/"),
		WithRestURL("http://localhost:8082/"),
		WithCDNURL("http://localhost:8083/"),
		WithUserAgent("custom/1.0"),
		WithTimeout(5*time.Second),
	)
	if c.runURL != "http://localhost:8080" {
		t.Errorf("runURL = %q (trailing slash should be trimmed)", c.runURL)
	}
	if c.queueURL != "http://localhost:8081" {
		t.Errorf("queueURL = %q", c.queueURL)
	}
	if c.userAgent != "custom/1.0" {
		t.Errorf("userAgent = %q", c.userAgent)
	}
	if c.httpClient.Timeout != 5*time.Second {
		t.Errorf("timeout = %v, want 5s", c.httpClient.Timeout)
	}
}

func TestNewClientWithKey(t *testing.T) {
	clearFalEnv(t)
	c := NewClient(WithKey("secret-key"))
	creds, err := c.creds.Credentials(context.Background())
	if err != nil {
		t.Fatalf("Credentials: %v", err)
	}
	want := Credentials{Scheme: "Key", Token: "secret-key"}
	if creds != want {
		t.Fatalf("creds = %+v, want %+v", creds, want)
	}
}

// fixedCredentials is a test CredentialsProvider.
type fixedCredentials struct {
	creds Credentials
	err   error
}

func (f fixedCredentials) Credentials(context.Context) (Credentials, error) {
	return f.creds, f.err
}

func TestNewClientWithCredentialsOverridesKey(t *testing.T) {
	c := NewClient(
		WithKey("ignored"),
		WithCredentials(fixedCredentials{creds: Credentials{Scheme: "Bearer", Token: "tok"}}),
	)
	creds, err := c.creds.Credentials(context.Background())
	if err != nil {
		t.Fatalf("Credentials: %v", err)
	}
	if creds.Scheme != "Bearer" || creds.Token != "tok" {
		t.Fatalf("creds = %+v, want Bearer tok", creds)
	}
}

func TestNewRequestSetsAuthAndUserAgent(t *testing.T) {
	c := NewClient(
		WithKey("mykey"),
		WithUserAgent("ua/2.0"),
	)
	req, err := c.newRequest(context.Background(), http.MethodPost, "https://fal.run/fal-ai/flux", nil)
	if err != nil {
		t.Fatalf("newRequest: %v", err)
	}
	if got := req.Header.Get("Authorization"); got != "Key mykey" {
		t.Errorf("Authorization = %q, want Key mykey", got)
	}
	if got := req.Header.Get("User-Agent"); got != "ua/2.0" {
		t.Errorf("User-Agent = %q, want ua/2.0", got)
	}
}

func TestNewRequestSurfacesCredentialError(t *testing.T) {
	clearFalEnv(t)
	c := NewClient() // default provider, no credentials available
	_, err := c.newRequest(context.Background(), http.MethodGet, "https://fal.run/app", nil)
	if err == nil {
		t.Fatal("expected credential error to surface on newRequest")
	}
}
