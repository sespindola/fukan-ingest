# Cable baseline dataset

Clean-room submarine cable dataset for Fukan. Two JSONL files:

- **cables.jsonl** — one record per cable (metadata: name, owners, RFS year,
  length, alt names, source provenance)
- **landings.jsonl** — one record per (cable, landing) pair (name, country,
  coordinates, sources)

## Source policy

- **Wikidata** (CC0) — cable identity (Q-id), aliases, RFS year (P571),
  length (P2043), owners (P127), Wikipedia sitelinks
- **Wikipedia** (CC-BY-SA 3.0, facts extractable) — landing list parsed from
  infoboxes / section bodies / wikitables / definition lists
- **Wikipedia-linked place entities on Wikidata** (CC0) — coordinates
  (P625) and country code (P17 → P297) for each landing

No data from submarinecablemap.com, TeleGeography, or any restricted
dataset. Every cable carries ≥2 source URLs (Wikidata + Wikipedia).

## How this was built

`build_cable_dataset.py` is the one-off builder kept here for audit and
reproducibility:

1. SPARQL query against `query.wikidata.org`:
   `?cable wdt:P31/wdt:P279* wd:Q506572` (submarine communications cable)
2. For each cable with an English Wikipedia sitelink, fetch the article
   wikitext via the MediaWiki API and extract landing wikilinks using a
   union of strategies:
   - `landings` / `landing_points` infobox fields
   - `== Landing points ==` section bodies (any heading level)
   - `;Landing points` definition-list markers
   - `has landings in:` prose patterns followed by bullets
   - Bulleted lists where each row has a wikilink + country suffix
   - Wikitables whose first column is a wikilinked place
   - Lead-paragraph fallback for short articles
3. For each landing wikilink, resolve Wikipedia title → Wikidata Q-id
   (pageprops API), then fetch P625 (coords) and P17 (country).
4. Drop entities whose P31 is only ever a country, continent, ocean, sea,
   US/Australian state, or similar non-landing type.

Rerun with `python3 build_cable_dataset.py /tmp/out` and copy the resulting
`cables.jsonl` / `landings.jsonl` into this directory to refresh.

## Rules for manual edits

- Every fact must have a source URL in the `sources` array with a
  known-compatible license (CC0, CC-BY, CC-BY-SA, public-domain, or a
  verifiable public record such as an FCC filing).
- Do not add facts sourced from submarinecablemap.com, TeleGeography
  publications, Infrapedia, or any paywalled dataset.
- When a cable is added by hand, set `cable_id` to its Wikidata Q-id if
  one exists, else a stable kebab-case slug (`sea-me-we-6`).
- Landings without a Wikidata entity can be included with
  `"landing_id": "{cable_id}:<slug>"` and `"sources"` pointing at the
  operator page / FCC filing the coords came from.
