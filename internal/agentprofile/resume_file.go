package agentprofile

import (
	"errors"
	"regexp"
	"strings"
)

var resumeFileLiteral = regexp.MustCompile(`^[A-Za-z0-9_.-]+$`)

func validateResumeFile(pattern string) error {
	parts := strings.Split(pattern, "/")
	if len(parts) < 3 || parts[len(parts)-2] != "*" || strings.Count(pattern, "*") != 1 || strings.Count(pattern, "{{session_id}}") != 1 {
		return errors.New("requires fixed directories, one /*/ directory and one filename {{session_id}} with a literal suffix")
	}
	for _, part := range parts[:len(parts)-2] {
		if part == "." || part == ".." || !resumeFileLiteral.MatchString(part) {
			return errors.New("directories must be clean literal relative components")
		}
	}
	file := parts[len(parts)-1]
	_, suffix, found := strings.Cut(file, "{{session_id}}")
	if !found || suffix == "" || !resumeFileLiteral.MatchString(strings.Replace(file, "{{session_id}}", "", 1)) {
		return errors.New("filename must contain only {{session_id}} and literal filename characters with a suffix")
	}
	return nil
}
