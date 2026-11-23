//go:build !unix

package database

import "math"

func platformAvailableDiskBytes(string) (uint64, error) {
	return math.MaxUint64, nil
}
