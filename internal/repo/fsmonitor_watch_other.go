//go:build !linux && !windows && !(darwin && cgo)
// +build !linux
// +build !windows
// +build !darwin !cgo

package repo

// newEventWatcher covers every platform without an event backend: the BSDs and
// other Unixes, plan9, and macOS when built without cgo, where FSEvents is out
// of reach.
//
// The three legacy +build lines are not redundant. Comma means AND and space
// means OR, so "!(darwin && cgo)" cannot be written on one line, and the three
// conditions together are what keep this file's complement exactly the set
// covered by the linux, windows, and darwin+cgo files — leaving any platform
// with no definition of newEventWatcher at all, which is a build failure
// rather than a fallback.
//
// There is no event API to use here, so the answer is simply that there is
// none: callers fall back to polling, which is correct everywhere.
func newEventWatcher() (watcher, error) {
	return nil, errNoEventBackend
}
