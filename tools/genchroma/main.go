// Generates web/assets/chroma.css from the same chroma style the renderer
// uses. Re-run after style changes:
//
//	go run ./tools/genchroma > web/assets/chroma.css
package main

import (
	"os"

	chromahtml "github.com/alecthomas/chroma/v2/formatters/html"
	"github.com/alecthomas/chroma/v2/styles"
)

func main() {
	f := chromahtml.New(chromahtml.WithClasses(true))
	if err := f.WriteCSS(os.Stdout, styles.Get("github")); err != nil {
		panic(err)
	}
}
