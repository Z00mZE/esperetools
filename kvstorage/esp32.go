//go:build tinygo && (esp32c3 || esp32s3)

package kvstorage

import "machine"

func flashOffset() (int64, error) {
	if machine.Flash.Size() < DefaultSize {
		return 0, ErrInvalidFlash
	}
	return machine.Flash.Size() - DefaultSize, nil
}

// Open opens the last DefaultSize bytes of machine.Flash. With the installed
// generic ESP32-C3/S3 targets this is a fixed physical region at the end of the
// first 4 MiB, independent of small changes to firmware size. It is NOT the end
// of the physical chip if the chip is larger. Reserve this region for kvstorage.
func Open() (*Store, error) {
	off, err := flashOffset()
	if err != nil {
		return nil, err
	}
	return NewStore(machine.Flash, off, DefaultSize)
}

// FormatFlash explicitly erases kvstorage's default 8 KiB region, deleting all
// keys. Use only to initialize a region whose previous contents may be discarded.
func FormatFlash() (*Store, error) {
	off, err := flashOffset()
	if err != nil {
		return nil, err
	}
	return Format(machine.Flash, off, DefaultSize)
}
