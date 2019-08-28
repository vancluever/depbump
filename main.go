package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"html/template"
	"os"
	"os/exec"
	"strings"
)

type commitTemplateData struct {
	Project string
	Version string
	Path    string
	URL     string
}

var commitTemplate = template.Must(
	template.New("commit-template").Parse(strings.TrimSpace(`
modules: upgrade {{.Project}} to {{.Version}}

This updates
  {{.Path}}

To version {{.Version}}.

Executed via:

  go get {{Path}}@{{.Version}}
	go mod tidy
	go mod vendor # If this project has a vendor dir.

For details on changes, see the project's release page.
{{- if .URL }}  {{.URL}}{{- end}}

This commit message was auto-generated.
`),
	))

// Type from "go help mod edit"
type pkgInfoGoMod struct {
	Require []pkgInfoRequire
}

// Type from "go help mod edit"
type pkgInfoRequire struct {
	Path    string
	Version string
}

// execCommand returns a newly initialized *exec.Cmd, and connects
// stderr.
func execCommand(cmd string, args ...string) *exec.Cmd {
	c := exec.Command(cmd, args...)
	c.Stderr = os.Stderr
	return c
}

// execCommandRun runs a command, connecting both stdout and stderr.
func execCommandRun(cmd string, args ...string) error {
	c := exec.Command(cmd, args...)
	c.Stdout = os.Stdout
	c.Stderr = os.Stderr
	return c.Run()
}

// fatal prints error messages to stderr, and exits.
func fatal(err interface{}) {
	fmt.Fprintln(os.Stderr, err)
	os.Exit(1)
}

// fatalf prints error messages to stderr, and exits.
// arguments are the same as fmt.Printf.
func fatalf(format string, a ...interface{}) {
	fmt.Fprintf(os.Stderr, format, a...)
	os.Exit(1)
}

// pkgVersion returns the version string of the supplied package.
func pkgVersion(path string) string {
	out, err := execCommand("go", "mod", "edit", "-json").Output()
	if err != nil {
		fatal(err)
	}

	var info pkgInfoGoMod
	if err := json.Unmarshal(out, &info); err != nil {
		fatal(err)
	}

	for _, req := range info.Require {
		if req.Path == path {
			return req.Version
		}
	}

	fatalf("package %q not found in go.mod, cannot get version", path)
	return ""
}

func main() {
	if len(os.Args) < 2 {
		fatal("usage: depbump PATH [VERSION]")
	}

	path := os.Args[1]
	if path == "" {
		fatal("fatal: path is empty\nusage: depbump PATH [VERSION]")
	}

	var version string
	if len(os.Args) > 2 {
		version = os.Args[2]
	}

	// Require clean repo before continuing
	out, err := execCommand("git", "status", "--porcelain").Output()
	if err != nil {
		fatal(err)
	}

	if len(out) > 0 {
		fatal("fatal: uncommitted changes in repository, please commit or stash before continuing")
	}

	oldVersion := pkgVersion(path)
	if oldVersion == version {
		fatalf("fatal: package %s is already at version %s", path, version)
	}

	// Upgrade package
	args := []string{"get", path}
	if version != "" {
		args = append(args, version)
	}

	if err := execCommandRun("go", args...); err != nil {
		fatal(err)
	}

	newVersion := pkgVersion(path)
	if oldVersion == newVersion {
		fatalf("fatal: package %s version %s is already current, nothing to do", path, version)
	}

	// Tidy
	if err := execCommandRun("go", "mod", "tidy"); err != nil {
		fatal(err)
	}

	// If vendor/modules.txt exists, vendor
	var skipVendor bool
	_, err = os.Stat("vendor/modules.txt")
	if err != nil {
		if os.IsNotExist(err) {
			skipVendor = true
		}

		fatal(err)
	}

	if !skipVendor {
		if err := execCommandRun("go", "mod", "tidy"); err != nil {
			fatal(err)
		}
	}

	// Commit changes on new branch
	pathSplit := strings.Split(path, "/")
	project := pathSplit[len(pathSplit)-1]
	branch := "update-" + project + "-" + newVersion
	if err := execCommandRun("git", "checkout", "-b", branch); err != nil {
		fatal(err)
	}
	if err := execCommandRun("git", "add", "--all"); err != nil {
		fatal(err)
	}

	// Build commit data. Add a URL if we have a GH link, redirecting
	// to the tree for the release.
	data := commitTemplateData{
		Project: project,
		Version: newVersion,
		Path:    path,
	}

	if pathSplit[0] == "github.com" {
		data.URL = "https://" + path + "@" + version
	}

	b := new(bytes.Buffer)
	if err := commitTemplate.Execute(b, data); err != nil {
		fatal(err)
	}

	cmd := execCommand("git", "commit", "-F", "-")
	cmd.Stdin = b
	cmd.Stdout = os.Stdout
	if err := cmd.Run(); err != nil {
		fatal(err.Error() + "\nWARNING: repository is in an unclean state; please correct before trying again")
	}

	// Push to origin
	if err := execCommandRun("git", "push", "origin", branch); err != nil {
		fatal(err.Error() + "\nWARNING: commit succeeded but push failed; push manually to correct")
	}

	fmt.Printf("path %s successfully updated to version %s.\n", path, version)
}
