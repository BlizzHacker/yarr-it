package main

import "sync/atomic"

// endpoint is an upstream URL a test can point at a stub server.
//
// It is atomic rather than a plain string var because some fetches outlive the
// request that started them: the linear-channel pool refresh and the type-ahead
// cache warm are both detached on purpose. Such a goroutine can still be
// reading the URL when a test, this one or the next, swaps it, which is a data
// race. Production sets each endpoint once, at init, and only ever reads it.
type endpoint struct{ url atomic.Pointer[string] }

func newEndpoint(url string) *endpoint {
	e := &endpoint{}
	e.set(url)
	return e
}

func (e *endpoint) String() string { return *e.url.Load() }

func (e *endpoint) set(url string) { e.url.Store(&url) }
