# depbump

`depbump` is a very simple command that can be used to bump a Go module, and
commit its changes. It's designed to be used in automated tooling to help the
process of keeping certain dependencies up to date.

## Usage

`depbump [-nopush] PATH [VERSION]`

`VERSION` is optional and is the version (or ref) that you want to update to.

Use `-nopush` to skip the push to origin. You can use this if you need to
preview the changes or amend the commit later.
