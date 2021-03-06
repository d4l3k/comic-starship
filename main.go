package main

import (
	"bytes"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"io/ioutil"
	"log"
	"mime"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"text/template"
	"time"

	"github.com/PuerkitoBio/goquery"
	"github.com/boltdb/bolt"
	"github.com/gorilla/mux"
	"github.com/pkg/errors"
	"github.com/vincent-petithory/dataurl"
)

//go:generate npm install
//go:generate bower install --allow-root

var debug = flag.Bool("debug", false, "whether to revulcanize on every request")

const (
	COMIC_ROCKET_COMICS_URL = "https://www.comic-rocket.com/api/1/marked/"
	COMIC_ROCKET_BASE_URL   = "https://www.comic-rocket.com/"
	ComicRocketSlug         = "comicrocket"
)

func getCSRFToken(client *http.Client) (string, error) {
	resp, err := client.Get("https://www.comic-rocket.com/login?next=/")
	if err != nil {
		return "", err
	}
	doc, err := goquery.NewDocumentFromResponse(resp)
	if err != nil {
		return "", err
	}
	log.Println("cookies", resp.Cookies())
	return doc.Find("[name=\"csrfmiddlewaretoken\"]").AttrOr("value", ""), nil
}

type serialResp struct {
	Slug string `json:"slug,omitempty"`
	URL  string `json:"url,omitempty"`
}

func getComic(slug string, page int) (*serialResp, error) {
	metaURL := fmt.Sprintf("https://www.comic-rocket.com/api/1/serial/%s/%d/", slug, page)
	log.Println("Fetching comic page meta", metaURL)
	b, err := httpGetOrCache(metaURL)
	if err != nil {
		return nil, err
	}
	serial := &serialResp{}
	reader := bytes.NewReader(b)
	return serial, json.NewDecoder(reader).Decode(serial)
}

var inFlightRequestsLock sync.Mutex
var inFlightRequests = map[string]*sync.WaitGroup{}

func httpGetOrCache(link string) ([]byte, error) {
	inFlightRequestsLock.Lock()
	req, ok := inFlightRequests[link]
	if ok {
		req.Wait()
	}
	inFlightRequestsLock.Unlock()
	var body, updated []byte
	if err := db.View(func(tx *bolt.Tx) error {
		bucket := tx.Bucket([]byte("comics"))
		body = bucket.Get([]byte(link))

		bucketUpdated := tx.Bucket([]byte("comics-updated"))
		updated = bucketUpdated.Get([]byte(link))
		return nil
	}); err != nil {
		return nil, err
	}

	updateTime := time.Now()
	if err := updateTime.UnmarshalText(updated); err != nil {
		updated = nil
	}

	if len(body) == 0 || len(updated) == 0 || (time.Now().Sub(updateTime) > 24*time.Hour) {
		log.Println("Fetching", link)

		inFlightRequestsLock.Lock()
		var wg sync.WaitGroup
		wg.Add(1)
		inFlightRequests[link] = &wg
		inFlightRequestsLock.Unlock()

		resp, err := http.Get(link)
		if err != nil {
			return nil, err
		}
		body, err = ioutil.ReadAll(resp.Body)
		if err != nil {
			return nil, err
		}

		if err := db.Update(func(tx *bolt.Tx) error {
			bucket := tx.Bucket([]byte("comics"))
			bucketUpdated := tx.Bucket([]byte("comics-updated"))
			bucket.Put([]byte(link), body)
			t, err := time.Now().MarshalText()
			if err != nil {
				return err
			}
			bucketUpdated.Put([]byte(link), t)
			return nil
		}); err != nil {
			return nil, err
		}
		wg.Done()
	} else {
		log.Println("Cached", link)
	}
	return body, nil
}

func fetchDoc(link string) (*goquery.Document, error) {
	body, err := httpGetOrCache(link)
	if err != nil {
		return nil, err
	}
	reader := bytes.NewReader(body)
	return goquery.NewDocumentFromReader(reader)
}

func fetchImages(slug string, page int) ([]string, error) {
	serial, err := getComic(slug, page)
	if err != nil {
		return nil, err
	}

	doc, err := fetchDoc(serial.URL)
	if err != nil {
		return nil, err
	}

	if err = resolveImageURLs(serial.URL, doc); err != nil {
		return nil, err
	}

	imgs := doc.Find("img")
	var urls []string
	imgs.Each(func(_ int, img *goquery.Selection) {
		src := img.AttrOr("src", "")

		// Blacklist gravatar pictures since they don't contribute anything.
		if !strings.Contains(src, "gravatar.com") {
			urls = append(urls, src)
		}
	})
	return urls, nil
}

func resolveImageURLs(page string, doc *goquery.Document) error {
	baseURL, err := url.Parse(page)
	if err != nil {
		return err
	}
	doc.Find("img").EachWithBreak(func(i int, img *goquery.Selection) bool {
		rawURL := strings.TrimSpace(img.AttrOr("src", ""))
		if strings.HasSuffix(rawURL, "%0A") {
			rawURL = rawURL[len(rawURL)-3:]
		}
		relURL, err2 := url.Parse(rawURL)
		if err2 != nil {
			err = err2
			return false
		}
		img.SetAttr("src", baseURL.ResolveReference(relURL).String())
		return true
	})
	return err
}

func (s *server) getComicImages(slug string, page int) ([]byte, error) {
	comic, ok := s.comicDB[slug]
	if ok && len(comic.ExtractRules) > 0 {
		rules := comic.ExtractRules
		serial, err := getComic(slug, page)
		if err != nil {
			return nil, err
		}
		doc, err := fetchDoc(serial.URL)
		if err != nil {
			return nil, err
		}
		if err = resolveImageURLs(serial.URL, doc); err != nil {
			return nil, err
		}
		var body string
		for _, rule := range rules {
			ruleBody, err := doc.Find(rule).Html()
			if err != nil {
				return nil, err
			}
			body += ruleBody
		}
		return []byte(body), nil
	}
	var urls, constURLs []string
	var err error
	wg := &sync.WaitGroup{}
	wg.Add(2)
	go func() {
		var err2 error
		urls, err2 = fetchImages(slug, page)
		if err2 != nil {
			err = err2
		}
		wg.Done()
	}()
	go func() {
		pPage := page - 1
		if pPage <= 0 {
			pPage = page + 1
		}
		var err2 error
		constURLs, err2 = fetchImages(slug, pPage)

		if err2 != nil {
			err = err2
		}
		wg.Done()
	}()
	wg.Wait()
	if err != nil {
		return nil, err
	}
	existing := make(map[string]bool)
	for _, url := range constURLs {
		existing[url] = true
	}
	var finalImages string
	for _, url := range urls {
		if _, ok := existing[url]; ok {
			continue
		}
		finalImages += "<img src=\"" + url + "\">"
	}
	return []byte(finalImages), nil
}

func inlineImages(body []byte) ([]byte, error) {
	reader := bytes.NewReader(body)
	doc, err := goquery.NewDocumentFromReader(reader)
	if err != nil {
		return nil, err
	}
	wg := &sync.WaitGroup{}
	var err2 error
	doc.Find("img").Each(func(i int, sel *goquery.Selection) {
		wg.Add(1)
		go func() {
			link := strings.TrimSpace(sel.AttrOr("src", ""))
			img, err := httpGetOrCache(link)
			if err != nil {
				err2 = err
			}
			typ := http.DetectContentType(img)
			if typ == "application/octet-stream" {
				if typ2 := mime.TypeByExtension(filepath.Ext(link)); len(typ2) > 0 {
					typ = typ2
				}
			}
			dataURL := dataurl.New(img, typ)
			sel.SetAttr("src", dataURL.String())
			wg.Done()
		}()
	})
	wg.Wait()
	if err != nil {
		return nil, err
	}
	out, err := doc.Html()
	return []byte(out), err
}

var db *bolt.DB

func initDB() (func() error, error) {
	var err error
	db, err = bolt.Open("comics.db", 0600, nil)
	if err != nil {
		return db.Close, err
	}

	err = db.Update(func(tx *bolt.Tx) error {
		for _, bucket := range []string{"comics", "comics-updated"} {
			_, err = tx.CreateBucketIfNotExists([]byte(bucket))
			if err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return db.Close, err
	}
	return db.Close, nil
}

type server struct {
	comicDB map[string]*Comic
}

func (s *server) markComic(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	r.ParseForm()
	for _, urlTmpl := range []string{"http://www.comic-rocket.com/read/%s/%s?mark", "https://www.comic-rocket.com/navbar/%s/%s/?mark"} {
		markURL := fmt.Sprintf(urlTmpl, r.FormValue("slug"), r.FormValue("idx"))
		log.Printf("MarkComic URL %s", markURL)
		req, err := http.NewRequest("GET", markURL, nil)
		if err != nil {
			http.Error(w, err.Error(), 503)
			return
		}
		req.Header = r.Header
		client := http.Client{}
		client.Jar, _ = cookiejar.New(nil)
		u, _ := url.Parse(markURL)
		client.Jar.SetCookies(u, r.Cookies())
		resp, err := client.Do(req)
		if err != nil {
			http.Error(w, err.Error(), 503)
			return
		}
		defer resp.Body.Close()
		io.Copy(w, resp.Body)
	}
}

func (s *server) getComicPage(w http.ResponseWriter, r *http.Request) {
	service := r.URL.Query().Get("service")
	slug := r.URL.Query().Get("slug")
	page, err := strconv.Atoi(r.URL.Query().Get("p"))
	if err != nil {
		http.Error(w, err.Error(), 400)
		return
	}
	var resp []byte
	switch service {
	case ComicRocketSlug:
		imgs, err := s.getComicImages(slug, page)
		if err != nil {
			http.Error(w, err.Error(), 400)
			return
		}
		resp, err = inlineImages(imgs)
		if err != nil {
			http.Error(w, err.Error(), 400)
			return
		}

	default:
		http.Error(w, fmt.Sprintf("invalid service: %q", service), 400)
		return
	}

	w.Header().Set("Content-Type", "text/html")
	w.Write(resp)
}

func (s *server) getVideo(w http.ResponseWriter, r *http.Request) {
	service := r.URL.Query().Get("service")
	slug := r.URL.Query().Get("slug")
	page, err := strconv.Atoi(r.URL.Query().Get("p"))
	if err != nil {
		http.Error(w, err.Error(), 400)
		return
	}
	_ = slug
	_ = page
	switch service {
	default:
		http.Error(w, fmt.Sprintf("invalid service: %q", service), 400)
		return
	}
}

var tmpls = template.Must(template.ParseFiles("templates/cache.manifest"))

func (s *server) cacheManifest(w http.ResponseWriter, r *http.Request) {
	/*if *debug {
		version = time.Now().String() + " debug"
	}*/
	if err := tmpls.Lookup("cache.manifest").Execute(w, version); err != nil {
		http.Error(w, err.Error(), 500)
	}
}

func (s *server) loginComicRocket(r *http.Request) (bool, []*http.Cookie, error) {
	v := r.Form
	v.Add("next", "/")
	jar, _ := cookiejar.New(nil)
	client := http.Client{
		Jar: jar,
	}
	token, err := getCSRFToken(&client)
	if err != nil {
		return false, nil, err
	}
	v.Add("csrfmiddlewaretoken", token)
	log.Print("token", token, v)

	req, err := http.NewRequest("POST", "https://www.comic-rocket.com/login", strings.NewReader(v.Encode()))
	if err != nil {
		return false, nil, err
	}
	req.Header.Add("Referer", "https://www.comic-rocket.com/login?next=/")
	req.Header.Add("Origin", "https://www.comic-rocket.com")
	req.Header.Add("Host", "www.comic-rocket.com")
	req.Header.Add("User-Agent", "Mozilla/5.0 (X11; Linux x86_64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/42.0.2311.90 Safari/537.36")
	resp, err := client.Do(req)
	if err != nil {
		return false, nil, err
	}
	defer resp.Body.Close()
	body, err := ioutil.ReadAll(resp.Body)
	if err != nil {
		return false, nil, err
	}
	u, _ := url.Parse(COMIC_ROCKET_COMICS_URL)
	cookies := client.Jar.Cookies(u)
	return bytes.Contains(body, []byte("My Comics")), cookies, nil
	/*
	 */
}

func (s *server) login(w http.ResponseWriter, r *http.Request) {
	http.DefaultClient.Jar, _ = cookiejar.New(nil)
	r.ParseForm()
	service := r.FormValue("service")
	var (
		success bool
		cookies []*http.Cookie
		err     error
	)
	switch service {
	case ComicRocketSlug:
		success, cookies, err = s.loginComicRocket(r)
	default:
		err = errors.Errorf("invalid service %q", service)
		return
	}
	if err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	for _, c := range cookies {
		c.Name = service + "-" + c.Name
		c.Path = "/"
		w.Header().Add("Set-Cookie", c.String())
	}
	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(success); err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
}

var version = time.Now().String()

func main() {
	flag.Parse()

	versionBytes, err := ioutil.ReadFile(".git/refs/heads/master")
	if err != nil {
		log.Printf("Failed to read git version %v", err)
	} else {
		version += " " + string(versionBytes)
	}
	log.Printf("Version %s", version)

	done, err := initDB()
	if err != nil {
		log.Fatal(err)
	}
	defer done()

	s := &server{}
	if err := s.loadComicDetails(); err != nil {
		log.Fatal(err)
	}

	ro := mux.NewRouter()
	ro.Path("/cache.manifest").Methods("GET").HandlerFunc(s.cacheManifest)
	ro.Path("/comicstarship/").Methods("GET").HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("debug"))
	})

	api := ro.PathPrefix("/api").Subrouter()
	api.Path("/comics").Methods("GET").HandlerFunc(s.getComics)
	api.Path("/markcomic").Methods("POST").HandlerFunc(s.markComic)
	api.Path("/comic").HandlerFunc(s.getComicPage)
	api.Path("/video.m3u8").Methods("GET").HandlerFunc(s.getVideo)
	api.Path("/video.mp4").Methods("GET").HandlerFunc(s.getVideo)
	api.Path("/login").Methods("POST").HandlerFunc(s.login)

	ro.PathPrefix("/lib/").Handler(http.FileServer(http.Dir("./public")))
	ro.PathPrefix("/static/").Handler(http.FileServer(http.Dir("./public")))
	ro.PathPrefix("/pages/").Handler(http.FileServer(http.Dir("./public")))

	if *debug {
		ro.Path("/").HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			index, err := vulcanize()
			if err != nil {
				http.Error(w, err.Error(), 500)
			}
			w.Write(index)
		})
	} else {
		index, err := vulcanize()
		if err != nil {
			log.Fatal(err)
		}
		ro.Path("/").HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Write(index)
		})
	}

	http.Handle("/", ro)
	log.Println("Listening 0.0.0.0:8282")
	log.Fatal(http.ListenAndServe("0.0.0.0:8282", nil))
}
