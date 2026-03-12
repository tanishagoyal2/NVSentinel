// Copyright (c) 2025, NVIDIA CORPORATION.  All rights reserved.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package helpers

import (
	"fmt"
	"testing"
	"time"
)

// TimedLogger wraps *testing.T and prepends elapsed time to every log line.
// Use NewTimedLogger at the start of a test or Assess step, then call
// tl.Log / tl.Logf in place of t.Log / t.Logf.
//
// Example output:
//
//	[T+0.00s] Phase 1: Immediate mode evicts pods immediately
//	[T+3.42s] All pods deleted from namespace immediate-test
type TimedLogger struct {
	t     *testing.T
	start time.Time
}

// NewTimedLogger returns a TimedLogger whose clock starts now.
func NewTimedLogger(t *testing.T) *TimedLogger {
	t.Helper()
	return &TimedLogger{t: t, start: time.Now()}
}

// Log prints msg prefixed with the elapsed time since the logger was created.
func (tl *TimedLogger) Log(msg string) {
	tl.t.Helper()
	tl.t.Logf("[T+%.2fs] %s", time.Since(tl.start).Seconds(), msg)
}

// Logf prints a formatted message prefixed with the elapsed time.
func (tl *TimedLogger) Logf(format string, args ...any) {
	tl.t.Helper()
	tl.t.Logf("[T+%.2fs] %s", time.Since(tl.start).Seconds(), fmt.Sprintf(format, args...))
}

// Elapsed returns how long has passed since the logger was created.
func (tl *TimedLogger) Elapsed() time.Duration {
	return time.Since(tl.start)
}
