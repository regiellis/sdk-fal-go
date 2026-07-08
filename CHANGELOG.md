# Changelog

All notable changes to this project are documented here. The format follows
[Keep a Changelog](https://keepachangelog.com/en/1.1.0/), and this project adheres
to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [0.1.0] - 2026-07-08

Initial release.

### Added

- `Client` with functional-option construction (`NewClient`, `WithKey`,
  `WithCredentials`, `WithHTTPClient`, `WithTimeout`, `WithUserAgent`, and base-URL
  overrides for the run, queue, REST, and CDN hosts).
- Lazy credential resolution from `FAL_KEY`, the `FAL_KEY_ID`/`FAL_KEY_SECRET`
  pair, and fal CLI tokens with automatic Auth0 refresh; `ErrMissingCredentials`
  when none resolve.
- `Run` for synchronous inference, returning the raw JSON response.
- Queue support: `Submit`, `Request` reattachment by id, `Request.Status`,
  `Request.Events` as a range-over-func iterator, `Request.Result`, and
  `Request.Cancel`.
- `Subscribe` with `OnEnqueue` and `OnUpdate` callbacks, `WithLogs`, and
  `WithPollInterval`; best-effort cancellation with `TimeoutError` when the
  context ends after submission.
- Per-call options: `WithPath`, `WithHint`, `WithStartTimeout`, `WithWebhook`,
  and `WithPriority`.
- `Stream` for Server-Sent Events, yielding one JSON message per event as a
  range-over-func iterator.
- Storage: `Upload`, `UploadFile` (streamed from disk, multipart above 100MB),
  and `UploadImage`, with `WithFileName`, `WithRepository`, `WithFallback`,
  `WithLifecycle`, and `WithImageFormat`.
- `Encode` and `EncodeFile` for base64 data URLs.
- `Realtime` WebSocket connections with `Send`, `Recv`, and `Close`, msgpack
  encoding, short-lived token minting, and structured `RealtimeError` frames.
- Typed errors (`APIError`, `TimeoutError`, `RealtimeError`,
  `ErrMissingCredentials`) compatible with `errors.Is` and `errors.As`.
- Automatic retry with exponential backoff and jitter for transient failures.

[0.1.0]: https://github.com/regiellis/sdk-fal-go/releases/tag/v0.1.0
