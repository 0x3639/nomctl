//go:build unix

package analytics

import "syscall"

const noFollow = syscall.O_NOFOLLOW
