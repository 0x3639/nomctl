//go:build !linux

package doctor

import "errors"

func readClock() Check { return clockCheck(false, 0, errors.New("kernel clock status requires Linux")) }
