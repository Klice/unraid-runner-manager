package gitlab

import (
	"errors"
	"net/url"
	"regexp"
	"strings"
)

type Project struct {
	Scheme string
	Host   string
	Path   string
}

var (
	ErrInvalidProject = errors.New("project must be a GitLab project URL such as https://gitlab.com/group/project")
	segmentPattern    = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]*$`)
)

func ParseProject(input string) (Project, error) {
	s := strings.TrimSpace(input)
	if s == "" {
		return Project{}, ErrInvalidProject
	}
	if !strings.Contains(s, "://") {
		s = "https://" + s
	}
	u, err := url.Parse(s)
	if err != nil || u.Host == "" || (u.Scheme != "https" && u.Scheme != "http") {
		return Project{}, ErrInvalidProject
	}
	path := strings.Trim(u.Path, "/")
	if i := strings.Index(path, "/-/"); i >= 0 {
		path = path[:i]
	}
	path = strings.TrimSuffix(path, ".git")
	segments := strings.Split(path, "/")
	if len(segments) < 2 {
		return Project{}, ErrInvalidProject
	}
	for _, seg := range segments {
		if !segmentPattern.MatchString(seg) || seg == ".." {
			return Project{}, ErrInvalidProject
		}
	}
	return Project{Scheme: u.Scheme, Host: u.Host, Path: strings.Join(segments, "/")}, nil
}

func (p Project) BaseURL() string {
	return p.Scheme + "://" + p.Host
}

func (p Project) URL() string {
	return p.BaseURL() + "/" + p.Path
}

func (p Project) Display() string {
	if p.Host == "gitlab.com" {
		return p.Path
	}
	return p.Host + "/" + p.Path
}

func (p Project) RunnersSettingsURL() string {
	return p.URL() + "/-/settings/ci_cd"
}
