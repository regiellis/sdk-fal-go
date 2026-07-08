# sdk-fal-go

[![Go Reference](https://pkg.go.dev/badge/github.com/regiellis/sdk-fal-go.svg)](https://pkg.go.dev/github.com/regiellis/sdk-fal-go)
[![Go Report Card](https://goreportcard.com/badge/github.com/regiellis/sdk-fal-go)](https://goreportcard.com/report/github.com/regiellis/sdk-fal-go)
[![License: MIT](https://img.shields.io/badge/License-MIT-blue.svg)](LICENSE)
![Go Version](https://img.shields.io/badge/Go-1.25%2B-00ADD8?logo=go)

A Go client for the [fal.ai](https://fal.ai) inference platform. One `Client` type
covers the whole API surface, every network call takes a `context.Context` first,
and results come back as raw JSON for decoding into your own types.

**Features**

- **Run** models synchronously and get the response in one call.
- **Queue** requests with `Submit`, poll them with a typed status iterator,
  reattach from another process by request id, and cancel when needed.
- **Subscribe** for the common submit-and-wait flow, with progress callbacks and
  automatic best-effort cancellation when the context ends.
- **Stream** Server-Sent Events as a native range-over-func iterator.
- **Upload** files to fal storage, with transparent multipart transfers for
  large files streamed straight from disk, lifecycle and ACL controls, and
  data-URL encoding for skipping storage entirely.
- **Realtime** WebSocket connections with msgpack encoding and structured error
  frames.
- **Resilient by default**: transient failures retry with exponential backoff
  and jitter, and user-set timeouts are always honored over retry logic.
- Two small dependencies (`coder/websocket`, `vmihailenco/msgpack`), used only
  by the realtime API. Everything else is standard library.

## Install

```sh
go get github.com/regiellis/sdk-fal-go
```

The package name is `fal` and it requires Go 1.25 or newer. The only third-party
dependencies are `github.com/coder/websocket` and
`github.com/vmihailenco/msgpack/v5`, both used solely by the realtime API.

## Authentication

Credentials resolve lazily on the first call that needs them, in this order:

1. The `FAL_KEY` environment variable.
2. `FAL_KEY_ID` together with `FAL_KEY_SECRET`.
3. Tokens saved by the fal CLI (`fal auth login`) under
   `$FAL_HOME_DIR/auth0_token` or `~/.fal/auth0_token`, refreshed automatically
   when close to expiry.

Set `FAL_KEY` for most cases:

```sh
export FAL_KEY="your-key"
```

Setting `FAL_FORCE_AUTH_BY_USER=1` skips the environment-key steps and forces the
saved CLI tokens. When nothing resolves, the first call returns
`ErrMissingCredentials`. Supply a key in code with `WithKey`, or plug in a custom
source with `WithCredentials`:

```go
client := fal.NewClient(fal.WithKey("your-key"))
```

`NewClient` never returns an error, and a `Client` is safe for concurrent use.

## Run

`Run` executes a model and returns the raw JSON response. Decode it into your own
type:

```go
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log"

	fal "github.com/regiellis/sdk-fal-go"
)

func main() {
	client := fal.NewClient()

	raw, err := client.Run(context.Background(), "fal-ai/flux/schnell", map[string]any{
		"prompt":              "a red panda reading a book",
		"image_size":          "square",
		"num_images":          1,
		"num_inference_steps": 4,
	})
	if err != nil {
		log.Fatal(err)
	}

	var out struct {
		Images []struct {
			URL string `json:"url"`
		} `json:"images"`
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		log.Fatal(err)
	}
	fmt.Println(out.Images[0].URL)
}
```

## Subscribe

`Subscribe` enqueues a request, polls it to completion, and returns the result.
`OnUpdate` reports each status change, and `WithLogs` asks the server to include
log entries:

```go
raw, err := client.Subscribe(ctx, "fal-ai/flux/schnell", input,
	fal.WithLogs(),
	fal.OnEnqueue(func(id string) {
		log.Printf("queued: %s", id)
	}),
	fal.OnUpdate(func(s fal.Status) {
		switch v := s.(type) {
		case fal.Queued:
			log.Printf("position %d", v.Position)
		case fal.InProgress:
			log.Printf("in progress, %d log lines", len(v.Logs))
		case fal.Completed:
			log.Print("done")
		}
	}),
)
```

The caller's context bounds the total time. If the context ends after submission,
the queued request is cancelled on a best-effort basis and the call returns a
`*TimeoutError` carrying the request id.

## Submit, reattach, and Result

For full control over the queue lifecycle, submit and hold the handle. Reattach
later by id with `Request`:

```go
req, err := client.Submit(ctx, "fal-ai/flux/schnell", input)
if err != nil {
	log.Fatal(err)
}
fmt.Println("request id:", req.ID)

// Later, in another process, rebuild the handle from the id.
req, err = client.Request("fal-ai/flux/schnell", req.ID)
if err != nil {
	log.Fatal(err)
}

// Result polls to completion, then fetches the response body.
raw, err := req.Result(ctx)
if err != nil {
	log.Fatal(err)
}
```

`Request.Events` returns an iterator over each observed `Status` for callers that
want to drive the poll loop themselves. `Request.Cancel` cancels a queued request.

## Stream

`Stream` returns a range-over-func iterator over server-sent events, one JSON
message per event:

```go
for event, err := range client.Stream(ctx, "fal-ai/flux/schnell", input) {
	if err != nil {
		log.Fatal(err)
	}
	fmt.Printf("%s\n", event)
}
```

The stream's lifetime is governed entirely by the context. Streamed responses are
never retried automatically.

## Upload

`Upload` sends a byte slice to fal storage and returns a URL. `UploadFile` streams
a file from disk without loading it into memory. Pass the returned URL as a model
input:

```go
url, err := client.Upload(ctx, data, "image/png")
if err != nil {
	log.Fatal(err)
}

raw, err := client.Run(ctx, "fal-ai/some-model", map[string]any{
	"image_url": url,
})
```

```go
url, err := client.UploadFile(ctx, "input.png")
```

`UploadImage` encodes an `image.Image` (JPEG by default, PNG via
`WithImageFormat`) and uploads it. Uploads accept `WithFileName`,
`WithRepository`, `WithFallback`, and `WithLifecycle` for retention and access
control.

## Encode

For small inputs, skip the upload entirely and pass a data URL. `Encode` builds a
base64 data URL from bytes; `EncodeFile` reads a file and infers its content type
from the extension:

```go
dataURL := fal.Encode(data, "image/png")

dataURL, err := fal.EncodeFile("input.png")
if err != nil {
	log.Fatal(err)
}

raw, err := client.Run(ctx, "fal-ai/some-model", map[string]any{
	"image_url": dataURL,
})
```

## Realtime

`Realtime` opens a WebSocket connection to an app. `Send` transmits an input and
`Recv` reads results until a clean close returns `io.EOF`:

```go
conn, err := client.Realtime(ctx, "fal-ai/some-realtime-app")
if err != nil {
	log.Fatal(err)
}
defer conn.Close()

if err := conn.Send(ctx, input); err != nil {
	log.Fatal(err)
}

for {
	msg, err := conn.Recv(ctx)
	if err == io.EOF {
		break
	}
	if err != nil {
		log.Fatal(err)
	}
	fmt.Printf("%s\n", msg)
}
```

A connection allows one `Send` goroutine alongside one `Recv` goroutine. Error
frames arrive from `Recv` as `*RealtimeError`.

## Error handling

Non-success HTTP responses surface as `*APIError`, which carries the status code,
message, error type, headers, and raw body. Inspect it with `errors.As`:

```go
var apiErr *fal.APIError
if errors.As(err, &apiErr) {
	log.Printf("status %d: %s", apiErr.StatusCode, apiErr.Message)
}
```

Check for missing credentials with `errors.Is`:

```go
if errors.Is(err, fal.ErrMissingCredentials) {
	log.Fatal("set FAL_KEY")
}
```

A request that outlives its context during `Subscribe` returns a `*TimeoutError`,
whose `RequestID` field lets the caller reattach and recover the result.

## Reliability

Requests retry automatically on transient failures: up to 10 attempts with
exponential backoff from 100ms (capped at 30s) plus jitter. Retries cover
408/409/429 responses, transport errors, and gateway errors that never reached
the fal application. Responses that carry a user-configured timeout are never
retried, and streamed responses are never retried. Request bodies are replayed
safely across attempts.

## Examples

Runnable programs live in [`examples/`](examples/), one per feature. Each reads
`FAL_KEY` from the environment. With [Task](https://taskfile.dev) installed and a
`.env` file present, the targets load credentials automatically:

| Target | What it does |
| --- | --- |
| `task example:run` | Synchronous inference, prints an image URL |
| `task example:subscribe` | Queue flow with live status updates |
| `task example:stream` | Server-Sent Events streaming |
| `task example:upload` | Storage upload, prints the file URL |
| `task example:realtime` | Realtime WebSocket round trip |
| `task example:web` | Browser playground, see below |

The web playground (`examples/webdemo`) serves a page on `0.0.0.0` with a model
picker and prompt form. It renders generated images in the browser and tracks
spend: live credit balance from the billing API, dollars spent since startup,
and billable units per request captured from response headers. Useful when the
SDK runs on a remote box and results need to be viewed from another machine.

## Development

```sh
task build          # compile all packages
task test           # unit tests (no network)
task test:race      # unit tests with the race detector
task lint           # golangci-lint
task test:integration  # live API tests, needs FAL_KEY and spends real credits
```

Unit tests run entirely against local test servers. The integration suite is
gated behind the `integration` build tag and kept deliberately cheap.

## License

[MIT](LICENSE).
