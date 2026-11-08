//go:build !unix

package fsx

import (
	"errors"
	"os"
)

func WriteRuntimeFile(string, []byte, os.FileMode) error {
	return errors.New("runtime files require a Unix host")
}

func ReadRuntimeFile(string, int64) ([]byte, error) {
	return nil, errors.New("runtime files require a Unix host")
}
