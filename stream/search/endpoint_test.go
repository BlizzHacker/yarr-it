package main

import "testing"

// useEndpoint points e at url for the rest of the test and puts the old value
// back when it ends.
func useEndpoint(t testing.TB, e *endpoint, url string) {
	t.Helper()
	old := e.String()
	e.set(url)
	t.Cleanup(func() { e.set(old) })
}
