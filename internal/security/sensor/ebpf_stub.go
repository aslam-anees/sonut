//go:build !linux

package sensor

// The real implementation is isolated behind ebpf_linux.go so importing the
// security package never makes macOS or Windows require Linux kernel APIs.
