package ownprobe

import (
	"io"
	"os"
	"os/exec"
	"syscall"
	"time"
)

// The script leaves processes behind on purpose: "start" puts the supervisor in the background, "stop" only signals it and its
// worker, and a worker that ends on a signal does not wait for the curls of the cycle it was in. A test can wait for the pids the
// pid files name, but not for those: the workers of earlier starts, their curls, a sleep. One of them still wrote
// fake/calls while the directory of the test was being removed ("TempDir RemoveAll cleanup: directory not empty").
//
// So every process a lab starts joins one process group and inherits the write end of one pipe, and so does everything it
// forks. When the test ends the group is killed, and the read end reports the end of the file once the last of them has closed
// its copy, which is when it is gone. This is registered after the temporary directory of the test, so it runs before the removal.
func (l *lab) startGroup() {
	l.t.Helper()
	r, w, err := os.Pipe()
	must(l.t, err)
	// The leader does nothing; it lives as long as the lab, so that the number of the group cannot go to a stranger's process.
	leader := exec.Command("cat")
	leader.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	_, err = leader.StdinPipe()
	must(l.t, err)
	must(l.t, startCmd(leader))
	l.group, l.hold = leader.Process.Pid, w
	l.t.Cleanup(func() {
		_ = syscall.Kill(-l.group, syscall.SIGKILL)
		_ = leader.Wait()
		_ = w.Close()
		// The deadline only turns a process that left the group (none does) into a failure with a name instead of a hung run.
		_ = r.SetReadDeadline(time.Now().Add(time.Minute))
		if _, err := r.Read(make([]byte, 1)); err != io.EOF {
			l.t.Errorf("a process of the lab outlived the kill of its group: %v", err)
		}
		_ = r.Close()
	})
}

// own makes c, and what c leaves behind, a process the lab ends with the test. Every start of the script or of a shell in a
// lab goes through it.
func (l *lab) own(c *exec.Cmd) {
	c.SysProcAttr = &syscall.SysProcAttr{Setpgid: true, Pgid: l.group}
	c.ExtraFiles = []*os.File{l.hold}
}
