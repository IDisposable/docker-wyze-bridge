package gwell

import "errors"

// ErrDisabled is returned when the Gwell integration is asked to
// start/connect but has been disabled via configuration.
var ErrDisabled = errors.New("gwell: integration disabled")

// ErrNoAuth is returned when a producer Connect is attempted without
// a valid Wyze access_token in the auth state. Callers should make
// sure wyzeapi.Client.EnsureAuth() has succeeded first.
var ErrNoAuth = errors.New("gwell: no Wyze auth token available")
