# go-tools

Small Go programs I wrote to stop doing things by hand. One tool so far.

## nav-sync

Fills in two columns of a Notion table that I used to type in every evening.

I track eleven mutual funds in Notion. Every column in that table was correct
except the price of each fund and the date that price was published. Those two
I read off my broker's app and typed in. This program does it instead.

It reads AMFI's public NAV report, matches it against the rows in a Notion
data source using the ISIN as the key, and writes the NAV and its publication
date back to each matching page. Everything else in the table is a Notion
formula built on top of those two values, so nothing else needs writing.

### How it runs

1. Query the Notion data source, following the cursor until there are no more
   pages. Build a map of ISIN to every page ID holding that ISIN.
2. Fetch `NAVAll.txt` from AMFI and parse it into a map of ISIN to NAV.
3. For each ISIN in Notion, look up the NAV, convert it, and PATCH the page.
4. Print a summary. Exit non-zero if anything failed.

Reading comes before writing because Notion has no "update where isin is X".
It is a store of pages, and every write needs the page ID.

### Requirements

- Go 1.26 or later
- A Notion internal integration token
- A Notion data source with three properties: `isin` (rich text), `nav`
  (number), and `nav_date` (date)

The integration must be added to the database under Connections. Without that
step the token is valid but the database is invisible, and the query returns
zero results with a 200 status.

### Setup

```bash
git clone https://github.com/siddhant-angore/go-tools.git
cd go-tools
```

Create a `.env` file. It is gitignored.

```bash
NOTION_TOKEN=your_integration_token
NOTION_DATA_SOURCE_ID=your_data_source_id
```

### Run

```bash
set -a; . ./.env; set +a
go run .
```

It writes to live Notion pages, so point it at a test database first.

### Output

```
2026/08/08 01:25:48 Found 11 pages across 11 ISINs in Notion
2026/08/08 01:25:49 Parsed 17960 ISINs from AMFI.
INF846K01K35 Axis Small Cap Fund - Direct Plan - Growth   135.58 07-Aug-2026 269b2c97-160b-4303-b007-737848af564e
INF789F01XA0 UTI Nifty 50 Index Fund - Growth Option- Direct  173.0145 07-Aug-2026 aa1c4623-18b4-2fcc-26ee-df5a5e9f166c
2026/08/08 01:25:55 Done in 8.109s: 11 updated, 0 failed
```

Exit code is 0 when every row was written, 1 when any row failed. A scheduler
reads the exit code, not the log.

### Notes on the data

AMFI's file is semicolon delimited and about 17,000 lines. Some things about
it are not obvious from looking at the top of it:

- The first five lines contain no funds. A header row, blank lines, a category
  heading, and an asset manager name. The parser keeps a line only when field 0
  converts to an integer, which is what a real scheme code always is.
- The ISIN sits in one of two columns. Field 1 is the payout or growth ISIN,
  field 2 is the reinvestment ISIN, and either can be a dash. Both are indexed
  against the same record.
- The file is not uniformly current. Dead schemes are never removed, they stop
  updating, so a NAV dated 2017 can sit in today's file. The date is kept as a
  field rather than assumed.
- NAV is parsed as a string, not a float. Some schemes publish `N.A.`, and a
  parser that dies on one bad row gives you nothing.

`navall.txt` is committed as a frozen snapshot of the file, so the examples
above stay reproducible. The program always fetches a fresh copy at runtime.

### Design notes

- `parseNAVs` takes an `io.Reader` rather than a URL, so it works against a
  live response, a saved file, or a string in a test. Fetching and parsing are
  separate jobs.
- Notion rows are stored as `map[string][]string`, ISIN to page IDs. The same
  fund can appear on several rows under different investors, and a plain map
  would silently keep only the last one.
- A non-2xx from Notion has its body read into the error. The body names the
  property Notion objected to. The status code alone does not.
- One bad row logs and continues rather than exiting. A batch job should finish
  what it can and report what it could not.
- One shared `http.Client` with a 30 second timeout, so a stalled server cannot
  hang the run.

### Still outstanding

- The eleven writes are sequential and take about six of the eight seconds.
  They are independent and should run concurrently.
- No retry on a failed write.
- No rate limiting, which Notion will start caring about above a few dozen rows.
