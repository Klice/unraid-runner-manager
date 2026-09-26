package github

import (
	"errors"
	"regexp"
	"strings"
)

type Repo struct {
	Owner string
	Name  string
}

var (
	ErrInvalidRepo   = errors.New("repository must look like owner/repo or https://github.com/owner/repo")
	ErrInvalidLabel  = errors.New("labels may only contain letters, digits, dot, dash and underscore")
	ownerPattern     = regexp.MustCompile(`^[A-Za-z0-9](?:[A-Za-z0-9-]{0,37}[A-Za-z0-9])?$`)
	namePattern      = regexp.MustCompile(`^[A-Za-z0-9._-]{1,100}$`)
	labelPattern     = regexp.MustCompile(`^[A-Za-z0-9._-]{1,64}$`)
	githubURLPrefixs = []string{"https://github.com/", "http://github.com/", "github.com/", "git@github.com:"}
)

func ParseRepo(input string) (Repo, error) {
	s := strings.TrimSpace(input)
	for _, p := range githubURLPrefixs {
		if strings.HasPrefix(strings.ToLower(s), p) {
			s = s[len(p):]
			break
		}
	}
	s = strings.TrimSuffix(strings.TrimSuffix(s, "/"), ".git")
	parts := strings.Split(s, "/")
	if len(parts) != 2 || !ownerPattern.MatchString(parts[0]) || !namePattern.MatchString(parts[1]) {
		return Repo{}, ErrInvalidRepo
	}
	if parts[1] == "." || parts[1] == ".." {
		return Repo{}, ErrInvalidRepo
	}
	return Repo{Owner: parts[0], Name: parts[1]}, nil
}

func (r Repo) FullName() string {
	return r.Owner + "/" + r.Name
}

func (r Repo) URL() string {
	return "https://github.com/" + r.FullName()
}

func (r Repo) NewRunnerURL() string {
	return r.URL() + "/settings/actions/runners/new"
}

func ParseLabels(input string) ([]string, error) {
	var out []string
	seen := map[string]bool{}
	for _, raw := range strings.FieldsFunc(input, func(r rune) bool { return r == ',' || r == ' ' || r == '\n' }) {
		label := strings.TrimSpace(raw)
		if label == "" {
			continue
		}
		if !labelPattern.MatchString(label) {
			return nil, ErrInvalidLabel
		}
		if !seen[label] {
			seen[label] = true
			out = append(out, label)
		}
	}
	return out, nil
}
