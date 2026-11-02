Inline subsets for the public user page (one self-contained HTML, see ../../../vite.sub.config.ts).
Derived from ../../fonts (Onest, JetBrains Mono; SIL OFL 1.1, the license texts are next to the sources) with fontTools:

    onest-*.woff2            pyftsubset --unicodes=<ASCII + punctuation | basic Cyrillic> --flavor=woff2 --no-hinting
                             (the variable wght 400-800 axis is kept)
    jetbrains-mono-*.woff2   wght pinned to 700 (varLib.instancer), then pyftsubset: ASCII + the Cyrillic capitals of the size units
    vazirmatn-arabic.woff2   Vazirmatn (rastikerdar/vazirmatn, SIL OFL 1.1: OFL-Vazirmatn.txt here, there is no source copy in
                             ../../fonts) as released: misc/Non-Latin/fonts/webfonts/Vazirmatn-NL[wght].woff2, the Arabic-script
                             letters only (made to pair with a Latin face), variable wght 100-900; the page asks for 400-800

Glyphs outside the subsets (arrows, check marks) fall back to the system font.
