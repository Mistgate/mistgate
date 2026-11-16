package bridge

import (
	"fmt"
	"os"
	"runtime"
	"testing"
	"time"
)

func TestMain(m *testing.M) {
	baseline := runtime.NumGoroutine()
	code := m.Run()
	if code == 0 {
		deadline := time.Now().Add(3 * time.Second)
		stable := 0
		for time.Now().Before(deadline) {
			if runtime.NumGoroutine() <= baseline+2 {
				stable++
				if stable >= 5 {
					os.Exit(code)
				}
			} else {
				stable = 0
			}
			time.Sleep(100 * time.Millisecond)
		}
		fmt.Fprintf(os.Stderr, "review test 16: goroutines did not settle: baseline=%d final=%d\n", baseline, runtime.NumGoroutine())
		code = 1
	}
	os.Exit(code)
}
