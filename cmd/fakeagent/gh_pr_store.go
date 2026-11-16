package main

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"
)

// ghPRStore makes the fork-pr gh stub remember the one pull request it
// created, so a later run on the same branch finds an existing PR: `pr list`
// reports it, `pr view --json title,body` returns what was last written, and
// `pr edit` replaces it. It is opt-in through $FAKEAGENT_GH_PR_STORE (a JSON
// file); without it the stub stays stateless. A test may rewrite the file
// between runs to stand in for a body an earlier version wrote.
type ghPRStore struct {
	path string
}

type ghStoredPR struct {
	URL   string `json:"url"`
	Base  string `json:"base"`
	Title string `json:"title"`
	Body  string `json:"body"`
}

func ghPRStoreFromEnv() (ghPRStore, bool) {
	path := os.Getenv("FAKEAGENT_GH_PR_STORE")
	return ghPRStore{path: path}, path != ""
}

func (s ghPRStore) load() (ghStoredPR, bool) {
	data, err := os.ReadFile(s.path)
	if err != nil {
		return ghStoredPR{}, false
	}
	var pr ghStoredPR
	if err := json.Unmarshal(data, &pr); err != nil {
		return ghStoredPR{}, false
	}
	return pr, true
}

func (s ghPRStore) save(pr ghStoredPR) int {
	data, err := json.Marshal(pr)
	if err == nil {
		err = os.WriteFile(s.path, data, 0o644)
	}
	if err != nil {
		fmt.Fprintf(os.Stderr, "fakeagent gh: save pull request: %v\n", err)
		return 1
	}
	return 0
}

// handle answers the calls that depend on the stored pull request. handled is
// false for every other call, which the stateless stub then answers.
func (s ghPRStore) handle(args []string, body string) (code int, handled bool) {
	if len(args) < 2 || args[0] != "pr" {
		return 0, false
	}
	pr, exists := s.load()
	switch args[1] {
	case "create":
		repo := argAfter(args, "--repo")
		if repo == "" {
			repo = os.Getenv("FAKEAGENT_GH_PARENT")
		}
		pr = ghStoredPR{
			URL:   fmt.Sprintf("https://github.com/%s/pull/99", strings.TrimSuffix(repo, ".git")),
			Base:  argAfter(args, "--base"),
			Title: argAfter(args, "--title"),
			Body:  body,
		}
		if code := s.save(pr); code != 0 {
			return code, true
		}
		fmt.Println(pr.URL)
		return 0, true
	case "list":
		if !exists {
			return 0, false
		}
		out, _ := json.Marshal([]map[string]any{{"number": 99, "url": pr.URL, "baseRefName": pr.Base}})
		fmt.Println(string(out))
		return 0, true
	case "view":
		if !exists {
			return 0, false
		}
		switch {
		case hasArgValue(args, "--json", "title,body"):
			out, _ := json.Marshal(map[string]string{"title": pr.Title, "body": pr.Body})
			fmt.Println(string(out))
			return 0, true
		case hasArgValue(args, "--json", "baseRefName"):
			fmt.Println(pr.Base)
			return 0, true
		}
		return 0, false
	case "edit":
		if !exists {
			fmt.Fprintln(os.Stderr, "fakeagent gh: no pull request to edit")
			return 1, true
		}
		if title := argAfter(args, "--title"); title != "" {
			pr.Title = title
		}
		if base := argAfter(args, "--base"); base != "" {
			pr.Base = base
		}
		if hasArgValue(args, "--body-file", "-") {
			pr.Body = body
		}
		return s.save(pr), true
	}
	return 0, false
}
