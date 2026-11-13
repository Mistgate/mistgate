# Module source fixtures (tests only)

The three `.h` files are unmodified `compat/compat.h` files of the AmneziaWG kernel module
(<https://github.com/amnezia-vpn/amneziawg-linux-kernel-module>), licensed GPL-2.0; the licence header is inside each file.
They are test inputs for `FixCompat` and are not built or shipped. Do not reformat them: the tests pin their sha256.
Git treats them as binary (`.gitattributes`) so line endings stay as they are.

| File | What it is | sha256 (first 12) |
|---|---|---|
| `ppa-1.0.0.h` | `amneziawg-dkms` 1.0.0 from the Amnezia PPA (the same file as upstream tag `v3.1.20260906`) | `b14346040ce0` |
| `handfixed.h` | the same file with the first of the two macro lines deleted by hand | `e308aa5c04ce` |
| `upstream-v3.1.20261113.h` | `src/compat/compat.h` of upstream tag `v3.1.20261113`, which carries the upstream fix (commit `70ae2481`) | `b782a6001123` |

`C` tests of the replacement macros are in `macro_test.c`.
