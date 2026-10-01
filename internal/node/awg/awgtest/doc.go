// Package awgtest is the test harness for AmneziaWG on Linux: network namespaces with veth pairs, real
// amneziawg-go clients built from the pinned source (never a downloaded binary), and small probes (ping, a
// "who do you see me as" TCP server, iperf3). The engine tests in internal/node/awg use it; the end-to-end stand
// and the synthetic checks are meant to reuse it.
//
// Rules it enforces: it runs only when MG_ROOT_TESTS=1 and as root, only in WSL or on a
// throwaway VM, never on a fleet host; every namespace it creates is named with the prefix "mg3-awg-" so that
// several sessions sharing one WSL cannot delete each other's objects; processes are killed and namespaces
// removed at the end of each test, and leftovers of a crashed run (same prefix, no other owner) are removed on
// the next start. It never starts QEMU/KVM.
//
// Everything is in *_linux.go files; on other systems the package is empty and only compiles.
package awgtest
