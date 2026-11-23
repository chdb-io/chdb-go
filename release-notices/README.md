# Static engine release notices

Each directory holds the three files that `staticbundle package -notices`
embeds in every public static engine module: `THIRD_PARTY_NOTICES.txt`,
`SOURCE_PROVENANCE.json` and `STATIC_RELINKING.md`, for the engine release it
is named after.

This directory is its own Go module only so that the root module's zip does not
carry these files to every purego user. Nothing imports it, and it is not
tagged or published.

The files are generated from the pinned chdb-core sources; the per-file records
are in [docs/compliance](../docs/compliance). Do not edit them by hand.
