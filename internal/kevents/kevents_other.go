//go:build !linux || !(386 || amd64 || arm || arm64 || loong64 || mips64le || mipsle || ppc64le || riscv64)

package kevents

import (
	"errors"

	"github.com/yuuki/tcpulse/internal/stats"
)

// Supported reports whether Start can work on this platform: it cannot.
const Supported = false

// Tracer is not supported on this platform.
type Tracer struct{}

// Start is not supported on this platform.
func Start() (*Tracer, error) { return nil, errors.ErrUnsupported }

// Generation is not supported on this platform.
func (*Tracer) Generation() (uint64, bool) { return 0, false }

// GenerationError reports that Generation is not supported.
func (*Tracer) GenerationError() error { return errors.ErrUnsupported }

// DropsError reports that drops are not supported.
func (*Tracer) DropsError() error { return errors.ErrUnsupported }

// DropReasons returns nil.
func (*Tracer) DropReasons() map[uint32]string { return nil }

// ReadEvents is not supported on this platform.
func (*Tracer) ReadEvents(int) (stats.Events, error) {
	return stats.Events{}, errors.ErrUnsupported
}

// Close does nothing.
func (*Tracer) Close() error { return nil }
