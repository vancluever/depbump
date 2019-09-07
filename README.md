# depbump

`depbump` is a very simple command that can be used to bump a Go module, and
commit its changes. It's designed to be used in automated tooling to help the
process of keeping certain dependencies up to date.

## Usage

`depbump [-nopush|-version VERSION] PATH [COMMAND]`

-version will update to a specific version of the dependency. 

Use `-nopush` to skip the push to origin. You can use this if you need to
preview the changes or amend the commit later.

COMMAND can be used to supply a post-update command. You can use this to run any
commands or scripts to update any other files post-update. The command line can
be Go [templated](https://golang.org/pkg/text/template/) according the specific
structure:

```
type commitTemplateData struct {
	Project string
	Version string // If this is a semver version, it has the "v" removed.
	Target  string
	Path    string
	URL     string
	Vendor  bool
}
```
