# Contributing to nexora-migrate

Thanks for helping. This file explains how to report a problem and how to send a
change.

## Reporting a problem

Open an issue and include:

- the source panel and its version (for example *3x-ui v3.8.5*), and the Nexora
  version;
- which channel you used: the database file or the running panel;
- what you expected and what happened, with the exact message from the wizard
  (the saved report is the easiest way to share it).

**Never attach a panel database, a backup or an unedited report.** They hold
every user's credentials and subscription token. If a problem only shows with
your data, describe the shape of the row (protocol, transport, which fields are
set) with the secrets replaced.

**Security problems** go to a private advisory, not a public issue: use
*Security → Report a vulnerability* on this repository.

## Sending a change

1. Fork the repository and create a branch from `main`.
2. Make the change. See [docs/development.md](docs/development.md) for building
   and the layout.
3. Before you push, run:

   ```sh
   go vet ./...
   go test -race ./...
   gofmt -l .        # must print nothing
   ```

4. Open a pull request that says what changed and why. CI must pass.

Keep pull requests to one subject. A new reader, a conversion fix and a
translation are three pull requests.

### Readers and conversions

- **Pin the upstream shape in a test.** A reader change comes with a test that
  builds the source panel's data in that exact shape: a SQLite fixture built in
  the test for s-ui, 3x-ui and x-ui, or an `httptest` server for the API-backed
  panels. Name the upstream version or commit the shape comes from.
- **Keep old versions working.** Upstream panels change their schema between
  releases. Read the new shape and keep reading the old one, instead of
  replacing it.
- **Never drop anything silently.** When something cannot come across, the item
  gets a note (`AddNote`) or is blocked (`Block`) with a sentence that tells the
  operator what happened and what to do.
- **Write only through Nexora's public API.** This tool does not write to
  Nexora's database and does not need changes to Nexora.

### Translations

The wizard's text is in `internal/web/assets/i18n.json`, in English, Persian,
Chinese, Russian and Vietnamese. A new or changed string needs all five.
`go test ./internal/web/` fails if a language is missing a key, or if the page
asks for a key that does not exist.

The user guides are in `docs/guide.<language>.md`, one file per language. A
change to how the tool behaves updates all five guides. If you cannot write one
of the languages, say so in the pull request and the change will be translated
before release.

## Licence

nexora-migrate is licensed under the
[GNU General Public License v3.0](LICENSE). By sending a contribution you agree
that it is released under the same licence.
