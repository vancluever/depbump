package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"html/template"
	"os"
	"os/exec"
	"regexp"
	"strings"
)

type commitTemplateData struct {
	Project string
	Version string
	Target  string
	Path    string
	URL     string
	Vendor  bool
}

var commitTemplate = template.Must(
	template.New("commit-template").Parse(strings.TrimSpace(`
modules: upgrade {{.Project}} to {{.Version}}

This updates
  {{.Path}}

To version {{.Version}}.

Executed via:

  go get {{.Target}}
  go mod tidy
{{if .Vendor}}  go mod vendor{{- end}}

For details on changes, see the project's release page.
{{if .URL }}  {{.URL}}{{- end}}

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

	fatalf("package %q not found in go.mod, cannot get version\n", path)
	return ""
}

func main() {
	if len(os.Args) < 2 {
		fatal("usage: depbump PATH [VERSION]")
	}

	var path string
	var version string
	var push bool

	for _, arg := range os.Args[1:] {
		if strings.HasPrefix(arg, "-") {
			switch arg {
			case "-push":
				push = true

			default:
				fatalf("fatal: invalid argument %q\nusage: depbump [-push] PATH [VERSION]\n", arg)
			}
		}

		if path != "" && version != "" {
			break
		}

		if path == "" {
			path = arg
		} else {
			version = arg
		}
	}

	if path == "" {
		fatal("fatal: path is empty\nusage: depbump [-push] PATH [VERSION]")
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
		fatalf("fatal: package %s is already at version %s\n", path, version)
	}

	// Upgrade package
	target := path
	if version != "" {
		target = path + "@" + version
	}

	if err := execCommandRun("go", "get", target); err != nil {
		fatal(err)
	}

	newVersion := pkgVersion(path)
	if oldVersion == newVersion {
		fatalf("fatal: package %s version %s is already current, nothing to do\n", path, oldVersion)
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
		} else {
			fatal(err)
		}
	}

	if !skipVendor {
		if err := execCommandRun("go", "mod", "vendor"); err != nil {
			fatal(err)
		}
	}

	// Get existing branch
	out, err = execCommand("git", "rev-parse", "--abbrev-ref", "HEAD").Output()
	if err != nil {
		fatal(err)
	}

	oldBranch := strings.TrimSpace(string(out))

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
		Target:  target,
		Vendor:  !skipVendor,
	}

	if pathSplit[0] == "github.com" {
		// Add the correct tree based version.
		var tree string
		if regexp.MustCompile(`^v\d+\.\d+\.\d+$`).MatchString(newVersion) {
			// Semver - versions otherwise start with a timestamp
			tree = newVersion
		} else {
			// Version is in format FAKEVER-TIMESTAMP-COMMIT, so we need to grab
			// the hash
			s := strings.Split(newVersion, "-")
			tree = s[len(s)-1]
		}

		data.URL = "https://" + path + "/tree/" + tree
	}

	b := new(bytes.Buffer)
	if err := commitTemplate.Execute(b, data); err != nil {
		fatal(err)
	}

	cmd := execCommand("git", "commit", "-F", "-")
	cmd.Stdin = b
	cmd.Stdout = os.Stdout
	if err := cmd.Run(); err != nil {
		fatal(err.Error() + "\n\nWARNING: repository is in an unclean state; please correct before trying again")
	}

	// Push to origin
	if push {
		if err := execCommandRun("git", "push", "origin", branch); err != nil {
			fatal(err.Error() + "\n\nWARNING: commit succeeded but push failed; push manually to correct")
		}
	}

	// Checkout old branch
	if err := execCommandRun("git", "checkout", oldBranch); err != nil {
		fatal(err.Error() + "\n\nWARNING: update succeeded, but cannot checkout old branch")
	}

	fmt.Printf("\npath %s successfully updated to version %s.\n", path, newVersion)
}
