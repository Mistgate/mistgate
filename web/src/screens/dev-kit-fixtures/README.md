Real template sites for the dev-kit scenes of the "From a template" window (`dev-kit-decoys.tsx`). Each file is one site as the
panel's preview sends it: a self-contained HTML document of the home page. Development only; nothing here is imported by the
panel (the dev-kit is not in production builds).

    go run ./tools/decoyfixtures web/src/screens/dev-kit-fixtures/sites gb "status files login blog portfolio business docs card club project"
    go run ./tools/decoyfixtures web/src/screens/dev-kit-fixtures/sites de blog 1

The seeds are fixed, so the same call writes the same files; write them again when the generator's look changes.
