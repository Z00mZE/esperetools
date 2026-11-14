//go:build tinygo && (esp32c3 || esp32s3)

package main

import (
	"time"

	"github.com/Z00mZE/esperetools/kvstorage"
)

func main() {
	time.Sleep(3 * time.Second)
	if err := run(); err != nil {
		println("kvstorage:", err.Error())
	}
	for {
		time.Sleep(time.Hour)
	}
}

func run() error {
	s, err := kvstorage.Open()
	if err != nil {
		return err
	}
	boots, _, err := s.Get[uint64]("boots")
	if err != nil {
		return err
	}
	if err := s.Set[uint64]("boots", boots+1); err != nil {
		return err
	}
	name, found, err := s.Get[string]("name")
	if err != nil {
		return err
	}
	if !found {
		name = "ESP32"
		if err := s.Set[string]("name", name); err != nil {
			return err
		}
	}
	if err := s.Set[bool]("enabled", true); err != nil {
		return err
	}
	if _, found, err := s.Get[[]uint16]("ports"); err != nil {
		return err
	} else if !found {
		if err := s.Set[[]uint16]("ports", []uint16{80, 443}); err != nil {
			return err
		}
	}
	ports, _, err := s.Get[[]uint16]("ports")
	if err != nil {
		return err
	}
	enabled, _, err := s.Get[bool]("enabled")
	if err != nil {
		return err
	}
	println("name:", name, "boot:", boots+1, "enabled:", enabled, "ports:", len(ports))
	return nil
}
