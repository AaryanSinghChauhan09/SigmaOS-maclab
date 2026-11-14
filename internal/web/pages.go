package web

import (
	"embed"
	"html/template"
	"io"
	"io/fs"
	"strings"
)

//go:embed templates
var templateFiles embed.FS

// pages is every page by its file under templates, without ".html": each is
// the layout with the page's own "main" and, if it has one, "head".
var pages = loadPages()

func loadPages() map[string]*template.Template {
	layout := template.Must(template.ParseFS(templateFiles, "templates/layout.html"))
	out := map[string]*template.Template{}
	err := fs.WalkDir(templateFiles, "templates", func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() || path == "templates/layout.html" {
			return err
		}
		page := template.Must(template.Must(layout.Clone()).ParseFS(templateFiles, path))
		out[strings.TrimSuffix(strings.TrimPrefix(path, "templates/"), ".html")] = page
		return nil
	})
	if err != nil {
		panic(err)
	}
	return out
}

// WritePage writes the page name, such as "reports/deletion", with data: the
// page's own fields and a Title.
func WritePage(w io.Writer, name string, data any) error {
	return pages[name].ExecuteTemplate(w, "layout.html", data)
}
