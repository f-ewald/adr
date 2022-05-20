package main

import (
	"context"
	"github.com/adrg/frontmatter"
	"github.com/blevesearch/bleve/v2"
	"github.com/fatih/color"
	"github.com/gomarkdown/markdown"
	"github.com/gomarkdown/markdown/html"
	"github.com/gorilla/mux"
	"html/template"
	"io/ioutil"
	"log"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"time"
)

var (
	searchIndex bleve.Index
)

// Document represents a single decision record.
type Document struct {
	Filename string    `json:"-" yaml:"-"`
	Number   int       `json:"number" yaml:"number"`
	Title    string    `json:"title" yaml:"title"`
	Date     time.Time `json:"date" yaml:"date"`
	Status   string    `json:"status" yaml:"status"`
	Body     string    `json:"-" yaml:"-"`
}

func docFromMap(m map[string]interface{}) Document {
	number := m["number"].(float64)
	return Document{
		Filename: "",
		Number:   int(number),
		Title:    m["title"].(string),
		Date:     time.Time{},
		Status:   m["status"].(string),
		Body:     "",
	}
}

// doNothing returns an empty response.
// This is used for example to return an empty favicon.
func doNothing(w http.ResponseWriter, r *http.Request) {}

// handleList is the entry page into the application.
func handleList(w http.ResponseWriter, req *http.Request) {
	w.Header().Add("Content-Type", "text/html")
	tpl, err := template.New("list.tpl.html").ParseFS(fs,
		"tpl/styles.css", "tpl/base.tpl.html", "tpl/list.tpl.html")
	if err != nil {
		panic(err)
	}

	cfg := getConfig()
	rawFiles, err := ioutil.ReadDir(cfg.BaseDir)
	if err != nil {
		panic(err)
	}
	docs := make([]Document, 0)
	for i := 0; i < len(rawFiles); i++ {
		if rawFiles[i].IsDir() {
			// Ignore subdirectories
			continue
		}
		if !strings.HasSuffix(rawFiles[i].Name(), ".yaml") {
			// Ignore all rawFiles not ending in .md
			continue
		}
		filename := strings.TrimSuffix(rawFiles[i].Name(), ".yaml")
		filename = strings.Join(strings.Split(filename, "-"), " ")

		f, err := os.Open(filepath.Join(cfg.BaseDir, rawFiles[i].Name()))
		if err != nil {
			panic(err)
		}

		var d Document
		body, err := frontmatter.Parse(f, &d)
		if err != nil {
			_ = f.Close()
			panic(err)
		}
		_ = f.Close()
		d.Filename = rawFiles[i].Name()
		d.Body = string(body)

		docs = append(docs, d)
	}

	err = tpl.Execute(w, struct {
		Files []Document
	}{
		Files: docs,
	})
	if err != nil {
		panic(err)
	}
}

// handleDetail shows the details for a document.
func handleDetail(w http.ResponseWriter, req *http.Request) {
	item, ok := mux.Vars(req)["item"]
	if !ok {
		w.WriteHeader(http.StatusNotFound)
		return
	}

	cfg := getConfig()
	f, err := os.Open(filepath.Join(cfg.BaseDir, item))
	if err != nil {
		panic(err)
	}
	var d Document
	body, err := frontmatter.Parse(f, &d)
	if err != nil {
		_ = f.Close()
		panic(err)
	}
	_ = f.Close()
	d.Body = string(body)

	// Render Markdown document
	renderer := html.NewRenderer(html.RendererOptions{Flags: html.SkipHTML | html.Smartypants})
	b := markdown.ToHTML(body, nil, renderer)

	tpl, err := template.New("detail.tpl.html").ParseFS(fs,
		"tpl/styles.css", "tpl/base.tpl.html", "tpl/detail.tpl.html")
	if err != nil {
		panic(err)
	}
	err = tpl.Execute(w, struct {
		Doc  Document
		Body template.HTML
	}{
		Doc:  d,
		Body: template.HTML(b),
	})
	if err != nil {
		panic(err)
	}
}

// handleSearch shows the search results if any.
func handleSearch(w http.ResponseWriter, req *http.Request) {
	q := req.URL.Query().Get("q")
	query := bleve.NewFuzzyQuery(q)
	searchRequest := bleve.NewSearchRequest(query)
	searchRequest.Fields = []string{"*"}
	searchRequest.Highlight = bleve.NewHighlight()
	results, err := searchIndex.Search(searchRequest)
	if err != nil {
		panic(err)
	}

	docs := make([]Document, 0)
	for _, hit := range results.Hits {
		docs = append(docs, docFromMap(hit.Fields))
	}

	tpl, err := template.New("results.tpl.html").ParseFS(fs,
		"tpl/styles.css", "tpl/base.tpl.html", "tpl/results.tpl.html")
	if err != nil {
		panic(err)
	}
	w.Header().Set("Content-Type", "text/html")
	err = tpl.Execute(w, struct {
		Count int
		Query string
		Docs  []Document
	}{
		Count: int(results.Total),
		Query: q,
		Docs:  docs,
	})
	if err != nil {
		panic(err)
	}
}

// createIndex creates the in-memory search index that is used during runtime.
func createIndex() error {
	color.Green("Building search index...")

	// Search index in-memory
	searchIndex, err = bleve.New("", bleve.NewIndexMapping())
	if err != nil {
		return err
	}

	cfg := getConfig()
	fileInfos, err := ioutil.ReadDir(cfg.BaseDir)
	if err != nil {
		return err
	}
	var i int
	for i = 0; i < len(fileInfos); i++ {
		fileInfo := fileInfos[i]
		if fileInfo.IsDir() {
			continue
		}
		if !strings.HasSuffix(fileInfo.Name(), ".yaml") {
			continue
		}
		f, err := os.Open(filepath.Join(cfg.BaseDir, fileInfo.Name()))
		if err != nil {
			panic(err)
		}

		var d Document
		body, err := frontmatter.Parse(f, &d)
		if err != nil {
			_ = f.Close()
			panic(err)
		}
		_ = f.Close()
		d.Body = string(body)
		d.Filename = fileInfo.Name()

		normalizedFilename := strings.ToLower(fileInfo.Name())
		err = searchIndex.Index(normalizedFilename, d)
		if err != nil {
			return err
		}
	}
	color.Green("Search index built from %d documents.", i)
	return nil
}

// serve is the main function that registers the routes and starts the webserver.
func serve() {
	// Create search index
	err = createIndex()
	if err != nil {
		panic(err)
	}

	router := mux.NewRouter()

	// Ignore favicon requests
	router.HandleFunc("/favicon.ico", doNothing)

	router.HandleFunc("/search", handleSearch)
	router.HandleFunc("/{item}", handleDetail)
	router.HandleFunc("/", handleList)

	color.Green("Starting server on port 8090")
	srv := &http.Server{
		Handler:      router,
		Addr:         ":8090",
		ReadTimeout:  5 * time.Second,
		WriteTimeout: 5 * time.Second,
	}
	go func() {
		err = srv.ListenAndServe()
		if err != nil {
			panic(err)
		}
	}()

	c := make(chan os.Signal, 1)
	// We'll accept graceful shutdowns when quit via SIGINT (Ctrl+C)
	// SIGKILL, SIGQUIT or SIGTERM (Ctrl+/) will not be caught.
	signal.Notify(c, os.Interrupt)

	// Block until we receive our signal.
	<-c

	// Create a deadline to wait for.
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	// Doesn't block if no connections, but will otherwise wait
	// until the timeout deadline.
	_ = srv.Shutdown(ctx)
	// Optionally, you could run srv.Shutdown in a goroutine and block on
	// <-ctx.Done() if your application should wait for other services
	// to finalize based on context cancellation.
	log.Println("Shutting down")
	os.Exit(0)
}
