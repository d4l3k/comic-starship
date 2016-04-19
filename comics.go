package main

import (
	"encoding/json"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"os"
	"path/filepath"
	"strings"

	"github.com/imdario/mergo"
	"github.com/russross/blackfriday"
	"github.com/spf13/hugo/parser"
)

type Comic struct {
	Name        string   `json:"name,omitempty"`
	Slug        string   `json:"slug,omitempty"`
	Rating      string   `json:"rating,omitempty"`
	Genres      []string `json:"genres,omitempty"`
	Page        int      `json:"idx,omitempty"`
	LastPage    int      `json:"max_idx,omitempty"`
	URL         string   `json:"uri,omitempty"`
	BannerURL   string   `json:"banner_url"`
	Description string   `json:"description,omitempty"`
}

func (s *server) getComics(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	req, err := http.NewRequest("GET", COMIC_ROCKET_COMICS_URL, nil)
	if err != nil {
		http.Error(w, err.Error(), 503)
		return
	}
	req.Header = r.Header
	client := http.Client{}
	client.Jar, _ = cookiejar.New(nil)
	u, _ := url.Parse(COMIC_ROCKET_COMICS_URL)
	client.Jar.SetCookies(u, r.Cookies())
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		http.Error(w, err.Error(), 503)
		return
	}

	var comics []*Comic
	if err := json.NewDecoder(resp.Body).Decode(&comics); err != nil {
		http.Error(w, err.Error(), 503)
		return
	}

	if err := s.annotateComics(comics); err != nil {
		http.Error(w, err.Error(), 503)
		return
	}

	json.NewEncoder(w).Encode(comics)
}

func (s *server) loadComicDetails() error {
	s.comicDB = make(map[string]*Comic)

	matches, err := filepath.Glob("./comics/*.md")
	if err != nil {
		return err
	}
	for _, match := range matches {
		file, err := os.Open(match)
		if err != nil {
			return err
		}
		basename := filepath.Base(match)
		slug := strings.TrimSuffix(basename, filepath.Ext(basename))
		comic := &Comic{
			Slug: slug,
		}
		page, err := parser.ReadFrom(file)
		if err != nil {
			return err
		}
		metaData, err := page.Metadata()
		if err != nil {
			return err
		}
		jsonStr, err := json.Marshal(metaData)
		if err != nil {
			return err
		}
		if err := json.Unmarshal(jsonStr, &comic); err != nil {
			return err
		}
		comic.Description = string(blackfriday.MarkdownCommon(page.Content()))
		s.comicDB[comic.Slug] = comic
	}
	return nil
}

func (s *server) annotateComics(comics []*Comic) error {
	for _, comic := range comics {
		c, ok := s.comicDB[comic.Slug]
		if !ok {
			continue
		}
		if err := mergo.Merge(comic, *c); err != nil {
			return err
		}
	}
	return nil
}
