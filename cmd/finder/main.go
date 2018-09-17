// Copyright 2018 The Gofrs. All rights reserved.
// Use of this source code is governed by the BSD 3-Clause
// license that can be found in the LICENSE file.

// finder is a command line tool (CLI) used to find stale projects
// on Github (those without recent commits, issues, etc) and rank
// them by their godoc import counts.
//
// Godoc.org import counts are public and computed by godoc.org as
// it indexes the public Go repositories.
package main // import "github.com/gofrs/help-requests/cmd/finder

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"math"
	"net/http"
	"os"
	"sort"
	"strconv"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/google/go-github/github"
	"golang.org/x/net/html"
	"golang.org/x/oauth2"
)

var (
	flagCount = flag.Int("count", 25, "How many (Github) projects to lookup")
)

func main() {
	flag.Parse()

	ghClient, err := createGithubClient(context.Background())
	if err != nil {
		fmt.Fprintf(os.Stderr, "ERROR: problem creating github client: %v", err)
	}

	opts := github.ListOptions{
		PerPage: *flagCount,
		Page:    1,
	}

	query := "stars:>100 pushed:<2018-01-01 language:Go"
	repoRes, res, err := ghClient.Search.Repositories(context.Background(), query, &github.SearchOptions{
		Sort:        "stars",
		Order:       "desc",
		ListOptions: opts,
	})
	if err != nil {
		fmt.Fprintf(os.Stderr, "ERROR: problem reading github repositories: %v", err)
	}
	res.Close = true

	type row struct {
		text        string
		importCount int
	}
	var rows []row
	for i := range repoRes.Repositories {
		repo := repoRes.Repositories[i]

		cleanName := strings.Replace(*repo.HTMLURL, `https://`, "", 1)

		// TODO(adam): goroutines + sync.WaitGroup
		importers, err := scrapeGodocImports(cleanName)
		if err != nil {
			fmt.Fprintf(os.Stderr, "ERROR: problem grabbing %s godoc importers: %v\n", cleanName, err)
		}

		days := int(math.Abs(float64(repo.PushedAt.Sub(time.Now()).Hours()) / 24.0))
		line := fmt.Sprintf("%s\t%d\t%d\t%d\n", cleanName, *repo.StargazersCount, days, importers)
		rows = append(rows, row{
			text:        line,
			importCount: importers,
		})
	}
	sort.Slice(rows, func(i, j int) bool { return rows[i].importCount < rows[j].importCount })

	// Write (sorted) output
	w := tabwriter.NewWriter(os.Stdout, 0, 0, 1, ' ', 0)
	fmt.Fprintf(w, "name\tstars\tlast commit (days)\timporters\n")
	defer w.Flush()
	for i := range rows {
		// we're going to write the rows in reverse
		// this will output them in desc order
		fmt.Fprintf(w, rows[len(rows)-i-1].text)
	}
}

func createGithubClient(ctx context.Context) (*github.Client, error) {
	v := os.Getenv("GITHUB_TOKEN")
	if v == "" {
		return nil, errors.New("environment variable GITHUB_TOKEN is required")
	}
	ts := oauth2.StaticTokenSource(&oauth2.Token{
		AccessToken: v,
	})
	tc := oauth2.NewClient(ctx, ts)
	return github.NewClient(tc), nil
}

func scrapeGodocImports(importPath string) (int, error) {
	req, err := http.NewRequest("GET", "https://godoc.org/"+importPath, nil)
	if err != nil {
		return -1, fmt.Errorf("problem loading godoc.org: %v", err)
	}
	req.Header.Set("User-Agent", "Gofrs popstalerepo bot")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return -1, fmt.Errorf("problem loading %s: %v", req.URL, err)
	}
	defer resp.Body.Close()

	// recursive search, from /x/net/html docs
	var f func(n *html.Node) (int, error)
	f = func(n *html.Node) (int, error) {
		if n.Type == html.ElementNode && n.Data == "a" {
			for _, a := range n.Attr {
				// TODO(adam): we should try and refresh importers
				// when running into errors.
				if a.Key == "href" && strings.Contains(a.Val, "?importers") {
					parts := strings.Fields(n.FirstChild.Data)
					n, err := strconv.Atoi(parts[0])
					if err != nil {
						return -1, fmt.Errorf("couldn't parse %q: %v", parts[0], err)
					}
					return n, nil
				}
			}
		}
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			n, err := f(c)
			if err == nil && n > 0 {
				return n, err
			}
		}
		return -1, errors.New(`didn't find <a href="?importers">`)
	}

	doc, err := html.Parse(resp.Body)
	if err != nil {
		return -1, fmt.Errorf("couldn't parse html: %v", err)
	}
	return f(doc)
}
