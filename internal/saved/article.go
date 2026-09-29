package saved

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	htmltomarkdown "github.com/JohannesKaufmann/html-to-markdown/v2"
	readability "github.com/go-shiori/go-readability"
	"github.com/jackc/pgx/v5/pgtype"

	"discodrive/internal/db"
)

const maxArticleBytes = 10 << 20

// articleMeta is the meta jsonb payload for article items.
type articleMeta struct {
	Site    string `json:"site,omitempty"`
	Byline  string `json:"byline,omitempty"`
	Excerpt string `json:"excerpt,omitempty"`
}

// processArticle stores the readable content of the page as markdown with a
// frontmatter in <user>/Downloads/articles/<year>/<slug>.md. The content comes
// either from the browser extension (content_html — extracted from the live
// DOM by Readability.js: works for paywalls, SPA pages and bot-walls) or from
// a server-side fetch + go-readability. The file lands inside the user tree,
// so it is picked up by rescan (visible in Files) and by the sync daemon.
// Images stay as external links.
func (s *Service) processArticle(ctx context.Context, item db.SavedItem) (result, error) {
	pageURL, err := url.Parse(item.Url)
	if err != nil {
		return result{}, err
	}

	var articleHTML, extractedTitle, byline, excerpt string
	if strings.TrimSpace(item.ContentHtml.String) != "" {
		// The client already extracted the article from the live DOM, so no
		// server-side fetch is needed.
		articleHTML = item.ContentHtml.String
	} else {
		if err := s.Validate(item.Url); err != nil {
			return result{}, err
		}
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, item.Url, nil)
		if err != nil {
			return result{}, err
		}
		req.Header.Set("Accept", "text/html,application/xhtml+xml")
		resp, err := s.Client.Do(req)
		if err != nil {
			return result{}, err
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			return result{}, fmt.Errorf("status %d", resp.StatusCode)
		}

		// go-readability handles charset detection internally.
		art, err := readability.FromReader(io.LimitReader(resp.Body, maxArticleBytes), pageURL)
		if err != nil {
			return result{}, fmt.Errorf("readability: %w", err)
		}
		if strings.TrimSpace(art.TextContent) == "" {
			return result{}, fmt.Errorf("no readable content (likely a JS-rendered page — save it from the extension popup)")
		}
		articleHTML, extractedTitle, byline, excerpt = art.Content, art.Title, art.Byline, art.Excerpt
	}

	md, err := htmltomarkdown.ConvertString(articleHTML)
	if err != nil {
		return result{}, fmt.Errorf("markdown: %w", err)
	}
	if strings.TrimSpace(md) == "" {
		return result{}, fmt.Errorf("no readable content in the extracted article")
	}

	title := item.Title
	if title == "" {
		title = extractedTitle
	}
	doc := frontmatter(item.Url, title, byline) + "\n" + strings.TrimSpace(md) + "\n"

	slug := Slugify(title)
	if slug == "" {
		slug = Slugify(pageURL.Hostname())
	}
	if slug == "" {
		slug = "article"
	}
	// An article is a few kilobytes of Markdown, but it is still the user's bytes on
	// the user's disk: a full quota refuses it like any other write.
	budget, err := s.budget(ctx, item)
	if err != nil {
		return result{}, err
	}
	if int64(len(doc)) > budget {
		return result{}, quotaErr(budget)
	}

	uid := db.UUIDString(item.UserID)
	// Next to the downloads: everything the extension saves lives in one folder
	// of the user's storage, and the articles/<year> subfolder keeps the
	// articles apart from the files.
	dir := uid + "/Downloads/articles/" + time.Now().UTC().Format("2006")
	destRel, err := s.freeName(dir, slug+".md")
	if err != nil {
		return result{}, err
	}
	tmpRel := ".tmp/saved-" + randHex(16)
	size, hash, err := s.st.WriteFile(tmpRel, strings.NewReader(doc))
	if err != nil {
		_ = s.st.Remove(tmpRel)
		return result{}, err
	}
	nodeID, err := s.publish(ctx, item.UserID, tmpRel, destRel, size, hash)
	if err != nil {
		_ = s.st.Remove(tmpRel)
		return result{}, err
	}

	metaJSON, err := json.Marshal(articleMeta{Site: pageURL.Hostname(), Byline: byline, Excerpt: excerpt})
	if err != nil {
		return result{}, err
	}
	res := result{
		contentPath: pgtype.Text{String: destRel, Valid: true},
		nodeID:      nodeID,
		size:        pgtype.Int8{Int64: size, Valid: true},
		meta:        metaJSON,
	}
	if item.Title == "" {
		res.title = extractedTitle
	}
	return res, nil
}

// frontmatter renders the YAML header of a stored article.
func frontmatter(rawURL, title, byline string) string {
	var b strings.Builder
	b.WriteString("---\n")
	fmt.Fprintf(&b, "url: %s\n", rawURL)
	fmt.Fprintf(&b, "title: %q\n", title)
	if byline != "" {
		fmt.Fprintf(&b, "byline: %q\n", byline)
	}
	fmt.Fprintf(&b, "saved: %s\n", time.Now().UTC().Format("2006-01-02"))
	b.WriteString("---\n")
	return b.String()
}
