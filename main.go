package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"html/template"
	"io/ioutil"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"regexp"
	"strings"
)

type commitTemplateData struct {
	Project string
	Owner   string
	Version string
	Target  string
	Path    string
	URL     string
	Vendor  bool
}

const gitHubPREndpointFmt = "https://api.github.com/repos/%s/%s/pulls"

var commitTemplate = template.Must(
	template.New("commit-template").Parse(strings.TrimSpace(`
modules: upgrade {{.Project}} to {{.Version}}

This updates:
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

const help = "usage: depbump [-nopush|-nopr|-version VERSION] PATH [COMMAND]"

func main() {
	if len(os.Args) < 2 {
		fatal(help)
	}

	var path string
	var version string
	var postCmdRaw []string
	push := true
	pr := true

	for i := 1; i < len(os.Args); i++ {
		arg := os.Args[i]
		if path == "" && strings.HasPrefix(arg, "-") {
			switch arg {
			case "-nopush":
				push = false

			case "-nopr":
				pr = false

			case "-version":
				if i+1 >= len(os.Args) {
					// Not enough arguments
					fatal("fatal: not enough arguments\n" + help)
				}

				i++
				version = os.Args[i]

			default:
				fatalf("fatal: invalid argument %q\n%s\n", arg, help)
			}

			continue
		}

		if path == "" {
			path = arg
		} else {
			postCmdRaw = append(postCmdRaw, arg)
		}
	}

	if path == "" {
		fatal("fatal: path is empty\n" + help)
	}

	// Do a pre-tidy. Some pre-build steps in CI (go mod download) can
	// cause updates in go.sum, which will falsely trigger the dirty
	// repo check below.
	if err := execCommandRun("go", "mod", "tidy"); err != nil {
		fatal(err)
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

	// Check origin to see if we can support a pull request
	var remoteOwner, remoteRepo string
	if push {
		out, err = execCommand("git", "remote", "get-url", "origin").Output()
		if err != nil {
			fatal(err)
		}

		rawURL := strings.TrimSpace(string(out))

		var host string
		var uri string
		remoteURL, err := url.Parse(rawURL)
		if err != nil {
			// Check to see if remote is a SSH URL
			sshParts := strings.SplitN(rawURL, ":", 2)
			userHost := strings.Split(sshParts[0], "@")
			if len(userHost) != 2 {
				// Fall back to URL error
				fatalf("fatal: error parsing remote URL: %s", err)
			}

			host = userHost[1]
			uri = sshParts[1]
		} else {
			host = remoteURL.Host
			uri = path
		}

		if host != "github.com" || os.Getenv("GITHUB_TOKEN") == "" {
			pr = false
		} else {
			ownerRepo := strings.Split(uri, "/")
			if len(ownerRepo) != 2 {
				fatal("fatal: expected repo remote URI to follow OWNER/REPO format")
			}

			remoteOwner = ownerRepo[0]
			remoteRepo = strings.TrimSuffix(ownerRepo[1], ".git")
		}
	} else {
		pr = false
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
		fmt.Printf("package %s version %s is already current, nothing to do. Exiting.\n", path, oldVersion)
		os.Exit(0)
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

	// Build commit template data. Add a URL if we have a GH link,
	// redirecting to the tree for the release.
	pathSplit := strings.Split(path, "/")
	project := pathSplit[len(pathSplit)-1]
	data := commitTemplateData{
		Project: project,
		Path:    path,
		Target:  target,
		Vendor:  !skipVendor,
	}
	vre := regexp.MustCompile(`^v\d+\.\d+\.\d+$`)
	if vre.MatchString(newVersion) {
		data.Version = newVersion[1:]
	}

	if pathSplit[0] == "github.com" {
		// Add the correct tree based version.
		var tree string
		if vre.MatchString(newVersion) {
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

	// If we have a post-run command, run it now
	if len(postCmdRaw) > 0 {
		fmt.Println("version has been updated, and post-command detected")
		// Template it
		postCmd := make([]string, len(postCmdRaw))
		for i, c := range postCmdRaw {
			s := new(strings.Builder)
			t, err := template.New("cmd").Parse(c)
			if err != nil {
				fatalf("error building post-update command: %s\n", err)
			}

			if err := t.Execute(s, data); err != nil {
				fatalf("error building post-update command: %s\n", err)
			}

			postCmd[i] = s.String()
		}

		fmt.Println("running:", strings.Join(postCmd, " "))
		if err := execCommandRun(postCmd[0], postCmd[1:]...); err != nil {
			fatalf("error running post-update command: %s\n", err)
		}
	}

	// Get existing branch
	out, err = execCommand("git", "rev-parse", "--abbrev-ref", "HEAD").Output()
	if err != nil {
		fatal(err)
	}

	oldBranch := strings.TrimSpace(string(out))

	// Commit changes on new branch.
	branch := "update-" + project + "-" + newVersion

	// Check to see if remote exists for this branch first if we are
	// pushing; if it does, we need to abort.
	out, err = execCommand("git", "ls-remote", "--heads", "origin", branch).Output()
	if err != nil {
		fatalf("fatal: error checking for remote branch: %s\n", err)
	}

	if len(out) > 0 {
		fmt.Println("remote branch for version already exists, exiting. This could possibly be due to a pending update.\ndetails:")
		fmt.Println(string(out))

		// Attempt to revert the working tree back to HEAD.
		if err := execCommandRun("git", "reset", "--hard", "HEAD"); err != nil {
			fatalf("fatal: could not reset repository back to original state: %s\n", err)
		}

		os.Exit(0)
	}

	if err := execCommandRun("git", "checkout", "-b", branch); err != nil {
		fatal(err)
	}
	if err := execCommandRun("git", "add", "--all"); err != nil {
		fatal(err)
	}

	b := new(bytes.Buffer)
	if err := commitTemplate.Execute(b, data); err != nil {
		fatal(err)
	}

	// Save the commit title and body first, for possible use in a PR.
	titleBody := strings.SplitN(b.String(), "\n\n", 2)

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

	// Submit PR
	var prURL string
	if pr {
		fmt.Println("creating pull request...")

		payload := map[string]interface{}{
			"title": titleBody[0],
			"body":  titleBody[1],
			"head":  branch,
			"base":  "master",
		}

		payloadB := new(bytes.Buffer)
		if err := json.NewEncoder(payloadB).Encode(payload); err != nil {
			fatalf("fatal: error encoding pull request payload: %s\n", err)
		}

		req, err := http.NewRequest("POST", fmt.Sprintf(gitHubPREndpointFmt, remoteOwner, remoteRepo), payloadB)
		if err != nil {
			fatalf("fatal: error creating request: %s\n", err)
		}

		req.Header.Add("Authorization", fmt.Sprintf("bearer %s", os.Getenv("GITHUB_TOKEN")))
		req.Header.Add("Content-Type", "application/json")

		client := &http.Client{}
		resp, err := client.Do(req)
		if err != nil {
			fatalf("fatal: error creating pull request: %s\n", err)
		}

		respBytes, err := ioutil.ReadAll(resp.Body)
		if err != nil {
			fatalf("fatal: error reading response body after creating pull request: %s\n\nWARNING: pull request status unknown, check you repository\n", err)
		}

		respData := make(map[string]interface{})
		if resp.Header.Get("Content-Type") == "application/json; charset=utf-8" {
			if err := json.Unmarshal(respBytes, &respData); err != nil {
				fatalf("fatal: error reading response JSON: %s\n\nWARNING: pull request status unknown, check your repository\n", err)
			}
		}

		switch resp.StatusCode {
		case http.StatusCreated:
			if u, ok := respData["html_url"]; ok {
				prURL = u.(string)
			} else {
				fmt.Println("WARNING: pull request successfully created, but no URL was returned")
			}

		default:
			fatalf("fatal: error creating pull request (%s): %s\n", resp.Status, respBytes)
		}
	}

	fmt.Printf("\npath %s successfully updated to version %s.\n", path, newVersion)
	if prURL != "" {
		fmt.Printf("pull request has been created at:\n    %s\n", prURL)
	}
}
