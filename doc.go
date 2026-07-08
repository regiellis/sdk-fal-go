// Package fal is a Go client for the fal.ai inference platform.
//
// It provides a single [Client] type whose methods run models synchronously,
// submit and poll queued requests, stream results, upload files, and open
// realtime connections. Every network operation takes a [context.Context] as
// its first argument and honors cancellation, including between retry attempts.
//
// # Authentication
//
// Credentials are resolved lazily on first use, in this order:
//
//  1. The FAL_KEY environment variable.
//  2. FAL_KEY_ID together with FAL_KEY_SECRET.
//  3. Tokens saved by the fal CLI in $FAL_HOME_DIR/auth0_token (or
//     ~/.fal/auth0_token), refreshed automatically when close to expiry.
//
// Setting FAL_FORCE_AUTH_BY_USER=1 skips the environment-key steps and forces
// use of the saved CLI tokens. When no credentials resolve, the first call that
// needs them returns [ErrMissingCredentials].
//
// Supply credentials explicitly with [WithKey] or a custom [CredentialsProvider]
// through [WithCredentials].
//
// # Errors
//
// Non-success HTTP responses surface as [*APIError], which carries the status
// code, message, error type, headers, and raw body. Use [errors.As] to inspect
// it and [errors.Is] against [ErrMissingCredentials].
package fal
