package main

import "syscall"

// lazyDetach detaches the mount now and cleans it up when it is no longer
// busy, so a hung unmount cannot hold shutdown.
func lazyDetach(mountPath string) error {
	return syscall.Unmount(mountPath, syscall.MNT_DETACH)
}
