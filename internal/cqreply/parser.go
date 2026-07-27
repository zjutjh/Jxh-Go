package cqreply

import (
	"html"
	"net/url"
	"path"
	"strings"
	"unicode/utf8"
)

const (
	PartText         = "text"
	PartImage        = "image"
	PartFile         = "file"
	PartRejectedFile = "rejected_file"
	localMediaRoot   = "/app/jxh-media"
)

type Part struct {
	Type  string
	Value string
	Name  string
}

type Result struct {
	Parts              []Part
	PlainText          string
	ImageCount         int
	RejectedImageCount int
	FileCount          int
	RejectedFileCount  int
}

func Parse(answer string) Result {
	var result Result
	var plain strings.Builder
	remaining := answer

	appendText := func(text string) {
		if text == "" {
			return
		}
		plain.WriteString(text)
		if n := len(result.Parts); n > 0 && result.Parts[n-1].Type == PartText {
			result.Parts[n-1].Value += text
			return
		}
		result.Parts = append(result.Parts, Part{Type: PartText, Value: text})
	}

	for remaining != "" {
		start := strings.Index(remaining, "[CQ:")
		if start < 0 {
			appendText(remaining)
			break
		}
		appendText(remaining[:start])
		remaining = remaining[start:]

		end := strings.IndexByte(remaining, ']')
		if end < 0 {
			appendText(remaining)
			break
		}
		tag := remaining[:end+1]
		remaining = remaining[end+1:]

		mediaType, source, name, isMedia := mediaFromTag(tag)
		if !isMedia {
			appendText(tag)
			continue
		}
		if source == "" {
			if mediaType == PartImage {
				result.RejectedImageCount++
			} else {
				result.RejectedFileCount++
				result.Parts = append(result.Parts, Part{Type: PartRejectedFile})
			}
			continue
		}
		result.Parts = append(result.Parts, Part{Type: mediaType, Value: source, Name: name})
		if mediaType == PartImage {
			result.ImageCount++
		} else {
			result.FileCount++
		}
	}

	result.PlainText = plain.String()
	return result
}

func mediaFromTag(tag string) (mediaType, source, name string, ok bool) {
	body := strings.TrimSuffix(strings.TrimPrefix(tag, "[CQ:"), "]")
	parts := strings.Split(body, ",")
	if len(parts) == 0 || (parts[0] != PartImage && parts[0] != PartFile) {
		return "", "", "", false
	}
	mediaType = parts[0]

	params := make(map[string]string, len(parts)-1)
	for _, part := range parts[1:] {
		key, value, found := strings.Cut(part, "=")
		if !found {
			continue
		}
		params[strings.TrimSpace(key)] = html.UnescapeString(strings.TrimSpace(value))
	}
	for _, key := range []string{"url", "file"} {
		if !isRemoteURL(params[key]) {
			continue
		}
		if mediaType == PartFile {
			name = remoteFileName(params[key])
			if name == "" {
				continue
			}
		}
		return mediaType, params[key], name, true
	}

	local := localMediaPath(params["file"])
	if local == "" {
		return mediaType, "", "", true
	}
	if mediaType == PartFile {
		name = path.Base(local)
		if !validFileName(name) {
			return mediaType, "", "", true
		}
		return mediaType, local, name, true
	}
	return mediaType, (&url.URL{Scheme: "file", Path: local}).String(), "", true
}

func remoteFileName(source string) string {
	parsed, err := url.ParseRequestURI(source)
	if err != nil {
		return ""
	}
	name := path.Base(parsed.Path)
	if !validFileName(name) {
		return ""
	}
	return name
}

func validFileName(name string) bool {
	if name == "" || name == "." || name == ".." || len(name) > 255 || !utf8.ValidString(name) ||
		strings.TrimSpace(name) != name || strings.HasSuffix(name, ".") {
		return false
	}
	for _, r := range name {
		if r < ' ' || r == 0x7f || strings.ContainsRune(`/\:*?"<>|`, r) {
			return false
		}
	}
	return true
}

func isRemoteURL(value string) bool {
	if value == "" {
		return false
	}
	parsed, err := url.ParseRequestURI(value)
	return err == nil &&
		(strings.EqualFold(parsed.Scheme, "http") || strings.EqualFold(parsed.Scheme, "https")) &&
		parsed.Host != "" && parsed.User == nil
}

func localMediaPath(value string) string {
	if value == "" || path.IsAbs(value) || strings.ContainsAny(value, `\:?#`) {
		return ""
	}
	for _, segment := range strings.Split(value, "/") {
		if segment == "" || segment == "." || segment == ".." {
			return ""
		}
	}
	cleaned := path.Clean(value)
	if cleaned == "." || cleaned == ".." || strings.HasPrefix(cleaned, "../") {
		return ""
	}
	return path.Join(localMediaRoot, cleaned)
}
