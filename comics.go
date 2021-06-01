package main

import (
	"encoding/json"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"

	"github.com/gernest/front"
	"github.com/imdario/mergo"
	"github.com/pkg/errors"
	"github.com/russross/blackfriday"
)

type Comic struct {
	Name         string   `json:"name,omitempty"`
	Slug         string   `json:"slug,omitempty"`
	Rating       string   `json:"rating,omitempty"`
	Genres       []string `json:"genres,omitempty"`
	Page         int      `json:"idx,omitempty"`
	LastPage     int      `json:"max_idx,omitempty"`
	URL          string   `json:"uri,omitempty"`
	BannerURL    string   `json:"banner_url"`
	Description  string   `json:"description,omitempty"`
	ExtractRules []string `json:"extract_rules,omitempty"`

	Service string `json:"service,omitempty"`
}

func extractCookiesToForward(r *http.Request, slug string) []*http.Cookie {
	var validCookies []*http.Cookie
	prefix := slug + "-"
	for _, c := range r.Cookies() {
		if strings.HasPrefix(c.Name, prefix) {
			c.Name = strings.TrimPrefix(c.Name, prefix)
			validCookies = append(validCookies, c)
		}
	}
	return validCookies
}

func (s *server) getComicRocketComics(r *http.Request) ([]*Comic, error) {
	req, err := http.NewRequest("GET", COMIC_ROCKET_COMICS_URL, nil)
	if err != nil {
		return nil, err
	}
	client := http.Client{}
	client.Jar, _ = cookiejar.New(nil)
	u, _ := url.Parse(COMIC_ROCKET_COMICS_URL)
	client.Jar.SetCookies(u, extractCookiesToForward(r, ComicRocketSlug))
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	var comics []*Comic
	if err := json.NewDecoder(resp.Body).Decode(&comics); err != nil {
		return nil, err
	}

	if err := s.annotateComics(comics, ComicRocketSlug); err != nil {
		return nil, err
	}

	return comics, nil
}

func (s *server) getComics(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")

	var comicsSourceFuncs = map[string]func(r *http.Request) ([]*Comic, error){
		ComicRocketSlug: s.getComicRocketComics,
	}

	var combinedComicsMu struct {
		sync.Mutex
		Comics []*Comic
	}
	var wg sync.WaitGroup
	for s, f := range comicsSourceFuncs {
		s := s
		f := f
		wg.Add(1)
		go func() {
			defer wg.Done()
			comics, err := f(r)
			if err != nil {
				http.Error(w, errors.Wrapf(err, "getComics: %s", s).Error(), 500)
				return
			}
			combinedComicsMu.Lock()
			defer combinedComicsMu.Unlock()
			combinedComicsMu.Comics = append(combinedComicsMu.Comics, comics...)
		}()
	}
	wg.Wait()

	sort.Sort(ComicsSlice(combinedComicsMu.Comics))

	if err := json.NewEncoder(w).Encode(combinedComicsMu.Comics); err != nil {
		http.Error(w, err.Error(), 500)
	}
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
		matter := front.NewMatter()
		matter.Handle("---", front.YAMLHandler)
		metaData, page, err := matter.Parse(file)
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
		comic.Description = string(blackfriday.MarkdownCommon([]byte(page)))
		s.comicDB[comic.Slug] = comic
	}
	return nil
}

func (s *server) annotateComics(comics []*Comic, service string) error {
	for _, comic := range comics {
		comic.Service = service
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

// ComicsSlice attaches the methods of Interface to []*Comic, sorting in increasing order.
type ComicsSlice []*Comic

func (p ComicsSlice) Len() int           { return len(p) }
func (p ComicsSlice) Less(i, j int) bool { return p[i].Name < p[j].Name }
func (p ComicsSlice) Swap(i, j int)      { p[i], p[j] = p[j], p[i] }
