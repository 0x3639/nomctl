//go:build linux

package doctor

import (
	"syscall"
	"time"
)

func readClock() Check {
	// Modes=0 queries the kernel discipline without changing the clock.
	var tx syscall.Timex
	state, err := syscall.Adjtimex(&tx)
	const staUnsync, staNano, timeError = 0x40, 0x2000, 5
	unit := time.Microsecond
	if tx.Status&staNano != 0 {
		unit = time.Nanosecond
	}
	return clockCheck(tx.Status&staUnsync == 0 && state != timeError, time.Duration(tx.Offset)*unit, err)
}
