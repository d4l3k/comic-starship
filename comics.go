package main

import (
	"encoding/json"
	"log"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"

	funimation "github.com/d4l3k/go-funimation"
	"github.com/imdario/mergo"
	"github.com/pkg/errors"
	"github.com/russross/blackfriday"
	"github.com/spf13/hugo/parser"
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

func (s *server) getFunimationVideos(r *http.Request) ([]*Comic, error) {
	c := funimation.NewClient()
	cookies := extractCookiesToForward(r, FunimationSlug)
	c.SetCookies(cookies)
	resp, err := c.Queue(10000, 0)
	if err != nil {
		return nil, err
	}
	comics := make([]*Comic, len(resp.Queue))
	var wg sync.WaitGroup
	for i, show := range resp.Queue {
		i := i
		show := show
		wg.Add(1)
		go func() {
			defer wg.Done()

			video := resp.NextVideo[show.ShowID]
			log.Printf("video %+v %+v", show, video)
			ep, err2 := strconv.Atoi(video.VideoSequence)
			if err2 != nil {
				err = err2
				return
			}
			ep--
			showDetails, err2 := funimation.GetShow(show.ShowID)
			if err2 != nil {
				err = err2
				return
			}
			comics[i] = &Comic{
				Name:        show.Title,
				Slug:        show.ShowURL,
				LastPage:    showDetails.EpisodeCount,
				Page:        ep,
				Service:     FunimationSlug,
				Description: showDetails.SeriesDescription,
				BannerURL:   showDetails.ThumbnailLarge,
				URL:         showDetails.Link,
				Genres:      strings.Split(showDetails.Genres, ","),
			}
		}()
	}
	wg.Wait()
	if err != nil {
		return nil, err
	}
	return comics, nil
}

func (s *server) getComics(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")

	comics, err := s.getComicRocketComics(r)
	if err != nil {
		http.Error(w, errors.Wrap(err, "getComics: comicrocket").Error(), 500)
		return
	}

	videos, err := s.getFunimationVideos(r)
	if err != nil {
		http.Error(w, errors.Wrap(err, "getComics: funimation").Error(), 500)
		return
	}
	log.Println("videos", videos)
	comics = append(comics, videos...)

	sort.Sort(ComicsSlice(comics))

	if err := json.NewEncoder(w).Encode(comics); err != nil {
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
