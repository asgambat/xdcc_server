package search

import (
	"context"
	"fmt"
	"io"
	"net/url"
	"strings"

	"xdcc-go/internal/entities"
)

// SubsPleaseEngine searches for XDCC packs on subsplease.org.
type SubsPleaseEngine struct {
	baseURL string // override for testing; empty = "https://subsplease.org"
}

func (e *SubsPleaseEngine) Name() string { return "subsplease" }

func (e *SubsPleaseEngine) Search(ctx context.Context, term string) ([]*entities.XDCCPack, error) {
	if term == "" {
		return nil, nil
	}

	base := resolveBaseURL(e.baseURL, "https://subsplease.org")
	searchURL := base + "/xdcc/search.php?t=" + url.PathEscape(term)

	resp, err := httpGet(ctx, searchURL)
	if err != nil {
		return nil, fmt.Errorf("subsplease request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 300 {
		return nil, fmt.Errorf("subsplease returned status %d (CloudFlare block?)", resp.StatusCode)
	}

	var bodyBytes []byte
	bodyBytes, err = io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("subsplease read failed: %w", err)
	}
	body := string(bodyBytes)

	var packs []*entities.XDCCPack
	results := strings.Split(body, ";")
	server := entities.NewIrcServer("irc.rizon.net")

	for _, result := range results {
		data, err := parseSubsPleaseResult(result)
		if err != nil {
			continue
		}
		bot := data["b"]
		filename := data["f"]
		var packNum, fileSize int
		_, _ = fmt.Sscanf(data["n"], "%d", &packNum)
		_, _ = fmt.Sscanf(data["s"], "%d", &fileSize)

		if bot == "" || packNum == 0 {
			continue
		}

		pack := entities.NewXDCCPack(server, bot, packNum)
		pack.SetFilename(filename, true)
		// SubsPlease reports sizes in decimal megabytes (SI), not mebibytes.
		pack.SetSize(int64(fileSize) * 1000 * 1000)
		packs = append(packs, pack)
	}

	return packs, nil
}

// parseSubsPleaseResult parses one semicolon-delimited subsplease result entry.
// Entry format: varname={b: "Bot", n:packnum, s:filesize, f:"filename"}
func parseSubsPleaseResult(result string) (map[string]string, error) {
	data := make(map[string]string)

	eqIdx := strings.Index(result, "=")
	if eqIdx < 0 {
		return nil, fmt.Errorf("no = found")
	}
	result = strings.TrimSpace(result[eqIdx+1:])

	// Strip surrounding braces
	result = strings.TrimPrefix(result, "{")
	result = strings.TrimSuffix(result, "}")

	// Split by quotes to separate strings from non-strings
	parts := strings.Split(result, "\"")
	var currentKey string

	for i, part := range parts {
		if i%2 == 0 {
			// Non-string content: key:value pairs separated by commas
			for _, segment := range strings.Split(part, ",") {
				segment = strings.TrimSpace(segment)
				if segment == "" {
					continue
				}
				kv := strings.SplitN(segment, ":", 2)
				if len(kv) < 2 {
					continue
				}
				currentKey = strings.TrimSpace(kv[0])
				val := strings.TrimSpace(kv[1])
				if val != "" {
					data[currentKey] = val
				}
			}
		} else if currentKey != "" {
			// String content
			data[currentKey] = part
		}
	}

	return data, nil
}
