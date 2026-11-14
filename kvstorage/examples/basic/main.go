//go:build tinygo && (esp32c3 || esp32s3)

package main

import (
	"time"

	"github.com/Z00mZE/esperetools/kvstorage"
	"github.com/google/uuid"
	"github.com/pkg/errors"
)

func main() {
	time.Sleep(time.Second)
	tries := 10
	// Give the USB serial monitor time to connect after reset.
	for range tries {
		println("start")
		if err := run(); err != nil {
			println("kvstorage error:", err.Error())
			continue
		}

		println("finish")
	}
}

func run() error {
	s, err := kvstorage.Open()
	if err != nil {
		return errors.Wrap(err, "kvstorage.Open")
	}
	// Read first. A later boot must not overwrite settings with defaults.
	name, found, err := s.GetString("name")
	if err != nil {
		return errors.Wrap(err, "kvstorage.GetString")
	}

	n := uuid.NewString()
	println("founded", found, "old", name, "new", n)
	if err := s.SetString("name", n); err != nil {
		return errors.Wrap(err, "kvstorage.SetString")
	}
	println("Saved name. Disconnect power, reconnect, and run the monitor.")
	return nil
}
