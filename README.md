# depbump

`depbump` is a very simple command that can be used to bump a Go module, and
commit its changes. It's designed to be used in automated tooling to help the
process of keeping certain dependencies up to date.

It also supports automating a PR against the repository (GitHub only). To enable
the functionality, run depbump with the `GITHUB_TOKEN` environment variable (or
the one supplied to `-token`) set to the token for the user/identity you want to
have the PR submitted as.

## Usage

`depbump [-nopush|-nopr|-token TOKEN_NAME|-version VERSION] PATH [COMMAND]`

-version will update to a specific version of the dependency. 

Use `-nopush` to skip the push to origin. You can use this if you need to
preview the changes or amend the commit later.

`-token` can be used to override the default token environment variable setting
of `GITHUB_TOKEN`.

If you are pushing, but don't want the PR to go through, you can use `-nopr`.
The PR is also skipped if `GITHUB_TOKEN` (or the variable configured by
`-token`) is missing.

COMMAND can be used to supply a post-update command. You can use this to run any
commands or scripts to update any other files post-update. The command line can
be Go [templated](https://golang.org/pkg/text/template/) according the specific
structure:

```
type commitTemplateData struct {
	Project string
	Owner   string // The repository "owner" (aka organization)
	Version string // If this is a semver version, it has the "v" removed.
	Target  string
	Path    string
	URL     string
	Vendor  bool
}
```
