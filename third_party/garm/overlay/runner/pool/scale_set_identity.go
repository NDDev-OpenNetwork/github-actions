// Copyright 2026 Cloudbase Solutions SRL
//
//    Licensed under the Apache License, Version 2.0 (the "License"); you may
//    not use this file except in compliance with the License. You may obtain
//    a copy of the License at
//
//         http://www.apache.org/licenses/LICENSE-2.0
//
//    Unless required by applicable law or agreed to in writing, software
//    distributed under the License is distributed on an "AS IS" BASIS, WITHOUT
//    WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied. See the
//    License for the specific language governing permissions and limitations
//    under the License.

package pool

import (
	"fmt"
	"net/url"
	"strconv"
	"strings"
	"unicode"

	"github.com/google/go-github/v84/github"

	"github.com/cloudbase/garm/params"
)

const (
	scaleSetListPageSize = 100
	scaleSetListMaxPages = 10
	githubActionsAppSlug = "github-actions"
)

type checkRunURLIdentity struct {
	host  string
	owner string
	repo  string
	id    int64
}

func parseCheckRunURL(raw string) (checkRunURLIdentity, bool) {
	parsed, err := url.Parse(raw)
	if err != nil || parsed.Scheme != "https" || parsed.User != nil || parsed.Opaque != "" || parsed.Fragment != "" {
		return checkRunURLIdentity{}, false
	}
	host := strings.ToLower(parsed.Hostname())
	if host == "" {
		return checkRunURLIdentity{}, false
	}
	parts := strings.Split(strings.Trim(parsed.Path, "/"), "/")
	reposAt := -1
	for index, part := range parts {
		if part == "repos" {
			reposAt = index
			break
		}
	}
	if reposAt < 0 || reposAt+5 != len(parts) || parts[reposAt+3] != "check-runs" {
		return checkRunURLIdentity{}, false
	}
	owner := parts[reposAt+1]
	repo := parts[reposAt+2]
	if !validCheckRunPathName(owner) || !validCheckRunPathName(repo) {
		return checkRunURLIdentity{}, false
	}
	id, err := strconv.ParseInt(parts[reposAt+4], 10, 64)
	if err != nil || id <= 0 {
		return checkRunURLIdentity{}, false
	}
	return checkRunURLIdentity{host: host, owner: owner, repo: repo, id: id}, true
}

func validCheckRunPathName(name string) bool {
	if name == "" || name == "." || name == ".." {
		return false
	}
	for _, r := range name {
		if unicode.IsLetter(r) || unicode.IsDigit(r) || r == '-' || r == '_' || r == '.' {
			continue
		}
		return false
	}
	return true
}

func checkRunIDFromURL(raw string) (int64, bool) {
	parsed, ok := parseCheckRunURL(raw)
	if !ok {
		return 0, false
	}
	return parsed.id, true
}

func workflowJobBindsCheckRun(job *github.WorkflowJob, check *github.CheckRun) bool {
	if job == nil || check == nil || check.GetID() == 0 {
		return false
	}
	jobURL, ok := parseCheckRunURL(job.GetCheckRunURL())
	if !ok || jobURL.id != check.GetID() {
		return false
	}
	listed, ok := parseCheckRunURL(check.GetURL())
	if !ok || listed.id != check.GetID() {
		return false
	}
	return jobURL.host == listed.host && strings.EqualFold(jobURL.owner, listed.owner) && strings.EqualFold(jobURL.repo, listed.repo)
}

func isGitHubActionsCheckRun(check *github.CheckRun) bool {
	if check == nil || check.App == nil || check.App.GetSlug() == "" {
		return false
	}
	return strings.EqualFold(check.App.GetSlug(), githubActionsAppSlug)
}

func validTerminalConclusion(conclusion string) bool {
	switch conclusion {
	case "success", "failure", "cancelled", "skipped", "timed_out", "startup_failure":
		return true
	default:
		return false
	}
}

func exactHeadSHA(values ...string) (string, bool) {
	var sha string
	for _, value := range values {
		if value == "" {
			return "", false
		}
		if sha == "" {
			sha = value
			continue
		}
		if !strings.EqualFold(sha, value) {
			return "", false
		}
	}
	return sha, sha != ""
}

type githubListCursor struct {
	seenPages     map[int]struct{}
	seenIDs       map[int64]struct{}
	collected     int
	expectedTotal int
	hasTotal      bool
}

func newGitHubListCursor() githubListCursor {
	return githubListCursor{
		seenPages: map[int]struct{}{},
		seenIDs:   map[int64]struct{}{},
	}
}

func (c *githubListCursor) accept(requestedPage int, resp *github.Response, ids []int64, total *int) (done bool, ok bool) {
	if c == nil || resp == nil || total == nil || *total < 0 {
		return false, false
	}
	if !c.hasTotal {
		c.expectedTotal = *total
		c.hasTotal = true
	} else if *total != c.expectedTotal {
		return false, false
	}
	if _, exists := c.seenPages[requestedPage]; exists {
		return false, false
	}
	c.seenPages[requestedPage] = struct{}{}
	for _, id := range ids {
		if id == 0 {
			return false, false
		}
		if _, exists := c.seenIDs[id]; exists {
			return false, false
		}
		c.seenIDs[id] = struct{}{}
	}
	c.collected += len(ids)
	if c.collected > c.expectedTotal {
		return false, false
	}
	if resp.NextPage != 0 {
		if resp.NextPage == requestedPage || resp.NextPage < 0 {
			return false, false
		}
		if _, exists := c.seenPages[resp.NextPage]; exists {
			return false, false
		}
		return false, true
	}
	if c.collected != c.expectedTotal {
		return false, false
	}
	return true, true
}

func exactScaleSetForJob(scaleSets []params.ScaleSet, job params.Job) (params.ScaleSet, error) {
	if len(job.Labels) == 0 {
		return params.ScaleSet{}, fmt.Errorf("queued job has no scale-set labels")
	}
	matches := make([]params.ScaleSet, 0, 1)
	for _, scaleSet := range scaleSets {
		if !scaleSet.Enabled {
			continue
		}
		for _, label := range job.Labels {
			if label == scaleSet.Name {
				matches = append(matches, scaleSet)
				break
			}
		}
	}
	if len(matches) != 1 {
		return params.ScaleSet{}, fmt.Errorf("queued job matches %d enabled scale sets", len(matches))
	}
	return matches[0], nil
}
