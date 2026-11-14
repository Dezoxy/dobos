package daemon

import (
	"context"
	"flodni/internal/gostorm/native"
)

// engineBridge is the part of the engine client the read path calls. The package var
// nativeBridge holds it so tests can swap in a fake; production holds a nativeEngine.
//
// nativeBridge == nil means "no engine configured": several paths test for it. Assign it
// only from a non-nil client, never from a nil *native.NativeClient, which would make a
// non-nil interface around a nil pointer.
type engineBridge interface {
	FetchBlock(hash string, fileID int, offset int64, p []byte) (int, error)
	FetchAhead(hash string, fileID int, offset int64, buf, dest []byte, onFill func(n int, done bool, err error)) (int, error)
	NewStreamReader(hash string, fileID int, totalSize int64) streamReader
	Wake(ctx context.Context, magnetUrl string, fileIdx int) error
	ListTorrents() ([]native.TorrentStats, error)
	CleanupHashes() int
}

// streamReader is the part of *native.NativeReader the pump and the handle use.
type streamReader interface {
	ReadAt(p []byte, off int64) (int, error)
	Close() error
	Interrupt()
	SetPieceLen(n int64)
}

// nativeEngine adapts *native.NativeClient to engineBridge. Only NewStreamReader needs
// the adapter: the client returns the concrete *native.NativeReader, and Go does not
// treat a method with a concrete result as one with an interface result. This keeps
// package native untouched.
type nativeEngine struct{ *native.NativeClient }

func (e nativeEngine) NewStreamReader(hash string, fileID int, totalSize int64) streamReader {
	return e.NativeClient.NewStreamReader(hash, fileID, totalSize)
}
