//go:build !linux

package main

import "errors"

// lazyDetach is Linux-only; the daemon runs in the Linux image.
func lazyDetach(string) error {
	return errors.New("lazy detach is only supported on Linux")
}
