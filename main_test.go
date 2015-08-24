package main

import (
	"bytes"
	"log"
	"testing"

	"github.com/PuerkitoBio/goquery"
	"github.com/boltdb/bolt"
)

func TestResolveImageURLs(t *testing.T) {
	testData := []struct {
		url, in, out string
	}{{
		"http://localhost/foo/",
		`<img src="https://google.com"/>
		<img src="/blue"/>
		<img src="moo"/>`,
		`<img src="https://google.com"/>
		<img src="http://localhost/blue"/>
		<img src="http://localhost/foo/moo"/>`,
	}}
	for i, td := range testData {
		reader := bytes.NewReader([]byte(td.in))
		doc, err := goquery.NewDocumentFromReader(reader)
		if err != nil {
			t.Error(err)
		}
		err = resolveImageURLs(td.url, doc)
		if err != nil {
			t.Error(err)
		}
		out, err := doc.Find("body").Html()
		if err != nil {
			t.Error(err)
		}
		if out != td.out {
			t.Errorf("%d. resolveImageURLs(%#v, doc) = %#v; not %#v", i, td.url, out, td.out)
		}
	}
}

func TestInlineImages(t *testing.T) {
	done, err := initDB()
	if err != nil {
		t.Fatal(err)
	}
	defer done()

	log.Println("initDBed")

	db.Update(func(tx *bolt.Tx) error {
		bucket := tx.Bucket([]byte("comics"))
		for _, img := range []string{"img1.png", "img2.jpg"} {
			bImage := []byte("http://localhost/" + img)
			bucket.Put(bImage, bImage)
		}
		return nil
	})

	log.Println("putIntoCached")

	testData := []struct {
		in, out string
	}{{
		`<img src="http://localhost/img1.png">
		<img src="http://localhost/img2.jpg">`,
		"",
	}}
	for i, td := range testData {
		out, err := inlineImages([]byte(td.in))
		if err != nil {
			t.Error(err)
		}
		if string(out) != td.out {
			t.Errorf("%d. inlineImages(%#v) = %#v; not %#v", i, td.in, string(out), td.out)
		}
	}
}
