package main

import (
	"bufio" // Buffered I/O
	"bytes"
	"encoding/json"
	"fmt" // Formatted I/O
	"io"
	"log"      // Output with timestamps, logs to stderr
	"net/http" // Network
	"os"
	"strconv"
	"strings"
	"time"
)

const (
	amfiNAVURL    = "https://www.amfiindia.com/spages/NAVAll.txt"
	notionVersion = "2025-09-03"
)

func main() {

	// Go has no throw/catch. A function that can fail returns what you
	// wanted AND what went wrong, side by side. The cost is three extra
	// lines everywhere; the benefit is that every failure point is
	// visible at the call site and cannot silently jump up the stack.
	// res, err := http.Get("https://www.amfiindia.com/spages/NAVAll.txt")

	// err must be checked BEFORE touching res, because res is
	// meaningless when err is non-nil. Reading res
	// first simply does not make sense.
	// if err != nil {
	// 	log.Fatal(err)
	// }

	// The network response is not a document/object, but a stream of bytes. It's an open pipe.
	// When http.Get returns, the stream is open and must be closed when we are done with it.
	// A connection is limited resource, and if you leak it, your program will eventually run out of connections and fail.
	// The idiomatic way to ensure that the stream is closed is to schedule it with defer immediately after the error check.
	// defer res.Body.Close()

	// The network has no concept of a line.
	//
	// TCP delivers arbitrary blobs. One packet might carry two & a half lines;
	// one line might extend across three packets.
	// res.Body is bytes with no structure at all.
	// The bufio package provides a buffered reader that can read lines from a stream of bytes.
	//
	// bufio.NewScanner returns a scanner that reads from res.Body.
	// The scanner has a buffer and can read lines from the stream.
	//
	// It accepts any io.Reader, so same line works against a file, a string, or a network stream / response.
	// scanner := bufio.NewScanner(res.Body)

	// countLines := 0

	// Funds that I hold, keyed by ISIN. This is a map of string to bool struct.
	// myISINs := map[string]bool{
	// 	"INF0R8F01026": true,
	// 	"INF789F01XA0": true,
	// 	"INF966L01986": true,
	// 	"INF663L01DV3": true,
	// 	"INF879O01027": true,
	// 	"INF204K01YC4": true,
	// 	"INF204KB14I2": true,
	// 	"INF769K01DM9": true,
	// 	"INF204KB1V68": true,
	// 	"INF732E01045": true,
	// 	"INF846K01K35": true,
	// }

	start := time.Now()

	token := os.Getenv("NOTION_TOKEN")
	if token == "" {
		log.Fatal("NOTION_TOKEN not set.")
	}
	dataSourceID := os.Getenv("NOTION_DATA_SOURCE_ID")
	if dataSourceID == "" {
		log.Fatal("NOTION_DATA_SOURCE_ID not set.")
	}

	rows, err := fetchNotionRows(token, dataSourceID)
	if err != nil {
		log.Fatal(err)
	}

	pageCount := 0
	for _, ids := range rows {
		pageCount += len(ids)
	}
	log.Printf("Found %d pages across %d ISINs in Notion", pageCount, len(rows))

	navs, err := fetchNAVs(amfiNAVURL)
	if err != nil {
		log.Fatal(err)
	}
	log.Printf("Parsed %d ISINs from AMFI.", len(navs))

	updated, failed := 0, 0

	for isin, pageIDs := range rows {
		nav, ok := navs[isin]
		if !ok {
			log.Printf("Warning: No NAV for %s", isin)
			failed += len(pageIDs)
			continue
		}

		value, err := strconv.ParseFloat(nav.Value, 64)
		if err != nil {
			log.Printf("Skipping %s: Bad NAV %q: %v", isin, nav.Value, err)
			failed += len(pageIDs)
			continue
		}

		t, err := time.Parse("02-Jan-2006", nav.Date)
		if err != nil {
			log.Printf("Skipping %s: Bad Date %q: %v", isin, nav.Date, err)
			failed += len(pageIDs)
			continue
		}
		isoDate := t.Format("2006-01-02")

		for _, pageID := range pageIDs {
			if err := updateNAV(token, pageID, value, isoDate); err != nil {
				log.Printf("Failed %s (%s): %v", isin, pageID, err)
				failed++
				continue
			}
			updated++
			fmt.Printf("%s %-45s %10s %s %s\n", isin, nav.Name, nav.Value, nav.Date, pageID)
		}
	}

	log.Printf("Done in %s: %d updated, %d failed", time.Since(start).Round(time.Millisecond), updated, failed)

	if failed > 0 {
		os.Exit(1)
	}

	// Keeping only what I hold, and printing the NAVs for those ISINs.
	// mine := make(map[string]NAV)
	// for isin := range myISINs {
	// 	if nav, ok := all[isin]; ok {
	// 		mine[isin] = nav
	// 	}
	// }

	// for isin, nav := range mine {
	// 	fmt.Printf("%s %-45s %10s %s\n", isin, nav.Name, nav.Value, nav.Date)
	// }

	// Every loop before this one iterated over what was FOUND. This one
	// iterates over what was WANTED. Without it, a fund that silently
	// vanishes from the file looks exactly like a successful run.
	// for isin := range myISINs {
	// 	if _, ok := mine[isin]; !ok {
	// 		log.Printf("Warning: No NAV found for %s", isin)
	// 	}
	// }

	// Parsing a stream is a loop. The scanner reads a line, and the loop body processes it.
	// for scanner.Scan() {
	// 	countLines++
	// 	if countLines <= 5 {
	// 		// Text() returns the current line as string without the newline character.
	// 		// The scanner keeps the line in memory until the next Scan() call.
	// 		fmt.Println(scanner.Text())
	// 	}
	// }

	// // HINT: bufio.Scanner "scanner" is used in Scan loop at line 48 without final check of scanner.Err()
	// // This is a common mistake. The scanner may have failed to read the stream, and the loop will exit without any indication of failure.
	// // To check this, call scanner.Err() after the loop. If it returns non-nil, the scanner failed to read the stream.
	// if err := scanner.Err(); err != nil {
	// 	log.Fatal(err)
	// }

	// fmt.Println("Total lines: ", countLines)
}

// NAV is one scheme's published net asset value.
//
// Value stays a string on purpose: some schemes publish "N.A." instead
// of a number, and a parser that dies on one bad row gives you nothing.
// Date is kept because the file is not uniformly current — dead schemes
// sit in it for years with their last published NAV.
type NAV struct {
	Name  string // field 3
	Value string // field 4
	Date  string // field 5
}

// parseNAVs reads AMFI's semicolon-delimited report from any source.
//
// Taking an io.Reader rather than a URL means this same function works
// against a live response, a file saved to disk, or a string literal in
// a test. Fetching and parsing are separate jobs.
func parseNAVs(r io.Reader) (map[string]NAV, error) {
	navs := make(map[string]NAV)
	scanner := bufio.NewScanner(r)

	for scanner.Scan() {
		// We will be splitting the line into fields using the semicolon ";" as the delimiter.
		fields := strings.Split(scanner.Text(), ";")

		// Instrument's name as no semiolons, as these are human readable in response.
		// We will add a guard here to ensure that we have at least 6 fields, as we are interested in fields 3, 4, and 5.
		if len(fields) < 6 {
			continue
		}

		// The header row passes the guard, but we don't want to include it in the map. We will skip it by checking if the first field is "Scheme Code".
		// Scheme code is a number, so we can use strconv.Atoi to check if it is a number. If it is not a number, we will skip the row.
		if _, err := strconv.Atoi(strings.TrimSpace(fields[0])); err != nil {
			continue
		}

		// Creating the NAV struct with the required fields. We will trim the whitespace from the fields to ensure that we have clean data.
		nav := NAV{
			Name:  strings.TrimSpace(fields[3]),
			Value: strings.TrimSpace(fields[4]),
			Date:  strings.TrimSpace(fields[5]),
		}

		// There are two ISINs for each fund, one for growth and one for dividend.
		// Indexing both ISINs in the map, so that we can look up the NAV by either ISIN.
		for _, index := range []int{1, 2} {
			isin := strings.TrimSpace(fields[index])
			if isin == "" || isin == "-" {
				continue
			}
			navs[isin] = nav
		}
	}

	return navs, scanner.Err()
}

func fetchNAVs(url string) (map[string]NAV, error) {
	res, err := httpClient.Get(url)
	if err != nil {
		return nil, err
	}
	defer res.Body.Close()

	// A non-2xx is not a transport error. The request succeeded, the
	// server just said no. Without this check an HTML error page parses
	// cleanly to zero rows and the program reports success.
	if res.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("AMFI returned %d", res.StatusCode)
	}

	return parseNAVs(res.Body)
}

// QueryResponse is Notion's reply to a data source query.
//
// Notion sends at most 100 rows at a time. HasMore and NextCursor are
// how it tells you there is more, and where to carry on from.
type QueryResponse struct {
	Results    []Page `json:"results"`
	HasMore    bool   `json:"has_more"`
	NextCursor string `json:"next_cursor"`
}

// Page is one row of the table.
//
// The real response carries around two hundred fields per row. This
// describes four of them. Go ignores everything it was not told about,
// so this code does not break when Notion adds something new.
//
// Note that rich_text is a list. A Notion text cell can hold several
// runs of differently styled text, so even a plain cell arrives as a
// list with one item in it.
type Page struct {
	ID         string `json:"id"`
	Properties struct {
		ISIN struct {
			RichText []struct {
				PlainText string `json:"plain_text"`
			} `json:"rich_text"`
		} `json:"isin"`
	} `json:"properties"`
}

// notionRequest builds a request with the three headers every Notion
// call needs. One function owns this so the API version string exists
// in exactly one place.
func notionRequest(method, url, token string, body []byte) (*http.Request, error) {
	var reader io.Reader
	if body != nil {
		reader = bytes.NewReader(body)
	}

	req, err := http.NewRequest(method, url, reader)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Notion-Version", notionVersion)
	req.Header.Set("Content-Type", "application/json")

	return req, nil
}

// fetchNotionRows returns every ISIN in the table, along with every
// page that holds it.
//
// The value is a slice, not a single string. I hold the same fund under
// two investors, so one ISIN can appear on several rows. A plain
// map[string]string would keep the last one and silently drop the rest.
func fetchNotionRows(token, dataSourceID string) (map[string][]string, error) {
	rows := make(map[string][]string)
	url := "https://api.notion.com/v1/data_sources/" + dataSourceID + "/query"
	cursor := ""

	for {
		body := map[string]any{"page_size": 100}

		// start_cursor is eft out entirely on the first request.
		// Sending it as an empty string is an error.
		if cursor != "" {
			body["start_cursor"] = cursor
		}

		payload, err := json.Marshal(body)
		if err != nil {
			return nil, err
		}

		req, err := notionRequest("POST", url, token, payload)
		if err != nil {
			return nil, err
		}

		res, err := httpClient.Do(req)
		if err != nil {
			return nil, err
		}

		// Notion sends its refusal in the response body.
		if res.StatusCode != http.StatusOK {
			msg, _ := io.ReadAll(res.Body)
			res.Body.Close()
			return nil, fmt.Errorf("Notion query returned %d: %s", res.StatusCode, msg)
		}

		var page QueryResponse
		err = json.NewDecoder(res.Body).Decode(&page)

		// Not using defer here because, defer is tied to a function.
		// Inside a loop they are not the same. Hence, not using defer here.
		res.Body.Close()
		if err != nil {
			return nil, err
		}

		for _, p := range page.Results {
			// A row with empty ISIN has an empty list
			if len(p.Properties.ISIN.RichText) == 0 {
				continue
			}

			// A cell holding only whitespace trims to "". Without this it
			// becomes a map key and reappears later as a warning about a
			// fund with no name.
			isin := strings.TrimSpace(p.Properties.ISIN.RichText[0].PlainText)
			if isin == "" || isin == "-" {
				continue
			}
			rows[isin] = append(rows[isin], p.ID)
		}

		// Notion sends 100 rows at a time. Stop only when it says so.
		if !page.HasMore {
			break
		}
		cursor = page.NextCursor
	}

	return rows, nil
}

// One shared client for every request in the program.
//
// It holds the connection pool, so repeated calls to Notion reuse the
// same TCP and TLS connection instead of negotiating a new one each
// time. It also carries a deadline, which the default client does not
// have at all. Without one, a server that accepts the connection and
// then stalls hangs this program forever.
var httpClient = &http.Client{
	Timeout: 30 * time.Second,
}

// updateNAVs writes 2 properties to 1 page.
//
// This is a PATCH, not a replacement.
func updateNAV(token, pageID string, nav float64, isoDate string) error {
	// Each value is wrapped in a property type, because a number & date column
	// accept different shapes
	body := map[string]any{
		"properties": map[string]any{
			"nav": map[string]any{"number": nav},
			"nav_date": map[string]any{
				"date": map[string]any{"start": isoDate},
			},
		},
	}

	payload, err := json.Marshal(body)
	if err != nil {
		return err
	}

	req, err := notionRequest("PATCH", "https://api.notion.com/v1/pages/"+pageID, token, payload)
	if err != nil {
		return err
	}

	res, err := httpClient.Do(req)
	if err != nil {
		return err
	}
	defer res.Body.Close()

	// Notion puts the reason in the body. A bare status code tells you
	// nothing; the body names the property it objected to.
	if res.StatusCode != http.StatusOK {
		msg, _ := io.ReadAll(res.Body)
		return fmt.Errorf("Notion returned %d: %s", res.StatusCode, msg)
	}

	return nil
}
