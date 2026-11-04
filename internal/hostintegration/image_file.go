package hostintegration

import (
	"fmt"
	"net/url"
	"path"
	"path/filepath"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/leogr/yoloctl/internal/safepath"
)

// clipboardFilePath accepts one local PNG file URI or absolute pathname. Never
// expand shell text, resolve remote authorities, or take just one of many files.
func clipboardFilePath(value string, uriList bool) string {
	if uriList {
		var item string
		for line := range strings.SplitSeq(value, "\n") {
			line = strings.TrimSuffix(line, "\r")
			if line == "" || strings.HasPrefix(line, "#") {
				continue
			}
			if item != "" {
				return ""
			}
			item = line
		}
		value = item
	} else {
		value = strings.TrimSuffix(strings.TrimSuffix(value, "\n"), "\r")
	}
	if strings.HasPrefix(value, "file:") {
		u, err := url.Parse(value)
		if err != nil || u.Opaque != "" || u.User != nil || u.RawQuery != "" || u.ForceQuery || u.Fragment != "" ||
			(u.Host != "" && !strings.EqualFold(u.Host, "localhost")) {
			return ""
		}
		value = u.Path
	} else if uriList {
		return ""
	}
	if !ValidImageFilePath(value) || !strings.EqualFold(filepath.Ext(value), ".png") {
		return ""
	}
	return value
}

// ValidImageFilePath is shared by both ends of the private reuse probe. Paths
// have a bounded, unambiguous spelling and can never contain terminal controls.
func ValidImageFilePath(value string) bool {
	if len(value) == 0 || len(value) > MaxImagePathBytes || !utf8.ValidString(value) ||
		!path.IsAbs(value) || path.Clean(value) != value || value == "/" {
		return false
	}
	for _, r := range value {
		if unicode.IsControl(r) || unicode.In(r, unicode.Cf) {
			return false
		}
	}
	return true
}

func readImageFile(value string) (ClipboardImage, error) {
	canonical, err := filepath.EvalSymlinks(value)
	if err != nil {
		return ClipboardImage{}, fmt.Errorf("read copied PNG file: %w", err)
	}
	if !ValidImageFilePath(canonical) {
		return ClipboardImage{}, fmt.Errorf("copied PNG path is invalid or too long")
	}
	// The source is an explicit local clipboard selection. Resolve its aliases,
	// then reject a replaced parent/leaf and nonregular files. The same bounded
	// bytes are used for hashing and fallback upload; no guest supplies this path.
	data, err := safepath.ReadFile(filepath.Dir(canonical), filepath.Base(canonical), MaxImageBytes)
	if err != nil {
		return ClipboardImage{}, fmt.Errorf("read copied PNG file: %w", err)
	}
	if err := ValidateImage(data); err != nil {
		return ClipboardImage{}, err
	}
	return ClipboardImage{Data: data, Path: canonical}, nil
}

// ImagePathText keeps simple paths bare. Quote other filenames as one shell
// word so a paste into a managed shell cannot become expansions or commands.
// It is text for the receiving CLI, not an image-attachment API.
func ImagePathText(value string) string {
	for _, r := range value {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || strings.ContainsRune("/._-", r) {
			continue
		}
		return "'" + strings.ReplaceAll(value, "'", "'\"'\"'") + "'"
	}
	return value
}
