package fal

// Per-request header names understood by the fal gateway. They are shared
// across the run, queue, and storage operations built on top of this package.
const (
	// headerRequestTimeout sets the server-side request timeout in seconds
	// (a floating-point value that must be greater than one second).
	headerRequestTimeout = "X-Fal-Request-Timeout"
	// headerRequestTimeoutType identifies the kind of a server-set timeout. Its
	// presence on a 504 response marks a user-configured timeout that must not
	// be retried.
	headerRequestTimeoutType = "X-Fal-Request-Timeout-Type"
	// headerRunnerHint requests a specific runner for the call.
	headerRunnerHint = "X-Fal-Runner-Hint"
	// headerQueuePriority sets the queue priority ("normal" or "low").
	headerQueuePriority = "X-Fal-Queue-Priority"

	// headerFalRequestID is returned by the fal server on responses that
	// originate from the application rather than the ingress proxy.
	headerFalRequestID = "X-Fal-Request-Id"
	// headerFalErrorType carries an error classification on failed responses.
	headerFalErrorType = "X-Fal-Error-Type"

	// headerFileName reports an uploaded object's file name to the CDN.
	headerFileName = "X-Fal-File-Name"
	// headerObjectLifecycle carries the lifecycle preference JSON payload on
	// storage uploads.
	headerObjectLifecycle = "X-Fal-Object-Lifecycle"
	// headerObjectLifecyclePref carries the same lifecycle payload under the
	// preference header name; the backend expects both.
	headerObjectLifecyclePref = "X-Fal-Object-Lifecycle-Preference"
)
