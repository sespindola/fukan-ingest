"""
Build cables.jsonl + landings.jsonl for fukan from Wikidata + Wikipedia.

Sources (per the approved plan):
- Wikidata (CC0)                                      — cable metadata (SPARQL)
- Wikipedia (CC-BY-SA, facts extractable)             — landing lists (article body)
- Linked place entities on Wikidata (CC0)             — landing coordinates

No data from submarinecablemap.com or other restricted sources.
"""

from __future__ import annotations

import json
import re
import sys
import time
import unicodedata
import urllib.parse
from collections import defaultdict
from concurrent.futures import ThreadPoolExecutor, as_completed
from pathlib import Path
from typing import Any, Dict, Iterable, List, Optional, Set, Tuple

import urllib.request

UA = "fukan-ingest/1.0 (+https://github.com/sespindola/fukan) cable-dataset-builder"
SPARQL_URL = "https://query.wikidata.org/sparql"
WIKIPEDIA_API = "https://en.wikipedia.org/w/api.php"
WIKIDATA_API = "https://www.wikidata.org/w/api.php"

SECTION_RE = re.compile(r"^==+\s*([^=]+?)\s*==+\s*$", re.MULTILINE)
WIKILINK_RE = re.compile(r"\[\[([^\]|#]+?)(?:#[^|\]]*)?(?:\|[^\]]*)?\]\]")
COUNTRY_AFTER_LINK_RE = re.compile(
    r"\[\[[^\]]+?\]\][,\s]*(?:(?:in)\s+)?([A-Za-z][A-Za-z \-.']{1,40})"
)

SKIP_LINK_PREFIXES = (
    "File:", "Image:", "Category:", "wikt:", "Help:", "Wikipedia:", ":", "#",
)
SKIP_LINK_EXACT = {
    "cable landing point", "Cable landing point", "Submarine communications cable",
    "Submarine cable", "Optical fiber", "Fibre-optic communication",
    "Landing point", "Cable landing station", "Submarine cable system",
    "List of international submarine communications cables",
}

# Sections whose wikilinks we want (landing lists usually live here)
LANDING_SECTION_RE = re.compile(
    r"^(landing[s]?|landing points?|stations?|cable stations?|"
    r"cable landing (?:points|stations)|route|routes|termin(?:us|als|i)|points of presence)$",
    re.IGNORECASE,
)


def http_get(url: str, retries: int = 3, timeout: int = 30) -> bytes:
    req = urllib.request.Request(url, headers={"User-Agent": UA})
    last: Optional[Exception] = None
    for i in range(retries):
        try:
            with urllib.request.urlopen(req, timeout=timeout) as r:
                return r.read()
        except Exception as e:  # noqa: BLE001
            last = e
            time.sleep(1 + i)
    raise RuntimeError(f"GET failed: {url}: {last}")


def http_post(url: str, body: bytes, headers: Dict[str, str], retries: int = 3, timeout: int = 60) -> bytes:
    req = urllib.request.Request(url, data=body, headers={"User-Agent": UA, **headers})
    last: Optional[Exception] = None
    for i in range(retries):
        try:
            with urllib.request.urlopen(req, timeout=timeout) as r:
                return r.read()
        except Exception as e:  # noqa: BLE001
            last = e
            time.sleep(1 + i)
    raise RuntimeError(f"POST failed: {url}: {last}")


def sparql(query: str) -> List[Dict[str, Dict[str, str]]]:
    body = urllib.parse.urlencode({"query": query}).encode()
    headers = {
        "Accept": "application/sparql-results+json",
        "Content-Type": "application/x-www-form-urlencoded",
    }
    data = json.loads(http_post(SPARQL_URL, body, headers))
    return data["results"]["bindings"]


def fetch_cable_seed() -> Dict[str, Dict[str, Any]]:
    """Return {qid: {name, aliases, length_km, inception, owners, wp_title, scm_slug, official}}.
    Owners are Q-ids here; we resolve labels separately."""
    print("SPARQL: cable list + core metadata", file=sys.stderr)
    q = """
    SELECT ?cable ?cableLabel ?length_km ?inception
           ?owner ?ownerLabel ?wp ?scmSlug ?official WHERE {
      ?cable wdt:P31/wdt:P279* wd:Q506572 .
      OPTIONAL { ?cable wdt:P2043 ?length_km . }
      OPTIONAL { ?cable wdt:P571 ?inception . }
      OPTIONAL { ?cable wdt:P127 ?owner . }
      OPTIONAL { ?cable wdt:P13628 ?scmSlug . }
      OPTIONAL { ?cable wdt:P856 ?official . }
      OPTIONAL {
        ?wp schema:about ?cable ; schema:isPartOf <https://en.wikipedia.org/> .
      }
      SERVICE wikibase:label { bd:serviceParam wikibase:language "en" . }
    }
    """
    rows = sparql(q)
    out: Dict[str, Dict[str, Any]] = {}
    for r in rows:
        qid = r["cable"]["value"].rsplit("/", 1)[-1]
        rec = out.setdefault(qid, {
            "qid": qid,
            "name": None,
            "length_km": None,
            "inception": None,
            "owners": [],   # {qid, label}
            "wp_url": None,
            "wp_title": None,
            "scm_slug": None,
            "official_url": None,
        })
        lbl = r.get("cableLabel", {}).get("value")
        if lbl and lbl != qid and not rec["name"]:
            rec["name"] = lbl
        if "length_km" in r and not rec["length_km"]:
            try:
                rec["length_km"] = int(float(r["length_km"]["value"]))
            except (ValueError, KeyError):
                pass
        if "inception" in r and not rec["inception"]:
            rec["inception"] = r["inception"]["value"]  # ISO date string
        if "owner" in r:
            owner_qid = r["owner"]["value"].rsplit("/", 1)[-1]
            owner_lbl = r.get("ownerLabel", {}).get("value") or owner_qid
            if not any(o["qid"] == owner_qid for o in rec["owners"]):
                rec["owners"].append({"qid": owner_qid, "label": owner_lbl})
        if "wp" in r and not rec["wp_url"]:
            rec["wp_url"] = r["wp"]["value"]
            rec["wp_title"] = urllib.parse.unquote(
                r["wp"]["value"].rsplit("/wiki/", 1)[-1]
            ).replace("_", " ")
        if "scmSlug" in r and not rec["scm_slug"]:
            rec["scm_slug"] = r["scmSlug"]["value"]
        if "official" in r and not rec["official_url"]:
            rec["official_url"] = r["official"]["value"]
    return out


def fetch_wikipedia_articles(titles: List[str]) -> Dict[str, str]:
    """Returns {title: wikitext} using MediaWiki action=query|prop=revisions, 50 at a time."""
    out: Dict[str, str] = {}
    for i in range(0, len(titles), 50):
        chunk = titles[i:i + 50]
        # Use | joined and URL-encoded titles
        params = {
            "action": "query",
            "prop": "revisions",
            "rvprop": "content",
            "rvslots": "main",
            "titles": "|".join(chunk),
            "format": "json",
            "formatversion": "2",
            "redirects": "1",
        }
        url = WIKIPEDIA_API + "?" + urllib.parse.urlencode(params)
        data = json.loads(http_get(url))
        # Redirects mapping
        redirects = {r["from"]: r["to"] for r in data.get("query", {}).get("redirects", [])}
        # title -> page mapping
        pages = data.get("query", {}).get("pages", [])
        page_by_title: Dict[str, Any] = {}
        for p in pages:
            page_by_title[p.get("title")] = p
        for t in chunk:
            final = redirects.get(t, t)
            p = page_by_title.get(final)
            if not p or "missing" in p:
                continue
            revs = p.get("revisions", [])
            if not revs:
                continue
            wikitext = revs[0].get("slots", {}).get("main", {}).get("content", "")
            if wikitext:
                out[t] = wikitext
        time.sleep(0.3)
    return out


INFOBOX_LANDINGS_RE = re.compile(
    r"\|\s*(?:landing_points?|landings|cable_landings)\s*=\s*([\s\S]+?)(?=\n\s*\||\n\s*\}\})",
    re.IGNORECASE,
)
DEF_LIST_LANDINGS_RE = re.compile(
    r"^;\s*(?:Landing points?|Landings|Cable stations?|Cable landing points?)\s*\n((?:[*#:][^\n]+\n)+)",
    re.IGNORECASE | re.MULTILINE,
)
INLINE_LANDINGS_RE = re.compile(
    r"(?:has|have|with)\s+landings?\s+(?:in|at)[^\n:]{0,300}:\s*\n((?:\s*[*#][^\n]+\n){2,})",
    re.IGNORECASE,
)
REF_TAG_RE = re.compile(r"<ref[^/]*?/>|<ref[\s\S]*?</ref>", re.IGNORECASE)
HTML_COMMENT_RE = re.compile(r"<!--[\s\S]*?-->")

# A bulleted/numbered list where each line links a place and mentions a
# country (the hallmark of a landing-points list). Anchored at line start.
BULLET_WITH_COUNTRY_RE = re.compile(
    r"^(?:[*#]+\s*(?:\{\{[^}]+\}\}\s*)?\[\[[^\]]+\]\][^\n]{0,80}\n){3,}",
    re.MULTILINE,
)

# A wikitable where most rows have a wikilinked place in the first cell.
WIKITABLE_RE = re.compile(
    r"\{\|\s*class=\"wikitable\"[\s\S]*?\n\|\}",
    re.MULTILINE,
)


def _extract_wikilinks(block: str) -> List[str]:
    links = WIKILINK_RE.findall(block)
    out: List[str] = []
    seen: Set[str] = set()
    for link in links:
        link = link.strip()
        if not link or link in SKIP_LINK_EXACT:
            continue
        if any(link.startswith(p) for p in SKIP_LINK_PREFIXES):
            continue
        if link[0].islower():
            continue
        key = link.split("#", 1)[0]
        if key in seen:
            continue
        seen.add(key)
        out.append(key)
    return out


def extract_landing_links(wikitext: str) -> Tuple[List[str], Optional[str]]:
    """Union of strategies: run all matchers, dedupe, return merged list.

    Returns (place_titles, methods_used_joined).
    """
    wikitext = REF_TAG_RE.sub("", wikitext)
    wikitext = HTML_COMMENT_RE.sub("", wikitext)

    all_links: List[str] = []
    methods: List[str] = []
    seen: Set[str] = set()

    def add(links: Iterable[str], label: str) -> None:
        added = 0
        for link in links:
            if link in seen:
                continue
            seen.add(link)
            all_links.append(link)
            added += 1
        if added:
            methods.append(f"{label}({added})")

    # 1. Infobox fields
    for m in INFOBOX_LANDINGS_RE.finditer(wikitext[:10000]):
        add(_extract_wikilinks(m.group(1)), "infobox")

    # 2. Sections headed with landing-like titles (any level)
    sections = list(SECTION_RE.finditer(wikitext))
    for i, m in enumerate(sections):
        title = m.group(1).strip()
        if LANDING_SECTION_RE.match(title):
            start = m.end()
            end = sections[i + 1].start() if i + 1 < len(sections) else len(wikitext)
            add(_extract_wikilinks(wikitext[start:end]), f"section:{title[:20]}")

    # 3. Definition-list marker
    for m in DEF_LIST_LANDINGS_RE.finditer(wikitext):
        add(_extract_wikilinks(m.group(1)), "def-list")

    # 4. Inline prose bullet list
    for m in INLINE_LANDINGS_RE.finditer(wikitext[:10000]):
        add(_extract_wikilinks(m.group(1)), "prose-bullets")

    # 5. Any bulleted list where each row has a wikilink + country suffix
    for m in BULLET_WITH_COUNTRY_RE.finditer(wikitext):
        block = m.group(0)
        links = _extract_wikilinks(block)
        if len(links) >= 3:
            add(links, "bullet-with-country")

    # 6. Wikitable first-cell wikilinks
    for m in WIKITABLE_RE.finditer(wikitext):
        table = m.group(0)
        rows = re.split(r"\n\|-", table)
        cell_links: List[str] = []
        for row in rows:
            mcell = re.search(r"\n\|\s*(?:\{\{[^}]+\}\}\s*)?\[\[([^\]|#]+)(?:[|#][^\]]*)?\]\]", row)
            if mcell:
                link = mcell.group(1).strip()
                if link and link not in SKIP_LINK_EXACT and not any(
                    link.startswith(p) for p in SKIP_LINK_PREFIXES
                ) and link[0].isupper():
                    cell_links.append(link.split("#", 1)[0])
        if len(cell_links) >= 3:
            add(cell_links, "wikitable")

    # 7. Lead-paragraph fallback (only if we haven't found anything so far,
    # since this is noisy)
    if not all_links:
        head = wikitext[:4000]
        for m in re.finditer(
            r"\[\[([^\]|#]+?)(?:#[^|\]]*)?(?:\|[^\]]*)?\]\](?=[,\s]+(?:in\s+)?[A-Z][a-z][\w \-.']+)",
            head,
        ):
            link = m.group(1).strip()
            if not link or link in SKIP_LINK_EXACT:
                continue
            if any(link.startswith(p) for p in SKIP_LINK_PREFIXES):
                continue
            if link[0].islower():
                continue
            key = link.split("#", 1)[0]
            if key in seen:
                continue
            seen.add(key)
            all_links.append(key)
        if all_links:
            methods.append(f"lead-fallback({len(all_links)})")

    return all_links, ",".join(methods) if methods else None


def batch_resolve_wikipedia_to_qid(titles: Iterable[str]) -> Dict[str, str]:
    """Map Wikipedia title → Wikidata Q-id using prop=pageprops, 50 at a time."""
    titles = list({t for t in titles if t})
    out: Dict[str, str] = {}
    for i in range(0, len(titles), 50):
        chunk = titles[i:i + 50]
        params = {
            "action": "query",
            "prop": "pageprops",
            "ppprop": "wikibase_item",
            "titles": "|".join(chunk),
            "format": "json",
            "formatversion": "2",
            "redirects": "1",
        }
        url = WIKIPEDIA_API + "?" + urllib.parse.urlencode(params)
        data = json.loads(http_get(url))
        redirects = {r["from"]: r["to"] for r in data.get("query", {}).get("redirects", [])}
        pages = {p["title"]: p for p in data.get("query", {}).get("pages", [])}
        for t in chunk:
            final = redirects.get(t, t)
            p = pages.get(final)
            if not p:
                continue
            qid = p.get("pageprops", {}).get("wikibase_item")
            if qid:
                out[t] = qid
        time.sleep(0.3)
    return out


NON_LANDING_LABELS = {
    "Africa", "Asia", "Europe", "North America", "South America",
    "Oceania", "Antarctica",
}

NON_LANDING_P31 = {
    "Q6256",       # country
    "Q3624078",    # sovereign state
    "Q185086",     # crown dependency
    "Q5107",       # continent
    "Q82794",      # geographical region
    "Q3336843",    # cardinal direction of the compass
    "Q165",        # sea
    "Q9430",       # ocean
    "Q23397",      # lake
    "Q12284",      # canal
    "Q34763",      # shoal
    "Q35657",      # US state
    "Q35000",      # Australian state
    "Q10864048",   # first-level administrative country subdivision
    "Q3455524",    # straits
    "Q37901",      # gulf
    "Q39594",      # bay
    "Q25614819",   # Canadian province
    "Q3572229",    # coast
    "Q34770",      # language (mis-resolved)
    "Q215627",     # person
    "Q8502",       # mountain
    "Q4022",       # river
    "Q46831",      # mountain range
    "Q23442",      # island (too broad; islands are often noise like "Atlantic ridge"), keep
    "Q28165",      # intercontinental region
    "Q43229",      # organization
    "Q891723",     # public company
    "Q4830453",    # business
    "Q133442",     # administrative territorial entity
    "Q494721",     # country of the United Kingdom
    "Q5255892",    # federal republic (carries Brazil and friends past the old issubset check)
    "Q1145276",    # member state of the United Nations
    "Q15642541",   # historical unrecognized state
    "Q3024240",    # historical country
    "Q1763527",    # OECD member state
    "Q1520223",    # member state of the European Union
}

# Settlement carve-out: even if a P31 matches the denylist, KEEP the entity
# when it ALSO has a settlement-ish type. New stricter check uses intersection +
# carve-out instead of the old issubset (countries leaked because their P31
# set has values outside the denylist).
SETTLEMENT_P31 = {
    "Q515",        # city
    "Q1093829",    # city in the United States
    "Q3957",       # town
    "Q486972",     # human settlement
    "Q5084",       # hamlet
    "Q15284",      # municipality
    "Q702492",     # urban area
    "Q1549591",    # big city
    "Q21672098",   # cable landing point
    "Q532",        # village
    "Q123705",     # neighbourhood
    "Q14757767",   # village (Wikidata variant)
    "Q1620908",    # historical settlement
    "Q1093829",    # incorporated city in the US
    "Q852446",     # administrative territorial entity of the US
    "Q5119",       # capital city
}

# P625 precision (degrees). Country centroids carry coarse precision; real
# city/landing-station coords are 0.0001 or finer. Reject anything coarser
# than this threshold — ~10 km grid cells.
P625_PRECISION_FLOOR_DEG = 0.1


def _normalize_label(s: str) -> str:
    """Lower + strip diacritics for fuzzy label comparisons."""
    s = unicodedata.normalize("NFKD", s)
    s = "".join(ch for ch in s if not unicodedata.combining(ch))
    return s.strip().lower()


def batch_fetch_place_coords(qids: Iterable[str]) -> Dict[str, Dict[str, Any]]:
    """Return {qid: {label, lat, lon, country_code}} using wbgetentities.

    Three-stage pipeline with strict landing-precision policy:
      1. Fetch place entities; reject countries/regions/oceans via stricter
         intersection-with-carve-out P31 check + P625 precision floor.
      2. Resolve referenced country Q-ids to ISO codes AND English labels +
         aliases (used in stage 3 to drop label-equals-country pseudo-landings).
      3. Drop any place whose label normalizes to its country's label/alias
         (catches "Brazil"/"Italy"/"Japan"-style country-centroid leaks that
         survived the P31 filter).
    """
    qids = list({q for q in qids if q})
    out: Dict[str, Dict[str, Any]] = {}
    country_qids: Set[str] = set()
    # Stage 1: fetch place entities (50 at a time)
    for i in range(0, len(qids), 50):
        chunk = qids[i:i + 50]
        params = {
            "action": "wbgetentities",
            "ids": "|".join(chunk),
            "props": "labels|claims",
            "languages": "en",
            "format": "json",
            "formatversion": "2",
        }
        url = WIKIDATA_API + "?" + urllib.parse.urlencode(params)
        data = json.loads(http_get(url))
        for qid, e in data.get("entities", {}).items():
            lbl = e.get("labels", {}).get("en", {}).get("value")
            coord_claims = e.get("claims", {}).get("P625", [])
            country_claims = e.get("claims", {}).get("P17", [])
            p31_claims = e.get("claims", {}).get("P31", [])
            if not coord_claims:
                continue
            if lbl in NON_LANDING_LABELS:
                continue
            p31_qids = {
                c.get("mainsnak", {}).get("datavalue", {}).get("value", {}).get("id")
                for c in p31_claims
            }
            p31_qids.discard(None)
            # Stricter check: drop if ANY P31 is in the denylist UNLESS the
            # entity ALSO has a settlement carve-out P31. Old code used
            # issubset, which let countries leak when they had P31 values
            # outside the denylist (Brazil → Q5255892 federal republic, etc.).
            if p31_qids & NON_LANDING_P31 and not (p31_qids & SETTLEMENT_P31):
                continue
            v = coord_claims[0].get("mainsnak", {}).get("datavalue", {}).get("value", {})
            lat = v.get("latitude")
            lon = v.get("longitude")
            if lat is None or lon is None:
                continue
            # P625 precision floor: country centroids carry coarse precision.
            precision = v.get("precision")
            if isinstance(precision, (int, float)) and precision >= P625_PRECISION_FLOOR_DEG:
                continue
            cqid = None
            if country_claims:
                cv = country_claims[0].get("mainsnak", {}).get("datavalue", {}).get("value", {})
                cqid = cv.get("id")
                if cqid:
                    country_qids.add(cqid)
            out[qid] = {"label": lbl, "lat": lat, "lon": lon, "country_qid": cqid, "country_code": None}
        time.sleep(0.3)
    # Stage 2: resolve country Q-ids to ISO codes (P297), labels, and aliases.
    country_qids_list = list(country_qids)
    country_codes: Dict[str, str] = {}
    country_label_set: Dict[str, Set[str]] = {}  # cqid -> {normalized labels + aliases}
    for i in range(0, len(country_qids_list), 50):
        chunk = country_qids_list[i:i + 50]
        params = {
            "action": "wbgetentities",
            "ids": "|".join(chunk),
            "props": "claims|labels|aliases",
            "languages": "en",
            "format": "json",
            "formatversion": "2",
        }
        url = WIKIDATA_API + "?" + urllib.parse.urlencode(params)
        data = json.loads(http_get(url))
        for qid, e in data.get("entities", {}).items():
            code_claims = e.get("claims", {}).get("P297", [])
            lbl = e.get("labels", {}).get("en", {}).get("value")
            if code_claims:
                v = code_claims[0].get("mainsnak", {}).get("datavalue", {}).get("value")
                if isinstance(v, str):
                    country_codes[qid] = v.upper()
            elif lbl:
                country_codes[qid] = lbl[:3].upper()
            # Build label/alias set for the country.
            label_set: Set[str] = set()
            if lbl:
                label_set.add(_normalize_label(lbl))
            for alias in e.get("aliases", {}).get("en", []):
                v = alias.get("value")
                if isinstance(v, str) and v:
                    label_set.add(_normalize_label(v))
            if label_set:
                country_label_set[qid] = label_set
        time.sleep(0.3)
    # Stage 3: filter places whose label matches their country's label/alias.
    filtered_out: Dict[str, Dict[str, Any]] = {}
    for qid, rec in out.items():
        cqid = rec.get("country_qid")
        if cqid and cqid in country_codes:
            rec["country_code"] = country_codes[cqid]
        place_label_norm = _normalize_label(rec.get("label") or "")
        if cqid and place_label_norm:
            if place_label_norm in country_label_set.get(cqid, set()):
                # Country-name label → pseudo-landing. Drop.
                continue
        filtered_out[qid] = rec
    return filtered_out


def slugify(name: str) -> str:
    s = re.sub(r"[^a-z0-9]+", "-", name.lower()).strip("-")
    return s or "cable"


def iso_year(iso: Optional[str]) -> int:
    if not iso:
        return 0
    m = re.match(r"([+-]?\d{4,5})-", iso)
    if m:
        y = int(m.group(1))
        if 1850 <= y <= 2100:
            return y
    return 0


def main() -> int:
    out_dir = Path(sys.argv[1]) if len(sys.argv) > 1 else Path("/tmp/fukan_cables_out")
    out_dir.mkdir(parents=True, exist_ok=True)

    seed = fetch_cable_seed()
    print(f"Seed cables from Wikidata: {len(seed)}", file=sys.stderr)
    wp_titles = [r["wp_title"] for r in seed.values() if r.get("wp_title")]
    print(f"With Wikipedia sitelink: {len(wp_titles)}", file=sys.stderr)

    # Fetch all Wikipedia articles
    wikitexts = fetch_wikipedia_articles(wp_titles)
    print(f"Fetched Wikipedia articles: {len(wikitexts)}", file=sys.stderr)

    # Extract landings per cable
    cable_landings: Dict[str, Tuple[List[str], Optional[str]]] = {}
    all_place_titles: Set[str] = set()
    for qid, rec in seed.items():
        t = rec.get("wp_title")
        if not t or t not in wikitexts:
            cable_landings[qid] = ([], None)
            continue
        places, section = extract_landing_links(wikitexts[t])
        cable_landings[qid] = (places, section)
        all_place_titles.update(places)
    with_landings = sum(1 for v in cable_landings.values() if v[0])
    print(f"Cables with extracted landing place links: {with_landings}", file=sys.stderr)
    print(f"Distinct place Wikipedia titles: {len(all_place_titles)}", file=sys.stderr)

    # Resolve place titles → Q-ids → coords
    title_to_qid = batch_resolve_wikipedia_to_qid(all_place_titles)
    print(f"Resolved place Q-ids: {len(title_to_qid)}", file=sys.stderr)
    qid_to_place = batch_fetch_place_coords(title_to_qid.values())
    print(f"Resolved coords: {len(qid_to_place)}", file=sys.stderr)

    # Build output
    cables_out = []
    landings_out = []
    for qid, rec in seed.items():
        places, section = cable_landings[qid]
        resolved_landings = []
        for title in places:
            pqid = title_to_qid.get(title)
            if not pqid:
                continue
            place = qid_to_place.get(pqid)
            if not place:
                continue
            resolved_landings.append({
                "qid": pqid,
                "wp_title": title,
                "label": place["label"] or title,
                "lat": place["lat"],
                "lon": place["lon"],
                "country_code": place.get("country_code"),
            })

        name = rec.get("name") or rec.get("wp_title") or qid
        slug = slugify(name)

        sources = [
            {"url": f"https://www.wikidata.org/wiki/{qid}", "license": "CC0", "retrieved_at": "2026-04-24"},
        ]
        if rec.get("wp_url"):
            sources.append({"url": rec["wp_url"], "license": "CC-BY-SA-3.0", "retrieved_at": "2026-04-24"})

        # Score: do we have ≥2 independent sources? Wikidata + Wikipedia counts as 2.
        n_sources = len(sources)

        cable_rec = {
            "cable_id": qid,
            "name": name,
            "slug": slug,
            "alt_names": [],
            "owners": [o["label"] for o in rec.get("owners", [])],
            "status": "active",  # default; refined in Go from inception/retirement if needed
            "rfs_year": iso_year(rec.get("inception")),
            "length_km": rec.get("length_km") or 0,
            "medium": "fibre",
            "category": "fibre_optic",
            "landing_ids": [f'{qid}:{l["qid"]}' for l in resolved_landings],
            "n_landings": len(resolved_landings),
            "wikipedia_section": section,
            "sources": sources,
            "source_count": n_sources,
        }
        cables_out.append(cable_rec)
        for l in resolved_landings:
            landings_out.append({
                "landing_id": f'{qid}:{l["qid"]}',
                "cable_id": qid,
                "cable_name": name,
                "country": l.get("country_code") or "",
                "location_name": l["label"],
                "lat": l["lat"],
                "lon": l["lon"],
                "sources": [
                    {"url": f"https://www.wikidata.org/wiki/{l['qid']}", "license": "CC0", "retrieved_at": "2026-04-24"},
                    *(
                        [{"url": rec["wp_url"], "license": "CC-BY-SA-3.0", "retrieved_at": "2026-04-24"}]
                        if rec.get("wp_url") else []
                    ),
                ],
            })

    (out_dir / "cables.jsonl").write_text(
        "\n".join(json.dumps(c, ensure_ascii=False) for c in cables_out) + "\n",
        encoding="utf-8",
    )
    (out_dir / "landings.jsonl").write_text(
        "\n".join(json.dumps(l, ensure_ascii=False) for l in landings_out) + "\n",
        encoding="utf-8",
    )
    print(
        f"Wrote {len(cables_out)} cables, {len(landings_out)} landings → {out_dir}",
        file=sys.stderr,
    )
    cables_with_landings = sum(1 for c in cables_out if c["n_landings"] > 0)
    print(f"Cables with >=1 resolved landing: {cables_with_landings}", file=sys.stderr)
    return 0


if __name__ == "__main__":
    sys.exit(main())
