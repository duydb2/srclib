package cli

import (
	"encoding/json"
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"

	"strings"

	"sourcegraph.com/sourcegraph/srclib/dep"
	"sourcegraph.com/sourcegraph/srclib/graph"
	"sourcegraph.com/sourcegraph/srclib/unit"
)

func init() {
	c, err := CLI.AddCommand("gen-data",
		"generates fake data",
		`generates fake data and outputs to .srclib-cache for debugging imports.`,
		&genDataCmd,
	)
	if err != nil {
		log.Fatal(err)
	}
	c.Aliases = []string{"c"}
}

type GenDataCmd struct {
	Repo     string `short:"r" long:"repo" description:"repo to build" required:"yes"`
	CommitID string `short:"c" long:"commit" description:"commit ID to build"`
	NUnits   int    `long:"nunits" description:"number of units to generate" default:"1"`
	NFiles   int    `long:"nfiles" description:"number of files to generate per unit" default:"1"`
	NDefs    int    `long:"ndefs" description:"number of defs to generate per file" required:"yes"`
	NRefs    int    `long:"nrefs" description:"number of refs to generate per file" required:"yes"`

	GenSource bool `long:"gen-source" description:"whether to emit source files for the generated data"`
}

var genDataCmd GenDataCmd

type unitInfo struct {
	Unit  *unit.SourceUnit
	Graph *graph.Output
	Deps  []*dep.Resolution
}

func (c *GenDataCmd) Execute(args []string) error {
	if c.CommitID == "" && !c.GenSource {
		return fmt.Errorf("--commit must be non-empty or --gen-source must be true")
	}

	units := make([]*unit.SourceUnit, c.NUnits)
	for u := 0; u < c.NUnits; u++ {
		units[u] = &unit.SourceUnit{
			Name:     fmt.Sprintf("unit_%d", u),
			Type:     "GoPackage",
			Repo:     c.Repo,
			CommitID: c.CommitID,
			Files:    []string{},
			Dir:      fmt.Sprintf("unit_%d", u),
		}
	}

	if c.GenSource {
		// generate source files
		for _, ut := range units {
			err := c.genUnit(ut)
			if err != nil {
				return err
			}
		}

		// get commit ID
		_, err := exec.Command("git", "add", "-A", ":/").Output()
		if err != nil {
			return err
		}
		_, err = exec.Command("git", "commit", "-m", "generated source").Output()
		if err != nil {
			return err
		}
		out, err := exec.Command("git", "log", "--pretty=oneline", "-n1").Output()
		if err != nil {
			return err
		}
		commitID := strings.Fields(string(out))[0]

		// update command to generate graph data
		c.CommitID = commitID
		c.GenSource = false
	}

	// generate graph data
	for _, ut := range units {
		ut.CommitID = c.CommitID

		err := c.genUnit(ut)
		if err != nil {
			return err
		}
	}

	return nil
}

func makeFilename(unitName string, file int) string {
	return filepath.Join(unitName, "subpackage", fmt.Sprintf("file%d.go", file))
}

func (c *GenDataCmd) genUnit(ut *unit.SourceUnit) error {
	defs := make([]*graph.Def, 0)
	refs := make([]*graph.Ref, 0)
	docs := make([]*graph.Doc, 0)

	for f := 0; f < c.NFiles; f++ {
		filename := makeFilename(ut.Name, f)

		ut.Files = append(ut.Files, filename)
		fileDefs, fileRefs, fileDocs, err := c.genFile(ut, filename, f)
		if err != nil {
			return err
		}
		defs, refs, docs = append(defs, fileDefs...), append(refs, fileRefs...), append(docs, fileDocs...)
	}

	if !c.GenSource {
		gr := graph.Output{Defs: defs, Refs: refs, Docs: docs}
		dp := make([]*dep.Resolution, 0)

		unitDir := filepath.Join(".srclib-cache", ut.CommitID, ut.Name)
		if err := os.MkdirAll(unitDir, 0700); err != nil {
			return err
		}

		unitFile, err := os.OpenFile(filepath.Join(unitDir, fmt.Sprintf("%s.unit.json", ut.Type)), os.O_CREATE|os.O_RDWR|os.O_TRUNC, 0666)
		if err != nil {
			return err
		}
		defer unitFile.Close()

		if err := json.NewEncoder(unitFile).Encode(ut); err != nil {
			return err
		}

		graphFile, err := os.OpenFile(filepath.Join(unitDir, fmt.Sprintf("%s.graph.json", ut.Type)), os.O_CREATE|os.O_RDWR|os.O_TRUNC, 0666)
		if err != nil {
			return err
		}
		defer graphFile.Close()

		if err := json.NewEncoder(graphFile).Encode(gr); err != nil {
			return err
		}

		depFile, err := os.OpenFile(filepath.Join(unitDir, fmt.Sprintf("%s.depresolve.json", ut.Type)), os.O_CREATE|os.O_RDWR|os.O_TRUNC, 0666)
		if err != nil {
			return err
		}
		defer depFile.Close()

		if err := json.NewEncoder(depFile).Encode(dp); err != nil {
			return err
		}
	}

	return nil
}

func (c *GenDataCmd) genFile(ut *unit.SourceUnit, filename string, f int) (defs []*graph.Def, refs []*graph.Ref, docs []*graph.Doc, err error) {
	offset := 0
	defName := "foo"
	docstring := "// this is a docstring"

	var sourceFile *os.File
	if c.GenSource {
		err := os.MkdirAll(filepath.Dir(filename), 0700)
		if err != nil {
			return nil, nil, nil, err
		}
		file, err := os.Create(filename)
		if err != nil {
			return nil, nil, nil, err
		}
		sourceFile = file
	}

	for i := 0; i < c.NDefs; i++ {
		def := &graph.Def{
			DefKey: graph.DefKey{
				Repo:     ut.Repo,
				CommitID: ut.CommitID,
				UnitType: ut.Type,
				Unit:     ut.Name,
				Path:     filepath.Join("package", "subpackage", "type", fmt.Sprintf("method_%d_%d", f, i)),
			},
			Name:     defName,
			Exported: true,
			File:     filename,
			DefStart: uint32(offset),
			DefEnd:   uint32(offset + len(defName)),
		}
		if sourceFile != nil {
			_, err := sourceFile.WriteString(def.Name + " ")
			if err != nil {
				return nil, nil, nil, err
			}
		}
		offset += len(defName) + 1
		defs = append(defs, def)

		doc := &graph.Doc{
			DefKey: def.DefKey,
			Data:   docstring,
			File:   def.File,
			Start:  uint32(offset),
			End:    uint32(offset + len(docstring)),
		}
		if sourceFile != nil {
			_, err := sourceFile.WriteString(docstring + "\n")
			if err != nil {
				return nil, nil, nil, err
			}
		}
		offset += len(docstring) + 1
		docs = append(docs, doc)

		defRef := &graph.Ref{
			DefRepo:     def.Repo,
			DefUnitType: def.UnitType,
			DefUnit:     def.Unit,
			DefPath:     def.Path,
			Repo:        def.Repo,
			CommitID:    def.CommitID,
			UnitType:    def.UnitType,
			Unit:        def.Unit,
			Def:         true,
			File:        def.File,
			Start:       def.DefStart,
			End:         def.DefEnd,
		}
		refs = append(refs, defRef)
	}

	for i, defIdx := 0, 0; i < c.NRefs; i, defIdx = i+1, (defIdx+1)%c.NDefs {
		ref := &graph.Ref{
			DefRepo:     ut.Repo,
			DefUnitType: ut.Type,
			DefUnit:     ut.Name,
			DefPath:     filepath.Join("package", "subpackage", "type", fmt.Sprintf("method_%d_%d", f, defIdx)),
			Repo:        ut.Repo,
			CommitID:    ut.CommitID,
			UnitType:    ut.Type,
			Unit:        ut.Name,
			Def:         false,
			File:        filename,
			Start:       uint32(offset),
			End:         uint32(offset + len(defName)),
		}
		refs = append(refs, ref)

		if sourceFile != nil {
			_, err := sourceFile.WriteString(defName + "\n")
			if err != nil {
				return nil, nil, nil, err
			}
		}

		offset += len(defName) + 1
	}

	// Close source file
	if sourceFile != nil {
		sourceFile.Close()
	}

	return defs, refs, docs, nil
}