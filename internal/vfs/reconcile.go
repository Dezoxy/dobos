package vfs

// SizeLimits bounds the declared size of a virtual stub.
type SizeLimits struct {
	Min int64
	Max int64
}

// The movie floor exists to catch a truncated 4K remux.
var VideoSizeLimits = SizeLimits{Min: MinFileSize, Max: MaxFileSize}

// ReadMetadataFromFileWithLimits reads a virtual stub, validating its declared
// size against limits. Format handling matches ReadMetadataFromFile.
func ReadMetadataFromFileWithLimits(path string, limits SizeLimits) (*FileMetadata, error) {
	return readMetadataFromFile(path, limits)
}
