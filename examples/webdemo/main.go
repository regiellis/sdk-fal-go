// Command webdemo serves a small web page for exercising the SDK from a
// browser. It renders a prompt form with a model picker, runs the chosen
// model, and displays the returned images alongside the raw JSON response.
//
// It also tracks spend from three sources: fal reports billable units on every
// inference response through the X-Fal-Billable-Units header, which the demo
// captures with a wrapping http.RoundTripper injected via fal.WithHTTPClient;
// the platform API (api.fal.ai/v1/account/billing?expand=credits, admin-scoped
// key required) supplies the live credit balance; and the REST billing endpoint
// supplies the monthly budget and lock state. The balance refreshes after each
// generation, and the page shows the dollars spent since the server started.
//
// It reads credentials from the environment (FAL_KEY), binds to 0.0.0.0 on the
// first free port starting at 8080, and prints the reachable URLs on startup.
// Run it with:
//
//	go run ./examples/webdemo
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"html/template"
	"io"
	"net"
	"net/http"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	fal "github.com/regiellis/sdk-fal-go"
)

const (
	basePort        = 8080
	maxPort         = 8100
	defaultModel    = "fal-ai/flux/schnell"
	runTimeout      = 3 * time.Minute
	billingURL      = "https://rest.fal.ai/billing/user_details"
	balanceURL      = "https://api.fal.ai/v1/account/billing?expand=credits"
	billingCacheTTL = 5 * time.Minute
)

// imageModels is the curated dropdown list. The form also accepts a custom
// model id for anything not listed.
var imageModels = []string{
	"fal-ai/flux/schnell",
	"fal-ai/flux/dev",
	"fal-ai/flux-pro/v1.1",
	"fal-ai/fast-sdxl",
	"fal-ai/fast-turbo-diffusion",
}

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}

func run() error {
	ln, port, err := listen()
	if err != nil {
		return err
	}
	printAddresses(port)

	s := &server{
		tmpl:  template.Must(template.New("page").Parse(pageHTML)),
		stats: sessionStats{ByModel: map[string]int{}},
	}

	mux := http.NewServeMux()
	mux.HandleFunc("GET /{$}", s.index)
	mux.HandleFunc("POST /generate", s.generate)

	srv := &http.Server{
		Handler:           mux,
		ReadHeaderTimeout: 10 * time.Second,
	}
	return srv.Serve(ln)
}

// listen binds 0.0.0.0 on the first free port in [basePort, maxPort].
func listen() (net.Listener, int, error) {
	for port := basePort; port <= maxPort; port++ {
		ln, err := net.Listen("tcp", fmt.Sprintf("0.0.0.0:%d", port))
		if err == nil {
			return ln, port, nil
		}
	}
	return nil, 0, fmt.Errorf("no free port between %d and %d", basePort, maxPort)
}

// printAddresses logs every reachable URL for the bound port to stdout.
func printAddresses(port int) {
	fmt.Printf("webdemo listening on 0.0.0.0:%d\n", port)
	fmt.Printf("  http://localhost:%d\n", port)
	addrs, err := net.InterfaceAddrs()
	if err != nil {
		return
	}
	for _, addr := range addrs {
		ipNet, ok := addr.(*net.IPNet)
		if !ok || ipNet.IP.IsLoopback() || ipNet.IP.To4() == nil {
			continue
		}
		fmt.Printf("  http://%s:%d\n", ipNet.IP, port)
	}
}

// sessionStats accumulates billable units reported since the server started.
type sessionStats struct {
	Requests int
	Units    int
	ByModel  map[string]int
}

// billingDetails is the subset of GET /billing/user_details the page shows.
type billingDetails struct {
	HardMonthlyBudget *float64 `json:"hard_monthly_budget"`
	IsLocked          bool     `json:"is_locked"`
	LockReason        string   `json:"lock_reason"`
}

// accountBilling is the platform API's billing response with credits expanded.
type accountBilling struct {
	Username string `json:"username"`
	Credits  *struct {
		CurrentBalance float64 `json:"current_balance"`
		Currency       string  `json:"currency"`
	} `json:"credits"`
}

// accountState combines everything the spend bar shows about the account.
type accountState struct {
	details *billingDetails
	balance *accountBilling
	// balanceErr and detailsErr hold short failure notes per source.
	balanceErr string
	detailsErr string
}

type server struct {
	tmpl *template.Template

	mu    sync.Mutex
	stats sessionStats

	billingMu      sync.Mutex
	billing        accountState
	billingWhen    time.Time
	initialBalance *float64 // first observed balance, for the spent-since-start figure
}

// pageData feeds the HTML template for both the empty form and a result.
type pageData struct {
	Prompt      string
	Model       string
	CustomModel string
	Models      []string
	Images      []string
	Raw         string
	Err         string
	Duration    string
	Units       string

	SessionRequests int
	SessionUnits    int
	Balance         string
	SpentSinceStart string
	Budget          string
	Locked          bool
	LockReason      string
	BillingNote     string
}

func (s *server) index(w http.ResponseWriter, _ *http.Request) {
	s.render(w, s.withShared(pageData{Model: defaultModel}))
}

func (s *server) generate(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		http.Error(w, "bad form", http.StatusBadRequest)
		return
	}
	data := pageData{
		Prompt:      strings.TrimSpace(r.PostFormValue("prompt")),
		Model:       strings.TrimSpace(r.PostFormValue("model")),
		CustomModel: strings.TrimSpace(r.PostFormValue("custom_model")),
	}
	model := data.Model
	if data.CustomModel != "" {
		model = data.CustomModel
	}
	if model == "" {
		model = defaultModel
	}
	if data.Prompt == "" {
		data.Err = "Enter a prompt."
		s.render(w, s.withShared(data))
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), runTimeout)
	defer cancel()

	// A per-request client with a capturing transport records the billing
	// header without touching shared state.
	capture := &captureTransport{base: http.DefaultTransport}
	client := fal.NewClient(fal.WithHTTPClient(&http.Client{
		Transport: capture,
		Timeout:   runTimeout,
	}))

	start := time.Now()
	raw, err := client.Run(ctx, model, map[string]any{
		"prompt":     data.Prompt,
		"image_size": "square",
		"num_images": 1,
	})
	data.Duration = time.Since(start).Round(10 * time.Millisecond).String()
	if err != nil {
		data.Err = err.Error()
		s.render(w, s.withShared(data))
		return
	}

	if units, ok := capture.units(); ok {
		data.Units = strconv.Itoa(units)
		s.mu.Lock()
		s.stats.Requests++
		s.stats.Units += units
		s.stats.ByModel[model] += units
		s.mu.Unlock()
	} else {
		s.mu.Lock()
		s.stats.Requests++
		s.mu.Unlock()
	}

	// Invalidate the billing cache so the next render shows the balance after
	// this generation.
	s.billingMu.Lock()
	s.billingWhen = time.Time{}
	s.billingMu.Unlock()

	data.Images = imageURLs(raw)
	data.Raw = indentJSON(raw)
	s.render(w, s.withShared(data))
}

// withShared fills the fields common to every render: the model list, session
// spend counters, and cached account billing details.
func (s *server) withShared(data pageData) pageData {
	data.Models = imageModels
	if data.Model == "" {
		data.Model = defaultModel
	}

	s.mu.Lock()
	data.SessionRequests = s.stats.Requests
	data.SessionUnits = s.stats.Units
	s.mu.Unlock()

	state, initial := s.accountState()

	if state.balance != nil && state.balance.Credits != nil {
		cur := state.balance.Credits.CurrentBalance
		data.Balance = fmt.Sprintf("$%.2f %s", cur, state.balance.Credits.Currency)
		if initial != nil && *initial > cur {
			data.SpentSinceStart = fmt.Sprintf("$%.2f", *initial-cur)
		}
	} else if state.balanceErr != "" {
		data.BillingNote = "balance unavailable: " + state.balanceErr
	}

	if state.details != nil {
		if state.details.HardMonthlyBudget != nil {
			data.Budget = fmt.Sprintf("$%.2f/month", *state.details.HardMonthlyBudget)
		} else {
			data.Budget = "not set"
		}
		data.Locked = state.details.IsLocked
		data.LockReason = state.details.LockReason
	} else if state.detailsErr != "" && data.BillingNote == "" {
		data.BillingNote = "billing lookup failed: " + state.detailsErr
	}

	if os.Getenv("FAL_KEY") == "" {
		data.BillingNote = "set FAL_KEY to show account billing state"
	}
	return data
}

// accountState returns the cached account billing state, refreshing it when the
// cache has expired or was invalidated, plus the first balance ever observed.
// It requires FAL_KEY; other credential sources are out of scope for the demo.
func (s *server) accountState() (accountState, *float64) {
	s.billingMu.Lock()
	defer s.billingMu.Unlock()

	if time.Since(s.billingWhen) >= billingCacheTTL {
		s.billingWhen = time.Now()
		s.billing = fetchAccountState()
		if s.initialBalance == nil && s.billing.balance != nil && s.billing.balance.Credits != nil {
			v := s.billing.balance.Credits.CurrentBalance
			s.initialBalance = &v
		}
	}
	return s.billing, s.initialBalance
}

// fetchAccountState queries both billing sources with FAL_KEY. A missing key
// returns an empty state with no error notes.
func fetchAccountState() accountState {
	key := os.Getenv("FAL_KEY")
	if key == "" {
		return accountState{}
	}

	var state accountState

	var balance accountBilling
	if err := getJSON(balanceURL, key, &balance); err != nil {
		state.balanceErr = err.Error()
	} else {
		state.balance = &balance
	}

	var details billingDetails
	if err := getJSON(billingURL, key, &details); err != nil {
		state.detailsErr = err.Error()
	} else {
		state.details = &details
	}
	return state
}

// getJSON performs an authenticated GET against a billing endpoint and decodes
// the JSON response into out.
func getJSON(url, key string, out any) error {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Key "+key)

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return err
	}
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("status %d", resp.StatusCode)
	}
	return json.Unmarshal(body, out)
}

// captureTransport records the X-Fal-Billable-Units header from the most
// recent response that carried one.
type captureTransport struct {
	base http.RoundTripper

	mu        sync.Mutex
	lastUnits int
	seen      bool
}

// RoundTrip implements http.RoundTripper.
func (t *captureTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	resp, err := t.base.RoundTrip(req)
	if err != nil {
		return resp, err
	}
	if v := resp.Header.Get("X-Fal-Billable-Units"); v != "" {
		if n, perr := strconv.Atoi(v); perr == nil {
			t.mu.Lock()
			t.lastUnits = n
			t.seen = true
			t.mu.Unlock()
		}
	}
	return resp, nil
}

// units reports the captured billable units and whether any were seen.
func (t *captureTransport) units() (int, bool) {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.lastUnits, t.seen
}

// imageURLs extracts image URLs from a model response, tolerating responses
// that carry none.
func imageURLs(raw json.RawMessage) []string {
	var out struct {
		Images []struct {
			URL string `json:"url"`
		} `json:"images"`
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil
	}
	urls := make([]string, 0, len(out.Images))
	for _, img := range out.Images {
		if img.URL != "" {
			urls = append(urls, img.URL)
		}
	}
	return urls
}

// indentJSON pretty-prints a raw JSON payload, falling back to the input text.
func indentJSON(raw json.RawMessage) string {
	var buf bytes.Buffer
	if err := json.Indent(&buf, raw, "", "  "); err != nil {
		return string(raw)
	}
	return buf.String()
}

func (s *server) render(w http.ResponseWriter, data pageData) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := s.tmpl.Execute(w, data); err != nil {
		fmt.Fprintln(os.Stderr, "render error:", err)
	}
}

const pageHTML = `<!doctype html>
<html lang="en">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<title>fal SDK demo</title>
<style>
  :root { color-scheme: light dark; }
  body {
    font-family: system-ui, sans-serif;
    max-width: 46rem;
    margin: 2.5rem auto;
    padding: 0 1rem;
    line-height: 1.5;
  }
  h1 { font-size: 1.3rem; }
  form { display: grid; gap: .6rem; margin-bottom: 1rem; }
  input, select, button {
    font: inherit;
    padding: .55rem .7rem;
    border: 1px solid color-mix(in srgb, currentColor 30%, transparent);
    border-radius: 6px;
    background: transparent;
    color: inherit;
  }
  select option { color: initial; }
  button { cursor: pointer; font-weight: 600; justify-self: start; padding-inline: 1.2rem; }
  .err {
    border: 1px solid #c0392b;
    border-radius: 6px;
    padding: .6rem .8rem;
    color: #c0392b;
    overflow-wrap: anywhere;
  }
  .meta { opacity: .65; font-size: .85rem; }
  .spend {
    display: flex;
    flex-wrap: wrap;
    gap: .4rem 1.4rem;
    font-size: .85rem;
    padding: .6rem .8rem;
    border: 1px solid color-mix(in srgb, currentColor 18%, transparent);
    border-radius: 6px;
    margin-bottom: 1.4rem;
  }
  .spend strong { font-weight: 600; }
  .locked { color: #c0392b; font-weight: 600; }
  img { max-width: 100%; border-radius: 8px; display: block; margin: .8rem 0; }
  details { margin-top: 1rem; }
  pre {
    overflow-x: auto;
    padding: .8rem;
    border-radius: 6px;
    background: color-mix(in srgb, currentColor 8%, transparent);
    font-size: .8rem;
  }
</style>
</head>
<body>
<h1>fal SDK demo</h1>

<div class="spend">
  <span>Session: <strong>{{.SessionRequests}}</strong> request{{if ne .SessionRequests 1}}s{{end}} · <strong>{{.SessionUnits}}</strong> billable unit{{if ne .SessionUnits 1}}s{{end}}</span>
  {{if .Balance}}<span>Balance: <strong>{{.Balance}}</strong></span>{{end}}
  {{if .SpentSinceStart}}<span>Spent since start: <strong>{{.SpentSinceStart}}</strong></span>{{end}}
  {{if .Budget}}<span>Monthly budget: <strong>{{.Budget}}</strong></span>{{end}}
  {{if .Locked}}<span class="locked">Account locked{{if .LockReason}}: {{.LockReason}}{{end}}</span>{{end}}
  {{if .BillingNote}}<span>{{.BillingNote}}</span>{{end}}
  <span><a href="https://fal.ai/dashboard/billing" target="_blank">billing dashboard</a></span>
</div>

<form method="post" action="/generate">
  <input name="prompt" placeholder="Prompt" value="{{.Prompt}}" autofocus>
  <select name="model">
    {{$selected := .Model}}
    {{range .Models}}<option value="{{.}}"{{if eq . $selected}} selected{{end}}>{{.}}</option>{{end}}
  </select>
  <input name="custom_model" placeholder="Custom model id (overrides the dropdown)" value="{{.CustomModel}}">
  <button>Generate</button>
</form>

{{if .Err}}<p class="err">{{.Err}}</p>{{end}}
{{if .Duration}}<p class="meta">completed in {{.Duration}}{{if .Units}} · {{.Units}} billable unit{{if ne .Units "1"}}s{{end}} this request{{end}}</p>{{end}}
{{range .Images}}<a href="{{.}}" target="_blank"><img src="{{.}}" alt="generated image"></a>{{end}}
{{if .Raw}}<details><summary>Raw response</summary><pre>{{.Raw}}</pre></details>{{end}}
</body>
</html>`
