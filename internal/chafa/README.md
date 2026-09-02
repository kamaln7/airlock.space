# internal/chafa

A pure-Go port of the sextant rendering path of [chafa](https://hpjansson.org/chafa/)
1.19.0 — byte-for-byte identical output, with no cgo and no libchafa.

Vendored from `github.com/kamaln7/go-chafa`. The upstream checkout carries what
does not belong in this repository: a C oracle that links the real libchafa, a
fidelity harness that renders several million cells through both and compares
them byte for byte, and the sample images it does that with. Changes belong
upstream first, where the oracle can prove they changed nothing, and are copied
here after.

What is ported is only what this program asks for: sextant symbols, the four
canvas modes, an image in and an ANSI string out. Not other symbol sets, not
sixel or kitty output, not dithering, not animation.
