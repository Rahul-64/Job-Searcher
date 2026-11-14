// Package raw compresses original source payloads for storage in posting_raw.
package raw

import (
	"bytes"
	"compress/gzip"
	"fmt"
	"io"
)

const maxDecompressed = 64 << 20

// Compress gzips b.
func Compress(b []byte) ([]byte, error) {
	var buf bytes.Buffer
	zw := gzip.NewWriter(&buf)
	if _, err := zw.Write(b); err != nil {
		return nil, err
	}
	if err := zw.Close(); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// Decompress reverses Compress. It refuses to expand beyond 64 MiB.
func Decompress(b []byte) ([]byte, error) {
	zr, err := gzip.NewReader(bytes.NewReader(b))
	if err != nil {
		return nil, err
	}
	defer zr.Close()
	out, err := io.ReadAll(io.LimitReader(zr, maxDecompressed+1))
	if err != nil {
		return nil, err
	}
	if len(out) > maxDecompressed {
		return nil, fmt.Errorf("decompressed payload exceeds %d bytes", maxDecompressed)
	}
	return out, nil
}
