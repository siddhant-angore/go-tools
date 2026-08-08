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
	"sync"
	"time"
)

const (
	amfiNAVURL    = "https://www.amfiindia.com/spages/NAVAll.txt"
	notionVersion = "2025-09-03"
)

func main() {
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

	jobs, skipped := buildJobs(rows, navs)
	updated, failed := runUpdates(token, jobs)
	failed += skipped

	log.Printf("Done in %s: %d updated, %d failed", time.Since(start).Round(time.Millisecond), updated, failed)

	if failed > 0 {
		os.Exit(1)
	}
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

type job struct {
	isin    string
	pageID  string
	value   float64
	isoDate string
}

type result struct {
	isin   string
	pageID string
	err    error
}

func buildJobs(rows map[string][]string, navs map[string]NAV) (jobs []job, skipped int) {
	for isin, pageIDs := range rows {
		nav, ok := navs[isin]
		if !ok {
			log.Printf("Warning: No NAV for %s", isin)
			skipped += len(pageIDs)
			continue
		}

		value, err := strconv.ParseFloat(nav.Value, 64)
		if err != nil {
			log.Printf("Skipping %s: Bad Value Value %q: %v", isin, nav.Value, err)
			skipped += len(pageIDs)
			continue
		}

		t, err := time.Parse("02-Jan-2006", nav.Date)
		if err != nil {
			log.Printf("Skipping %s: Bad Date %q: %v", isin, nav.Date, err)
			skipped += len(pageIDs)
			continue
		}
		isoDate := t.Format("2006-01-02")

		for _, pageID := range pageIDs {
			jobs = append(jobs, job{
				isin:    isin,
				pageID:  pageID,
				value:   value,
				isoDate: isoDate,
			})
		}
	}

	return jobs, skipped
}

func runUpdates(token string, jobs []job) (updated, failed int) {
	jobChannel := make(chan job)
	resultChannel := make(chan result, len(jobs))

	limiter := time.NewTicker(334 * time.Millisecond)
	defer limiter.Stop()

	var waitGroup sync.WaitGroup
	for i := 0; i < 3; i++ {
		waitGroup.Add(1)
		go func() {
			defer waitGroup.Done()
			for j := range jobChannel {
				<-limiter.C

				t0 := time.Now()
				err := updateNAV(token, j.pageID, j.value, j.isoDate)
				log.Printf("%s took %s", j.isin, time.Since(t0).Round(time.Millisecond))
				resultChannel <- result{isin: j.isin, pageID: j.pageID, err: err}
			}
		}()
	}

	for _, j := range jobs {
		jobChannel <- j
	}
	close(jobChannel)

	waitGroup.Wait()
	close(resultChannel)

	for r := range resultChannel {
		if r.err != nil {
			log.Printf("Failed %s (%s): %v", r.isin, r.pageID, r.err)
			failed++
			continue
		}
		updated++
	}

	return updated, failed
}
