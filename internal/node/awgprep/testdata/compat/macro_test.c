/*
 * Test of the replacement block of compat.h (compatNewBlock in compat.go), without a kernel tree.
 *
 * compat_cc_test.go writes the block to block.h and builds this file three times, each time with one set of prototypes:
 *   SET_OLD      both functions take a struct socket * (kernels before the API change)
 *   SET_BACKPORT setup_udp_tunnel_sock takes a struct sock *, udp_tunnel_sock_release still a struct socket *
 *                (what Ubuntu 7.0.0-38 ships: one of the two changes was backported)
 *   SET_NEW      both take a struct sock *
 * and the program checks which pointer each call received. The block comes after the prototypes, as in compat.h, where
 * <net/udp_tunnel.h> is included first. -Werror: a wrong pointer type in the branch that is not chosen must not warn.
 */
struct net;
struct udp_tunnel_sock_cfg;
struct socket { int id; };
struct sock { struct socket *sk_socket; int id; };

static const void *got_setup, *got_release;

#if defined(SET_OLD)
void setup_udp_tunnel_sock(struct net *n, struct socket *s, struct udp_tunnel_sock_cfg *c) { (void)n; (void)c; got_setup = s; }
void udp_tunnel_sock_release(struct socket *s) { got_release = s; }
#define WANT_SETUP_SOCKET 1
#define WANT_RELEASE_SOCKET 1
#elif defined(SET_BACKPORT)
void setup_udp_tunnel_sock(struct net *n, struct sock *s, struct udp_tunnel_sock_cfg *c) { (void)n; (void)c; got_setup = s; }
void udp_tunnel_sock_release(struct socket *s) { got_release = s; }
#define WANT_SETUP_SOCKET 0
#define WANT_RELEASE_SOCKET 1
#elif defined(SET_NEW)
void setup_udp_tunnel_sock(struct net *n, struct sock *s, struct udp_tunnel_sock_cfg *c) { (void)n; (void)c; got_setup = s; }
void udp_tunnel_sock_release(struct sock *s) { got_release = s; }
#define WANT_SETUP_SOCKET 0
#define WANT_RELEASE_SOCKET 0
#else
#error "define SET_OLD, SET_BACKPORT or SET_NEW"
#endif

#include "block.h"

int main(void)
{
	struct socket sock = { 1 };
	struct sock sk = { &sock, 2 };
	struct sock *psk = &sk;
	struct net *netns = 0;      /* not called "net": the block must not depend on the name of its own parameters */
	struct udp_tunnel_sock_cfg *cfg = 0;

	setup_udp_tunnel_sock(netns, psk, cfg);
	udp_tunnel_sock_release(psk);
	if (got_setup != (WANT_SETUP_SOCKET ? (const void *)&sock : (const void *)&sk))
		return 1;
	if (got_release != (WANT_RELEASE_SOCKET ? (const void *)&sock : (const void *)&sk))
		return 2;
	return 0;
}
