package warmup

import (
	"errors"
	"os"
	"sync/atomic"
	"syscall"

	"golang.org/x/sys/unix"
)

// seekFn is unix.Seek; a test swaps it to stand in for a filesystem without SEEK_DATA.
var seekFn = unix.Seek

// seekUnsupportedLogged keeps the "no SEEK_HOLE here" line to one per process.
var seekUnsupportedLogged atomic.Bool

// headHasHoles reports whether a head file has a hole anywhere in [0, size). A dense
// head has one data extent that starts at 0 and reaches the end. A filesystem without
// SEEK_DATA/SEEK_HOLE (EINVAL) cannot tell, and the head counts as dense: keeping a good
// head is the safe error, and the line says so once.
func headHasHoles(path string) (bool, error) {
	f, err := os.Open(path)
	if err != nil {
		return false, err
	}
	defer func() { _ = f.Close() }()
	fi, err := f.Stat()
	if err != nil {
		return false, err
	}
	size, fd := fi.Size(), int(f.Fd())
	if size == 0 {
		return false, nil
	}
	data, err := seekFn(fd, 0, unix.SEEK_DATA)
	if err != nil {
		if errors.Is(err, syscall.ENXIO) {
			return true, nil // no data at all: the whole file is one hole
		}
		return false, seekErr(err)
	}
	if data != 0 {
		return true, nil
	}
	hole, err := seekFn(fd, 0, unix.SEEK_HOLE)
	if err != nil {
		return false, seekErr(err)
	}
	return hole < size, nil
}

// seekErr turns EINVAL into "dense, unknown" (nil) and logs it once.
func seekErr(err error) error {
	if errors.Is(err, syscall.EINVAL) {
		if seekUnsupportedLogged.CompareAndSwap(false, true) {
			logf.Printf("[DiskWarmup] SEEK_HOLE is not supported on this filesystem: heads are assumed dense")
		}
		return nil
	}
	return err
}
