package web

import (
	"bytes"
	"fmt"
	"html/template"
	"io/ioutil"
	"log"
	"net/http"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/carneades/carneades-4/src/common"
	"github.com/carneades/carneades-4/src/engine/caes"
	"github.com/carneades/carneades-4/src/engine/caes/encoding/agxml"
	"github.com/carneades/carneades-4/src/engine/caes/encoding/aif"
	"github.com/carneades/carneades-4/src/engine/caes/encoding/caf"
	"github.com/carneades/carneades-4/src/engine/caes/encoding/dot"
	"github.com/carneades/carneades-4/src/engine/caes/encoding/graphml"
	"github.com/carneades/carneades-4/src/engine/caes/encoding/lkif"
	"github.com/carneades/carneades-4/src/engine/caes/encoding/yaml"
	"github.com/carneades/carneades-4/src/engine/dung"
	ddot "github.com/carneades/carneades-4/src/engine/dung/encoding/dot"
	dgml "github.com/carneades/carneades-4/src/engine/dung/encoding/graphml"
	"github.com/carneades/carneades-4/src/engine/dung/encoding/tgf"
)

const afLimit = 20   // max number of arguments handled by the Dung solver
const timeLimit = 15 // seconds, for running Dot

type templateHandler struct {
	once         sync.Once
	filename     string
	templatesDir string
	templ        *template.Template
	Version      string // Carneades version number
}

func newTemplateHandler(templatesDir string, filename string) *templateHandler {
	return &templateHandler{filename: filename, templatesDir: templatesDir, Version: common.Version}
}

func (t *templateHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	t.once.Do(func() {
		t.templ = template.Must(template.ParseFiles(filepath.Join(t.templatesDir, t.filename)))
	})
	t.templ.Execute(w, t)
}

func CarneadesServer(port string, templatesDir string) {

	var errorTemplate = template.Must(template.ParseFiles(filepath.Join(templatesDir, "error.html")))

	evalHandler := func(w http.ResponseWriter, req *http.Request) {
		inputFormat := req.FormValue("input-format")
		outputFormat := req.FormValue("output-format")
		file, _, err := req.FormFile("datafile")
		if err != nil {
			errorTemplate.Execute(w, err.Error())
			return
		}
		data, err := ioutil.ReadAll(file)
		if err != nil {
			errorTemplate.Execute(w, err.Error())
			return
		}

		var ag *caes.ArgGraph
		rd := bytes.NewReader(data)

		switch inputFormat {
		case "yaml":
			ag, err = yaml.Import(rd)
			if err != nil {
				errorTemplate.Execute(w, err.Error())
				return
			}
		case "agxml":
			ag, err = agxml.Import(rd)
			if err != nil {
				errorTemplate.Execute(w, err.Error())
				return
			}
		case "aif":
			ag, err = aif.Import(rd)
			if err != nil {
				errorTemplate.Execute(w, err.Error())
				return
			}
		case "lkif":
			ag, err = lkif.Import(rd)
			if err != nil {
				errorTemplate.Execute(w, err.Error())
				return
			}
		case "caf":
			ag, err = caf.Import(rd)
			if err != nil {
				errorTemplate.Execute(w, err.Error())
				return
			}
		default:
			errorTemplate.Execute(w, fmt.Sprintf("unknown or unsupported input format: %s\n", inputFormat))
			return
		}

		// Apply the theory of the argument graph, if any, to
		// derive further arguments
		ag.Infer()
		// evaluate the argument graph, using grounded semantics
		// and update the labels of the statements in the argument graph
		l := ag.GroundedLabelling()
		// fmt.Printf("labelling=%v\n", l)
		ag.ApplyLabelling(l)

		switch outputFormat {
		case "yaml":
			yaml.Export(w, ag)
		case "graphml":
			err = graphml.Export(w, ag)
			if err != nil {
				errorTemplate.Execute(w, err.Error())
				return
			}
		case "dot":
			err = dot.Export(w, *ag)
			if err != nil {
				errorTemplate.Execute(w, err.Error())
				return
			}
		case "png", "svg":
			cmd := exec.Command("dot", "-T"+outputFormat)
			w2 := bytes.NewBuffer([]byte{})
			cmd.Stdin = w2
			cmd.Stdout = w
			err = dot.Export(w2, *ag)
			if err != nil {
				errorTemplate.Execute(w, err.Error())
				return
			}
			// Limit the runtime of dot
			cmd.Start()
			done := make(chan error, 1)
			go func() {
				done <- cmd.Wait()
			}()
			select {
			case <-time.After(timeLimit * time.Second):
				cmd.Process.Kill()
			case err := <-done:
				if err != nil {
					errorTemplate.Execute(w, err.Error())
					return
				}
			}

		default:
			errorTemplate.Execute(w, fmt.Sprintf("unknown or unsupported output format: %s\n", outputFormat))
			return
		}
	}

	dungHandler := func(w http.ResponseWriter, req *http.Request) {
		semantics := req.FormValue("semantics")
		outputFormat := req.FormValue("output-format")
		file, _, err := req.FormFile("datafile")
		if err != nil {
			errorTemplate.Execute(w, err.Error())
			return
		}
		data, err := ioutil.ReadAll(file)
		if err != nil {
			errorTemplate.Execute(w, err.Error())
			return
		}

		var af dung.AF
		rd := bytes.NewReader(data)

		af, err = tgf.Import(rd)
		if err != nil {
			errorTemplate.Execute(w, err.Error())
			return
		} else if len(af.Args()) > afLimit {
			errorTemplate.Execute(w, fmt.Sprintf("Argumentation frameworks with more than %v arguments are not supported by this server.\n", afLimit))
			return
		}

		// evaluate the argumentation framework, using the selected semantics
		var extensions []dung.ArgSet

		switch semantics {
		case "complete":
			if outputFormat == "text" {
				extensions = af.CompleteExtensions()
			} else {
				e, ok := af.SomeExtension(dung.Complete)
				if ok {
					extensions = []dung.ArgSet{e}
				} else {
					extensions = []dung.ArgSet{}
				}
			}
		case "preferred":
			if outputFormat == "text" {
				extensions = af.PreferredExtensions()
			} else {
				e, ok := af.SomeExtension(dung.Preferred)
				if ok {
					extensions = []dung.ArgSet{e}
				} else {
					extensions = []dung.ArgSet{}
				}
			}
		case "stable":
			if outputFormat == "text" {
				extensions = af.StableExtensions()
			} else {
				e, ok := af.SomeExtension(dung.Stable)
				if ok {
					extensions = []dung.ArgSet{e}
				} else {
					extensions = []dung.ArgSet{}
				}
			}
		default:
			extensions = []dung.ArgSet{af.GroundedExtension()}
		}

		printExtensions := func(extensions []dung.ArgSet) {
			s := []string{}
			for _, E := range extensions {
				s = append(s, E.String())
			}
			fmt.Fprintf(w, "[%s]\n", strings.Join(s, ","))
		}

		var as dung.ArgSet
		if len(extensions) == 0 {
			as = dung.NewArgSet()
		} else {
			as = extensions[0]
		}
		switch outputFormat {
		case "graphml":
			dgml.Export(w, af, as)
		case "dot":
			err = ddot.Export(w, af, as)
			if err != nil {
				errorTemplate.Execute(w, err.Error())
				return
			}
		case "png", "svg":
			cmd := exec.Command("dot", "-T"+outputFormat)
			w2 := bytes.NewBuffer([]byte{})
			cmd.Stdin = w2
			cmd.Stdout = w
			err = ddot.Export(w2, af, as)
			if err != nil {
				errorTemplate.Execute(w, err.Error())
				return
			}
			// limit the runtime of Dot
			cmd.Start()
			done := make(chan error, 1)
			go func() {
				done <- cmd.Wait()
			}()
			select {
			case <-time.After(timeLimit * time.Second):
				cmd.Process.Kill()
			case err := <-done:
				if err != nil {
					errorTemplate.Execute(w, err.Error())
					return
				}
			}
		default: // text
			printExtensions(extensions)
		}
	}

	http.Handle("/", newTemplateHandler(templatesDir, "carneades.html"))
	http.Handle("/help", newTemplateHandler(templatesDir, "help.html"))
	http.Handle("/eval-form", newTemplateHandler(templatesDir, "eval-form.html"))
	http.Handle("/eval-help", newTemplateHandler(templatesDir, "eval-help.html"))
	http.HandleFunc("/eval", evalHandler)
	http.Handle("/dung-form", newTemplateHandler(templatesDir, "dung-form.html"))
	http.Handle("/dung-help", newTemplateHandler(templatesDir, "dung-help.html"))
	http.HandleFunc("/dung", dungHandler)
	http.Handle("/imprint", newTemplateHandler(templatesDir, "imprint.html"))

	// start the web server
	if err := http.ListenAndServe(":"+port, nil); err != nil {
		log.Fatal("carneades:", err)
	}
}