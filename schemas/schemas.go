// Package schemas embeds the JSON Schemas for profiles, address datasets and
// golden tables, and validates documents against them.
package schemas

import (
	"bytes"
	"embed"
	"fmt"
	"io/fs"
	"sort"
	"strings"
	"sync"

	"github.com/santhosh-tekuri/jsonschema/v6"
	"golang.org/x/text/language"
	"golang.org/x/text/message"
)

// Schema names.
const (
	Network        = "network-profile.schema.json"
	Agency         = "agency-profile.schema.json"
	AddressDataset = "address-dataset.schema.json"
	GoldenTable    = "golden-table.schema.json"
)

const baseURL = "https://github.com/RIKIKU/PagerArchive/schemas/"

//go:embed *.schema.json
var files embed.FS

var (
	once     sync.Once
	compiled map[string]*jsonschema.Schema
)

func load() {
	c := jsonschema.NewCompiler()
	names, err := fs.Glob(files, "*.schema.json")
	if err != nil {
		panic(err)
	}
	for _, name := range names {
		data, _ := files.ReadFile(name)
		doc, err := jsonschema.UnmarshalJSON(bytes.NewReader(data))
		if err != nil {
			panic(fmt.Sprintf("schema %s: %v", name, err))
		}
		if err := c.AddResource(baseURL+name, doc); err != nil {
			panic(fmt.Sprintf("schema %s: %v", name, err))
		}
	}
	compiled = make(map[string]*jsonschema.Schema, len(names))
	for _, name := range names {
		compiled[name] = c.MustCompile(baseURL + name)
	}
}

// Problem is one schema violation at a JSON Pointer path in the document.
type Problem struct {
	Path    string
	Message string
}

func (p Problem) String() string { return "at " + p.Path + ": " + p.Message }

// Validate checks data against the named schema. A document that isn't JSON
// is reported as a single problem at the root.
func Validate(schema string, data []byte) []Problem {
	once.Do(load)
	s, ok := compiled[schema]
	if !ok {
		panic("unknown schema " + schema)
	}
	inst, err := jsonschema.UnmarshalJSON(bytes.NewReader(data))
	if err != nil {
		return []Problem{{Path: "/", Message: "not valid JSON: " + err.Error()}}
	}
	err = s.Validate(inst)
	if err == nil {
		return nil
	}
	ve, ok := err.(*jsonschema.ValidationError)
	if !ok {
		return []Problem{{Path: "/", Message: err.Error()}}
	}
	p := message.NewPrinter(language.English)
	var out []Problem
	var walk func(*jsonschema.ValidationError)
	walk = func(e *jsonschema.ValidationError) {
		if len(e.Causes) == 0 {
			out = append(out, Problem{
				Path:    "/" + strings.Join(e.InstanceLocation, "/"),
				Message: e.ErrorKind.LocalizedString(p),
			})
			return
		}
		for _, c := range e.Causes {
			walk(c)
		}
	}
	walk(ve)
	sort.SliceStable(out, func(i, j int) bool { return out[i].Path < out[j].Path })
	return out
}
